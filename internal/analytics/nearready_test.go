package analytics_test

import (
	"reflect"
	"testing"

	"churn/internal/analytics"
	"churn/internal/domain"
)

// near is NearReady with the default cutoff and no filter.
func near(p *domain.Projection) []analytics.NearReadyEntry {
	return analytics.NearReady(p, analytics.ReadyFilter{}, 0)
}

// frontierOf returns the entry for thing, or nil if absent.
func frontierOf(entries []analytics.NearReadyEntry, thing string) *analytics.NearReadyEntry {
	for i := range entries {
		if entries[i].Thing == thing {
			return &entries[i]
		}
	}
	return nil
}

// TestNearReadyChain is the golden chain A→B→C→D (D depends on C, C on B,
// B on A): the frontier is always the NEAREST unfinished blocker — B waits
// on A, C on B, D on C — never the whole upstream chain.
func TestNearReadyChain(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_a", "A"), thing("th_b", "B"), thing("th_c", "C"), thing("th_d", "D"))
	w.batch(dep("dep_ba", "th_b", "th_a"), dep("dep_cb", "th_c", "th_b"), dep("dep_dc", "th_d", "th_c"))

	got := near(w.p)
	want := []analytics.NearReadyEntry{
		{Thing: "th_b", Project: "pr_1", Type: "ty_task",
			Frontier: []analytics.NearBlocker{{Thing: "th_a", Status: domain.StatusReady}}, Count: 1},
		{Thing: "th_c", Project: "pr_1", Type: "ty_task",
			Frontier: []analytics.NearBlocker{{Thing: "th_b", Status: domain.StatusBlocked}}, Count: 1},
		{Thing: "th_d", Project: "pr_1", Type: "ty_task",
			Frontier: []analytics.NearBlocker{{Thing: "th_c", Status: domain.StatusBlocked}}, Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("near ready = %+v, want %+v", got, want)
	}

	// A finished: B leaves the list (ready now), C's frontier member B reads
	// ready — one start away, no special casing.
	w.batch(state("th_a", "st_done"))
	got = near(w.p)
	if frontierOf(got, "th_b") != nil {
		t.Fatalf("th_b still near-ready after its blocker finished: %+v", got)
	}
	if en := frontierOf(got, "th_c"); en == nil ||
		!reflect.DeepEqual(en.Frontier, []analytics.NearBlocker{{Thing: "th_b", Status: domain.StatusReady}}) {
		t.Fatalf("th_c entry = %+v, want frontier [th_b ready]", en)
	}
}

// TestNearReadyDiamondReduction: T declares edges to both A and B while B
// also depends on A — transitive reduction collapses the frontier to the
// nearest blocker B alone, so T counts as ONE blocker away.
func TestNearReadyDiamondReduction(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_t", "T"), thing("th_a", "A"), thing("th_b", "B"))
	w.batch(dep("dep_ta", "th_t", "th_a"), dep("dep_tb", "th_t", "th_b"), dep("dep_ba", "th_b", "th_a"))

	en := frontierOf(near(w.p), "th_t")
	if en == nil || en.Count != 1 ||
		!reflect.DeepEqual(en.Frontier, []analytics.NearBlocker{{Thing: "th_b", Status: domain.StatusBlocked}}) {
		t.Fatalf("diamond entry = %+v, want frontier [th_b] only", en)
	}
}

// TestNearReadyCompositeBlocker: an edge pointing at a composite reports the
// DECLARED composite as the one blocker, with its §2.1 rollup status — not
// its N unfinished leaves.
func TestNearReadyCompositeBlocker(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_t", "T"), thing("th_ws", "Workstream"))
	w.batch(childThing("th_s1", "S1", "th_ws"), childThing("th_s2", "S2", "th_ws"))
	w.batch(dep("dep_t", "th_t", "th_ws"))

	en := frontierOf(near(w.p), "th_t")
	if en == nil || en.Count != 1 ||
		!reflect.DeepEqual(en.Frontier, []analytics.NearBlocker{{Thing: "th_ws", Status: domain.StatusPending}}) {
		t.Fatalf("composite entry = %+v, want frontier [th_ws pending]", en)
	}

	// One leaf active: the declared blocker's rollup follows (§2.1).
	w.batch(state("th_s1", "st_act"))
	if en := frontierOf(near(w.p), "th_t"); en == nil || en.Frontier[0].Status != domain.StatusWorking {
		t.Fatalf("entry after child start = %+v, want rollup working", en)
	}
}

// TestNearReadyMaxBlockers: the cutoff is a frontier-size boundary — exactly
// maxBlockers is in, one more is out; 0 means the default of 2. Output order
// is frontier size ascending, then thing id.
func TestNearReadyMaxBlockers(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_x", "X"), thing("th_y", "Y"), thing("th_z", "Z"),
		thing("th_two", "Two"), thing("th_three", "Three"), thing("th_one", "One"))
	w.batch(
		dep("dep_1x", "th_one", "th_x"),
		dep("dep_2x", "th_two", "th_x"), dep("dep_2y", "th_two", "th_y"),
		dep("dep_3x", "th_three", "th_x"), dep("dep_3y", "th_three", "th_y"), dep("dep_3z", "th_three", "th_z"),
	)

	got := near(w.p) // default cutoff 2
	if len(got) != 2 || got[0].Thing != "th_one" || got[0].Count != 1 ||
		got[1].Thing != "th_two" || got[1].Count != 2 {
		t.Fatalf("default cutoff: %+v, want [th_one(1) th_two(2)]", got)
	}
	if frontierOf(got, "th_three") != nil {
		t.Fatal("th_three (3 blockers) must be beyond the default cutoff")
	}

	got = analytics.NearReady(w.p, analytics.ReadyFilter{}, 3)
	if len(got) != 3 || got[2].Thing != "th_three" || got[2].Count != 3 {
		t.Fatalf("cutoff 3: %+v, want th_three included last", got)
	}
	if got[0].Count > got[1].Count || got[1].Count > got[2].Count {
		t.Fatalf("order not frontier-size ascending: %+v", got)
	}

	if got := analytics.NearReady(w.p, analytics.ReadyFilter{}, 1); len(got) != 1 || got[0].Thing != "th_one" {
		t.Fatalf("cutoff 1: %+v, want [th_one]", got)
	}
}

