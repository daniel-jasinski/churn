package writer

import (
	"math/rand"
	"strings"
	"testing"

	"churn/internal/event"
)

// TestSubmitRejectsReservedCauses pins §5.2: causes is reserved and null in
// V1 — the writer refuses to author an event carrying one, mirroring the
// import-side rejection of non-null causes columns.
func TestSubmitRejectsReservedCauses(t *testing.T) {
	st := openStore(t, t.TempDir())
	defer st.Close()
	w, err := Open(st, Options{Now: (&testClock{ms: 1721390000000}).now, Entropy: rand.New(rand.NewSource(9))})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	before := w.Projection().LastSeq
	target := "01AAAAAAAAAAAAAAAAAAAAAAA0"
	_, err = w.Submit("daniel", []Command{{
		Type: event.TypeProjectCreated, V: 1, Entity: "pr_1",
		Payload: event.ProjectCreated{Name: "P"}, Causes: &target,
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), "causes is reserved") {
		t.Fatalf("got %v, want a reserved-causes rejection", err)
	}
	if got := w.Projection().LastSeq; got != before {
		t.Fatalf("rejected batch advanced the log: %d → %d", before, got)
	}
}
