package store

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLockHelperProcess is not a test: it is the body of the second process
// spawned by TestLockExclusiveAcrossProcesses (the standard test-binary
// re-exec pattern). It tries to open the store in CHURN_LOCK_DIR and prints
// the outcome.
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv("CHURN_LOCK_HELPER") != "1" {
		t.Skip("helper process only")
	}
	dir := os.Getenv("CHURN_LOCK_DIR")
	s, err := Open(dir)
	if err != nil {
		os.Stdout.WriteString("HELPER:LOCKED\n")
		return
	}
	s.Close()
	os.Stdout.WriteString("HELPER:ACQUIRED\n")
}

// TestLockExclusiveAcrossProcesses verifies that churn.lock actually
// excludes a *real second process* — on Windows via CreateFile share-mode
// exclusion, elsewhere via flock — not merely a second handle in this
// process.
func TestLockExclusiveAcrossProcesses(t *testing.T) {
	dir := t.TempDir()

	runHelper := func() string {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$", "-test.v")
		cmd.Env = append(os.Environ(), "CHURN_LOCK_HELPER=1", "CHURN_LOCK_DIR="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper process failed: %v\n%s", err, out)
		}
		return string(out)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := runHelper()
	if !strings.Contains(out, "HELPER:LOCKED") {
		t.Fatalf("second process acquired the lock while we hold it:\n%s", out)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	out = runHelper()
	if !strings.Contains(out, "HELPER:ACQUIRED") {
		t.Fatalf("second process could not acquire the released lock:\n%s", out)
	}
}

// TestLockHolderProcess is the body of the process spawned by
// TestNoStaleLockAfterHolderKilled: it opens the store, announces it, and
// then blocks until killed.
func TestLockHolderProcess(t *testing.T) {
	if os.Getenv("CHURN_LOCK_HOLDER") != "1" {
		t.Skip("helper process only")
	}
	s, err := Open(os.Getenv("CHURN_LOCK_DIR"))
	if err != nil {
		os.Stdout.WriteString("HELPER:FAILED\n")
		return
	}
	defer s.Close() // never reached: we are killed
	os.Stdout.WriteString("HELPER:HOLDING\n")
	time.Sleep(5 * time.Minute)
}

// TestNoStaleLockAfterHolderKilled backs the claim in the lock docs that a
// crash never leaves a stale lock: hard-kill the holding process (no
// cleanup runs) and assert the lock is immediately acquirable.
func TestNoStaleLockAfterHolderKilled(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHolderProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "CHURN_LOCK_HOLDER=1", "CHURN_LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	sc := bufio.NewScanner(stdout)
	holding := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), "HELPER:FAILED") {
			t.Fatal("holder process could not open the store")
		}
		if strings.Contains(sc.Text(), "HELPER:HOLDING") {
			holding = true
			break
		}
	}
	if !holding {
		t.Fatal("holder process never acquired the lock")
	}

	// While held, we must be excluded.
	if _, err := Open(dir); err == nil {
		t.Fatal("acquired the lock while the holder process is alive")
	}

	// Hard kill: no defers, no Close, no cleanup of any kind runs.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait() // reap; error is expected (killed)

	s := openEventually(t, dir, 10*time.Second)
	s.Close()
}

// openEventually retries Open until the deadline — the OS releases a killed
// process's handles asynchronously, so allow a brief grace period.
func openEventually(t *testing.T, dir string, timeout time.Duration) *Store {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		s, err := Open(dir)
		if err == nil {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock still held %v after holder was killed: %v", timeout, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