// TestNearReadyResourceBlockedFrontier: a thing blocked ONLY by a
// resource-blocked leaf is listed, and the frontier status makes the reason
// visible — while the resource-blocked leaf itself is NOT near-ready (that
// is the ready board's resource_blocked column already).
func TestNearReadyResourceBlockedFrontier(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_a", "A"), thing("th_b", "B"))
	w.batch(dep("dep_ba", "th_b", "th_a"))
	// A needs editing capacity; no resource carries it → resource_blocked.
	w.batch(c3{"requirement.asserted", "req_a", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})

	if st := w.p.Derive("th_a").Status; st != domain.StatusResourceBlocked {
		t.Fatalf("th_a = %v, want resource_blocked", st)
	}
	got := near(w.p)
	if frontierOf(got, "th_a") != nil {
		t.Fatal("resource_blocked leaf must not be near-ready itself")
	}
	if en := frontierOf(got, "th_b"); en == nil ||
		!reflect.DeepEqual(en.Frontier, []analytics.NearBlocker{{Thing: "th_a", Status: domain.StatusResourceBlocked}}) {
		t.Fatalf("th_b entry = %+v, want frontier [th_a resource_blocked]", en)
	}
}

// TestNearReadyFilters: the §3.1 filters apply with ready-list semantics —
// project and type match the blocked thing, subtree keeps its containment
// subtree, capability matches the blocked thing's OWN requirements (never
// its blockers').
func TestNearReadyFilters(t *testing.T) {
	w := newWS(t)
	w.batch(
		thing("th_a", "A"),
		thing("th_b", "B"), // blocked, ty_task, requires cap_appr
		c3{"thing.created", "th_r", `{"name":"R","project":"pr_1","type":"ty_review"}`},
		c3{"thing.created", "th_p2", `{"name":"P2","project":"pr_2","type":"ty_task"}`},
		thing("th_ws", "WS"),
	)
	w.batch(childThing("th_in", "In", "th_ws"))
	w.batch(
		dep("dep_ba", "th_b", "th_a"),
		dep("dep_ra", "th_r", "th_a"),
		dep("dep_p2", "th_p2", "th_a"),
		dep("dep_in", "th_in", "th_a"),
		c3{"requirement.asserted", "req_b", `{"thing":"th_b","quantity":1,"capabilities":["cap_appr"]}`},
	)

	all := near(w.p)
	if len(all) != 4 {
		t.Fatalf("unfiltered = %+v, want 4 entries", all)
	}
	ids := func(es []analytics.NearReadyEntry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.Thing
		}
		return out
	}
	if got := analytics.NearReady(w.p, analytics.ReadyFilter{Project: "pr_2"}, 0); !reflect.DeepEqual(ids(got), []string{"th_p2"}) {
		t.Fatalf("project filter = %v, want [th_p2]", ids(got))
	}
	if got := analytics.NearReady(w.p, analytics.ReadyFilter{Type: "ty_review"}, 0); !reflect.DeepEqual(ids(got), []string{"th_r"}) {
		t.Fatalf("type filter = %v, want [th_r]", ids(got))
	}
	if got := analytics.NearReady(w.p, analytics.ReadyFilter{Subtree: "th_ws"}, 0); !reflect.DeepEqual(ids(got), []string{"th_in"}) {
		t.Fatalf("subtree filter = %v, want [th_in]", ids(got))
	}
	if got := analytics.NearReady(w.p, analytics.ReadyFilter{Capability: "cap_appr"}, 0); !reflect.DeepEqual(ids(got), []string{"th_b"}) {
		t.Fatalf("capability filter = %v, want [th_b]", ids(got))
	}
	if got := analytics.NearReady(w.p, analytics.ReadyFilter{Subtree: "th_unknown"}, 0); got != nil {
		t.Fatalf("unknown subtree = %v, want nil (filters everything out)", got)
	}
}

