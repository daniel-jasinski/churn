//go:build !windows

package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestServeShutsDownOnSIGTERM pins SIGTERM as a graceful-shutdown signal.
// SIGTERM is what `docker stop`, systemd, and a plain `kill` send; trapping
// only SIGINT leaves the default disposition in place, so the process dies
// where it stands — no drain, no writer stop, no clean database close. A
// regression shows up here as a signal-killed exit status instead of 0.
//
// This needs a real process, so it drives the built binary rather than the
// in-process run(). Unix-only: Windows has no SIGTERM to deliver.
//
// The second-signal force-exit (watchSecondSignal) is deliberately NOT pinned
// here. Making it deterministic means wedging the drain so the second signal
// lands while shutdown is still running, and with no SSE clients attached
// shutdown finishes in microseconds. Doing it properly would mean exposing a
// slow-handler seam from cmdServe into production code, which costs more than
// the coverage is worth.
func TestServeShutsDownOnSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping binary signal test")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "churn")
	build := exec.Command("go", "build", "-o", bin, "churn/cmd/churn")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	data := filepath.Join(tmp, "ws")
	srv := exec.Command(bin, "serve", "--data", data, "--init",
		"--listen", "127.0.0.1:0", "--no-open")
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		t.Fatalf("starting %s: %v", bin, err)
	}
	defer func() { _ = srv.Process.Kill() }()

	// Wait until it is actually serving, so the signal exercises the shutdown
	// path rather than racing startup.
	banner := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "listening on http://") {
				banner <- sc.Text()
				return
			}
		}
		banner <- ""
	}()
	select {
	case line := <-banner:
		if line == "" {
			t.Fatal("server exited before announcing its address")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the serve banner")
	}

	if err := srv.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve did not exit cleanly on SIGTERM: %v", err)
		}
	case <-time.After(shutdownGrace + 15*time.Second):
		t.Fatal("serve ignored SIGTERM")
	}

	// A graceful shutdown releases the lock and leaves a reopenable workspace.
	if _, _, err := runCLI(t, "ls", "things", "--data", data); err != nil {
		t.Fatalf("workspace unusable after SIGTERM shutdown: %v", err)
	}
}
