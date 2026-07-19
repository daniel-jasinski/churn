package analytics_test

import (
	"reflect"
	"testing"

	"churn/internal/analytics"
)

// The documented criticality example graph. Dependencies point at what must
// finish first (X → Y: X depends on Y):
//
//	th_b ──────────────► th_a
//	th_c (composite) ──► th_a      (composite-ORIGINATING: inherited by c1, c2)
//	  ├─ th_c1
//	  └─ th_c2
//	th_d ──► th_c                  (composite-TARGETED: expands to c1 AND c2)
//	th_e ──► th_d
//
// Expanded leaf graph: b→a, c1→a, c2→a, d→{c1,c2}, e→d. All leaves pending.
func critGraph(t *testing.T) *ws {
	w := newWS(t)
	w.batch(thing("th_a", "A"), thing("th_b", "B"), thing("th_c", "C"),
		thing("th_d", "D"), thing("th_e", "E"))
	w.batch(childThing("th_c1", "C1", "th_c"), childThing("th_c2", "C2", "th_c"))
	w.batch(
		dep("dep_ba", "th_b", "th_a"),
		dep("dep_ca", "th_c", "th_a"),
		dep("dep_dc", "th_d", "th_c"),
		dep("dep_ed", "th_e", "th_d"))
	return w
}

// TestCriticalityGolden hand-computes all three numbers on the example
// graph, including the composite-originating and composite-targeted edges.
func TestCriticalityGolden(t *testing.T) {
	w := critGraph(t)

	want := map[string]analytics.Criticality{
		// a gates everything: b, c1, c2 unlock immediately (their only
		// dependency is a); d still needs c1+c2, e still needs d. Deepest
		// unfinished chain through a: e→d→c1→a = 4 steps.
		"th_a": {Thing: "th_a", DownstreamReach: 5, ImmediateUnlock: 3, RemainingDepth: 4},
		// b gates nothing; its chain is b→a.
		"th_b": {Thing: "th_b", DownstreamReach: 0, ImmediateUnlock: 0, RemainingDepth: 2},
		// The composite: dependents outside its subtree are d and e;
		// completing the whole subtree makes exactly d dependency-ready;
		// the deepest chain through c1 (or c2) is e→d→c1→a.
		"th_c":  {Thing: "th_c", DownstreamReach: 2, ImmediateUnlock: 1, RemainingDepth: 4},
		"th_c1": {Thing: "th_c1", DownstreamReach: 2, ImmediateUnlock: 0, RemainingDepth: 4},
		// d: only e depends on it, and e unlocks the moment d finishes.
		"th_d": {Thing: "th_d", DownstreamReach: 1, ImmediateUnlock: 1, RemainingDepth: 4},
		// e: the chain end.
		"th_e": {Thing: "th_e", DownstreamReach: 0, ImmediateUnlock: 0, RemainingDepth: 4},
	}
	for id, exp := range want {
		if got := analytics.CriticalityOf(w.p, id); got != exp {
			t.Errorf("CriticalityOf(%s) = %+v, want %+v", id, got, exp)
		}
	}
}

// TestCriticalityFinishedLeavesLeaveTheChain: finishing a shortens the
// remaining depth (chains count unfinished things only) while the
// structural downstream reach is unchanged.
func TestCriticalityFinishedLeavesLeaveTheChain(t *testing.T) {
	w := critGraph(t)
	w.batch(state("th_a", "st_done"))

	got := analytics.CriticalityOf(w.p, "th_a")
	if got.RemainingDepth != 0 {
		t.Fatalf("finished a: RemainingDepth = %d, want 0", got.RemainingDepth)
	}
	if got.DownstreamReach != 5 {
		t.Fatalf("reach is structural: %d, want 5", got.DownstreamReach)
	}
	// The chain through c1 no longer extends into a: e→d→c1 = 3.
	if got := analytics.CriticalityOf(w.p, "th_c"); got.RemainingDepth != 3 {
		t.Fatalf("after a done: depth(th_c) = %d, want 3", got.RemainingDepth)
	}
}

// TestCriticalitiesSortedAndComplete: the bulk listing covers every thing,
// sorted by id, agreeing with the single-thing computation.
func TestCriticalitiesSortedAndComplete(t *testing.T) {
	w := critGraph(t)
	all := analytics.Criticalities(w.p)
	if len(all) != len(w.p.Things) {
		t.Fatalf("got %d entries, want %d", len(all), len(w.p.Things))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Thing >= all[i].Thing {
			t.Fatalf("not sorted by id at %d: %s >= %s", i, all[i-1].Thing, all[i].Thing)
		}
	}
	for _, c := range all {
		if single := analytics.CriticalityOf(w.p, c.Thing); !reflect.DeepEqual(c, single) {
			t.Fatalf("bulk %+v != single %+v", c, single)
		}
	}
}
