package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"churn/internal/store"
)

// TestServeRequiresInitToCreateWorkspace pins the creation policy: serve on a
// directory with no workspace must fail and create NOTHING unless --init says
// so. --data defaults to ".", so the permissive alternative turns a typo (or
// the wrong working directory) into an empty workspace that looks exactly
// like having lost the real one.
func TestServeRequiresInitToCreateWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  func(t *testing.T) string
	}{
		{"missing directory", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "no-such-workspace")
		}},
		{"empty directory", func(t *testing.T) string { return t.TempDir() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir(t)
			_, _, err := runCLI(t, "serve", "--data", dir, "--listen", "127.0.0.1:0", "--no-open")
			if err == nil || !strings.Contains(err.Error(), "no workspace at") {
				t.Fatalf("serve without --init: got %v, want a no-workspace error", err)
			}
			if !strings.Contains(err.Error(), "--init") {
				t.Errorf("the error should point at --init: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); !os.IsNotExist(err) {
				t.Errorf("serve created a workspace despite refusing to start: %v", err)
			}
		})
	}
}

// TestServeInitOnExistingWorkspaceWarns pins that --init is advisory, not
// destructive: pointed at a directory that already holds a workspace it opens
// it as usual and says the flag did nothing.
func TestServeInitOnExistingWorkspaceWarns(t *testing.T) {
	dir := t.TempDir()
	buildWorkspace(t, dir)

	var stderr strings.Builder
	creating, err := checkServeDataDir(dir, true, &stderr)
	if err != nil {
		t.Fatalf("checkServeDataDir on an existing workspace: %v", err)
	}
	if creating {
		t.Error("an existing workspace must not be reported as newly created")
	}
	if !strings.Contains(stderr.String(), "--init ignored") {
		t.Errorf("want an --init-ignored warning, got %q", stderr.String())
	}
}

// TestServeRefusesPartialRestore pins the interrupted-import guard: a
// leftover workspace.db.partial refuses to serve with or without --init.
// Creating a fresh workspace beside it would hide the wreckage of a restore
// the operator believes succeeded.
func TestServeRefusesPartialRestore(t *testing.T) {
	for _, init := range []bool{false, true} {
		dir := t.TempDir()
		partial := filepath.Join(dir, store.RestoreDBFileName)
		if err := os.WriteFile(partial, []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
		args := []string{"serve", "--data", dir, "--listen", "127.0.0.1:0", "--no-open"}
		if init {
			args = append(args, "--init")
		}
		_, _, err := runCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), "partial restore") {
			t.Fatalf("serve with --init=%v over a partial restore: got %v, want a partial-restore refusal", init, err)
		}
		if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); !os.IsNotExist(err) {
			t.Errorf("serve minted a workspace beside the partial restore: %v", err)
		}
		// The partial itself must survive: it is the user's data to salvage.
		if _, err := os.Stat(partial); err != nil {
			t.Errorf("the partial restore was disturbed: %v", err)
		}
	}
}

// TestServeReportsLiveImportAsInUse pins the diagnosis that separates an
// interrupted restore from one running right now. Both hold
// workspace.db.partial, so a check that looks only at the file tells the
// operator to delete the database of a live import — the exact data loss the
// partial-restore guard exists to prevent. The lock is the discriminator.
func TestServeReportsLiveImportAsInUse(t *testing.T) {
	dir := t.TempDir()
	st, err := store.OpenRestore(dir) // an import-log in progress
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	_, _, err = runCLI(t, "serve", "--data", dir, "--init", "--listen", "127.0.0.1:0", "--no-open")
	if err == nil {
		t.Fatal("serve started while an import held the workspace")
	}
	if !strings.Contains(err.Error(), "in use by another churn process") {
		t.Errorf("a live import must be reported as in-use, got: %v", err)
	}
	if strings.Contains(err.Error(), "delete the file") {
		t.Errorf("serve advised deleting a live import's database: %v", err)
	}
}

// TestServeShutsDownWithSSEClientAttached pins the drain order: an open SSE
// stream is a request that never finishes on its own, so serve must end the
// streams (api.Shutdown) before draining in-flight requests. Drop that call
// and the drain stalls until the grace period expires and shutdown reports a
// timeout; here it must return cleanly, well inside it.
func TestServeShutsDownWithSSEClientAttached(t *testing.T) {
	dir := t.TempDir()
	buildWorkspace(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, errOut := &syncBuf{}, &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--data", dir, "--listen", "127.0.0.1:0", "--no-open"},
			strings.NewReader(""), out, errOut)
	}()
	addr := waitForListen(t, out, errOut)

	// Hold the stream open, unread, for the whole shutdown.
	resp, err := http.Get(addr + "/api/v1/events/stream")
	if err != nil {
		t.Fatalf("opening the SSE stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned: %v", err)
		}
	case <-time.After(shutdownGrace + 5*time.Second):
		t.Fatal("serve did not shut down with an SSE client attached")
	}
	if elapsed := time.Since(start); elapsed >= shutdownGrace {
		t.Errorf("shutdown took %s: the SSE stream was drained on the timeout, not ended", elapsed)
	}
}

// waitForListen blocks until serve announces its address, and returns it.
func waitForListen(t *testing.T, out, errOut *syncBuf) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if s := out.String(); strings.Contains(s, "listening on http://") {
			s = s[strings.Index(s, "http://"):]
			return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not report an address; stdout=%q stderr=%q", out.String(), errOut.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestServeInitCreatesAndAnnounces pins that --init on an empty directory
// creates the workspace and says so — the message that makes a mistyped
// --data recognizable at a glance rather than mistakable for data loss.
func TestServeInitCreatesAndAnnounces(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out, errOut := &syncBuf{}, &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--data", dir, "--init", "--listen", "127.0.0.1:0", "--no-open"},
			strings.NewReader(""), out, errOut)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "listening on http://") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start; stdout=%q stderr=%q", out.String(), errOut.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "created a new workspace") {
		t.Errorf("serve --init should announce the creation; stdout=%q", out.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down on context cancel")
	}
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); err != nil {
		t.Fatalf("serve --init did not create the workspace: %v", err)
	}
	// A clean shutdown releases the lock, so the directory reopens.
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("reopening after a clean shutdown: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
}