// TestNearReadySubsumedDeclaredTarget: T depends on composite WS AND on S1,
// a leaf inside WS. S1's edge is subsumed by the composite edge — the
// declared frontier is [WS] alone (count 1), never [S1 WS] with S1 double-
// counted against the cutoff.
func TestNearReadySubsumedDeclaredTarget(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_t", "T"), thing("th_ws", "Workstream"))
	w.batch(childThing("th_s1", "S1", "th_ws"), childThing("th_s2", "S2", "th_ws"))
	w.batch(dep("dep_tws", "th_t", "th_ws"), dep("dep_ts1", "th_t", "th_s1"))

	en := frontierOf(near(w.p), "th_t")
	if en == nil || en.Count != 1 ||
		!reflect.DeepEqual(en.Frontier, []analytics.NearBlocker{{Thing: "th_ws", Status: domain.StatusPending}}) {
		t.Fatalf("subsumed entry = %+v, want frontier [th_ws] count 1", en)
	}

	// Blockers in DIFFERENT composites never subsume each other: T2 → WS and
	// T2 → X (a top-level leaf) stays a two-member frontier.
	w.batch(thing("th_t2", "T2"), thing("th_x", "X"))
	w.batch(dep("dep_t2ws", "th_t2", "th_ws"), dep("dep_t2x", "th_t2", "th_x"))
	if en := frontierOf(near(w.p), "th_t2"); en == nil || en.Count != 2 {
		t.Fatalf("cross-target entry = %+v, want count 2 (no false subsumption)", en)
	}
}
