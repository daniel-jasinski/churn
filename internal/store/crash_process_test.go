package store

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"churn/internal/event"
)

const crashBatchSize = 3

// TestCrashWriterProcess is the body of the process spawned by
// TestCrashLeavesOnlyWholeBatches: it appends fixed-size batches in a tight
// loop, announcing each commit, until it is killed mid-loop.
func TestCrashWriterProcess(t *testing.T) {
	if os.Getenv("CHURN_CRASH_WRITER") != "1" {
		t.Skip("helper process only")
	}
	s, err := Open(os.Getenv("CHURN_LOCK_DIR"))
	if err != nil {
		os.Stdout.WriteString("HELPER:FAILED\n")
		return
	}
	defer s.Close() // never reached: we are killed
	for b := 1; b <= 100000; b++ {
		batch := make([]event.Envelope, crashBatchSize)
		for i := range batch {
			batch[i] = event.Envelope{
				ID:     fmt.Sprintf("B%06d-%d", b, i),
				Origin: "wr_crash",
				Batch:  fmt.Sprintf("batch%06d", b),
				TS:     "2026-07-19T10:00:00.000Z",
				Actor:  "crash",
				Type:   event.TypeWriterStarted,
				V:      1,
				Data:   []byte(`{}`),
			}
		}
		refs := []Ref{{Event: 0, EntityID: fmt.Sprintf("ent%06d", b), Role: "subject"}}
		if _, err := s.AppendBatch(batch, refs); err != nil {
			fmt.Printf("HELPER:ERROR %v\n", err)
			return
		}
		fmt.Printf("HELPER:COMMITTED %d\n", b)
	}
}

// TestCrashLeavesOnlyWholeBatches is the crash-shaped atomicity test: a real
// process is hard-killed at a random moment while appending batches in a
// loop; after reopening, the log must contain only whole batches with
// contiguous seq — never a torn one — and event_refs must agree.
func TestCrashLeavesOnlyWholeBatches(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashWriterProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "CHURN_CRASH_WRITER=1", "CHURN_LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	// Let it commit a few batches, then kill at a random moment while the
	// append loop is running full tilt — the kill lands wherever it lands,
	// including mid-transaction.
	sc := bufio.NewScanner(stdout)
	committed := 0
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "HELPER:FAILED") || strings.Contains(line, "HELPER:ERROR") {
			t.Fatalf("writer process: %s", line)
		}
		if strings.Contains(line, "HELPER:COMMITTED") {
			committed++
			if committed >= 3 {
				break
			}
		}
	}
	if committed < 3 {
		t.Fatal("writer process never got going")
	}
	time.Sleep(time.Duration(rand.Intn(120)) * time.Millisecond)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()  // reap; error is expected (killed)
	go func() { // drain the pipe so nothing blocks
		for sc.Scan() {
		}
	}()

	s := openEventually(t, dir, 10*time.Second)
	defer s.Close()

	var evs []event.Envelope
	if err := s.Scan(func(ev event.Envelope) error { evs = append(evs, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(evs) < 3*crashBatchSize {
		t.Fatalf("expected at least the 3 announced batches, got %d events", len(evs))
	}
	if len(evs)%crashBatchSize != 0 {
		t.Fatalf("%d events is not a whole number of %d-event batches: a batch tore",
			len(evs), crashBatchSize)
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("seq gap: event %d has seq %d", i, ev.Seq)
		}
		wantBatch := fmt.Sprintf("batch%06d", i/crashBatchSize+1)
		wantID := fmt.Sprintf("B%06d-%d", i/crashBatchSize+1, i%crashBatchSize)
		if ev.Batch != wantBatch || ev.ID != wantID {
			t.Fatalf("event %d: batch %q id %q, want %q %q — batches interleaved or torn",
				i, ev.Batch, ev.ID, wantBatch, wantID)
		}
	}

	// event_refs were written in the same transactions: exactly one per
	// surviving batch.
	var refCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM event_refs`).Scan(&refCount); err != nil {
		t.Fatal(err)
	}
	if refCount != len(evs)/crashBatchSize {
		t.Fatalf("event_refs count %d, want %d (one per whole batch)", refCount, len(evs)/crashBatchSize)
	}
}
