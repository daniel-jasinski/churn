package server

import (
	"testing"
)

// batchInfo is one batch's boundary read back from history.
type batchInfo struct {
	firstSeq, lastSeq int64
	ts                string
}

// batches groups the whole log by batch id, in seq order.
func (e *env) batches() []batchInfo {
	e.t.Helper()
	hist := e.call("GET", "/api/v1/history", nil, 200)
	var out []batchInfo
	lastBatch := ""
	for _, raw := range hist["events"].([]any) {
		ev := raw.(map[string]any)
		seq := int64(ev["seq"].(float64))
		if b := str(ev, "batch"); b != lastBatch {
			lastBatch = b
			out = append(out, batchInfo{firstSeq: seq, lastSeq: seq, ts: str(ev, "ts")})
		} else {
			out[len(out)-1].lastSeq = seq
		}
	}
	return out
}

// TestAsOfSnapping builds a log with known batch boundaries and checks §3.6:
// cursors snap DOWN to the last COMPLETE batch at or before them.
func TestAsOfSnapping(t *testing.T) {
	e := newEnv(t)
	f := e.seed() // several single-event batches after the seed batch

	// A multi-event batch: two things in one commit.
	com := e.call("POST", "/api/v1/batch", map[string]any{
		"mode": "commit",
		"operations": []map[string]any{
			{"op": "create", "kind": "thing", "data": map[string]any{"project": f.project, "name": "m1", "type": f.typ}},
			{"op": "create", "kind": "thing", "data": map[string]any{"project": f.project, "name": "m2", "type": f.typ}},
		},
	}, 200)
	_ = com
	// One more batch after it.
	e.thing(f, "later")

	bs := e.batches()
	if len(bs) < 4 {
		t.Fatalf("want at least 4 batches, got %d", len(bs))
	}
	multi := bs[len(bs)-2] // the two-event batch
	if multi.lastSeq != multi.firstSeq+1 {
		t.Fatalf("expected multi-event batch, got %+v", multi)
	}
	prev := bs[len(bs)-3]
	last := bs[len(bs)-1]
	graph := "/api/v1/projects/" + f.project + "/graph"

	// as_of = exact last seq of the multi batch → that batch included.
	m := e.call("GET", graph+"?as_of="+itoa(multi.lastSeq), nil, 200)
	if got := int64(m["as_of"].(map[string]any)["seq"].(float64)); got != multi.lastSeq {
		t.Fatalf("as_of=%d snapped to %d, want %d", multi.lastSeq, got, multi.lastSeq)
	}

	// as_of = seq INSIDE the multi batch (its first event) → snaps DOWN to
	// the previous complete batch.
	m = e.call("GET", graph+"?as_of="+itoa(multi.firstSeq), nil, 200)
	if got := int64(m["as_of"].(map[string]any)["seq"].(float64)); got != prev.lastSeq {
		t.Fatalf("as_of=%d (mid-batch) snapped to %d, want %d", multi.firstSeq, got, prev.lastSeq)
	}

	// as_of = ts of the multi batch → includes it, not the later batch
	// (every batch has a distinct stepped-clock ts in this harness).
	m = e.call("GET", graph+"?as_of="+multi.ts, nil, 200)
	asOf := m["as_of"].(map[string]any)
	if got := int64(asOf["seq"].(float64)); got != multi.lastSeq {
		t.Fatalf("as_of=%s snapped to seq %d, want %d", multi.ts, got, multi.lastSeq)
	}
	if last.ts <= multi.ts {
		t.Fatalf("harness violated distinct batch timestamps: %q then %q", multi.ts, last.ts)
	}

	// A ts strictly between two batch commits snaps down to the earlier one,
	// and the past graph genuinely lacks the later thing.
	if got := len(m["things"].([]any)); got != 2 {
		t.Fatalf("graph as of multi batch has %d things, want 2 (m1, m2 without 'later'... ", got)
	}

	// as_of before the first batch → 404 (documented decision: there was no
	// workspace to view).
	mm := e.call("GET", graph+"?as_of=2000-01-01T00:00:00Z", nil, 404)
	if errKind(mm) != "not_found" {
		t.Fatalf("pre-log as_of: %v", mm)
	}

	// as_of before the project existed → 404 for the project itself.
	e.call("GET", graph+"?as_of=1", nil, 404)

	// Live graph (no as_of) has no as_of block and all things.
	live := e.call("GET", graph, nil, 200)
	if _, present := live["as_of"]; present {
		t.Fatalf("live graph carries as_of: %v", live["as_of"])
	}
	if got := len(live["things"].([]any)); got != 3 {
		t.Fatalf("live graph things: %d, want 3", got)
	}

	// Malformed cursors are 400.
	e.call("GET", graph+"?as_of=yesterday", nil, 400)
	e.call("GET", graph+"?as_of=0", nil, 400)
}

// TestAsOfSnapshotStatuses: the replayed projection derives statuses at that
// moment — a thing that was working then reads working, though it is done
// now.
func TestAsOfSnapshotStatuses(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	th := e.thing(f, "then-working")
	e.requirement(f, th, 1)
	e.resource(f, "r", 1, false)

	p := e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{"state": f.active}, 200)
	conf := e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(p["proposal"].(map[string]any), "token"),
	}, 200)
	activeSeq := int64(conf["seq"].(float64))
	e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{"state": f.done}, 200)

	g := e.call("GET", "/api/v1/projects/"+f.project+"/graph?as_of="+itoa(activeSeq), nil, 200)
	things := g["things"].([]any)
	if len(things) != 1 || str(things[0].(map[string]any), "status") != "working" {
		t.Fatalf("as-of statuses: %v", things)
	}
	live := e.call("GET", "/api/v1/things/"+th, nil, 200)
	if str(live, "status") != "finished" {
		t.Fatalf("live status: %v", live)
	}
}
