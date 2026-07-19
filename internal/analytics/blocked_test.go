package analytics_test

import (
	"reflect"
	"testing"

	"churn/internal/analytics"
)

// TestBlockedByFrontierMinimal: t depends on both b and c while b also
// depends on c — the frontier shows only the NEAREST blocker b (c is
// reachable from b and thus transitively reduced away), with c expandable
// through the chains.
func TestBlockedByFrontierMinimal(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_t", "T"), thing("th_b", "B"), thing("th_c", "C"))
	w.batch(dep("dep_tb", "th_t", "th_b"), dep("dep_tc", "th_t", "th_c"), dep("dep_bc", "th_b", "th_c"))

	got := analytics.BlockedBy(w.p, "th_t")
	if !reflect.DeepEqual(got.Frontier, []string{"th_b"}) {
		t.Fatalf("frontier = %v, want [th_b]", got.Frontier)
	}
	if !reflect.DeepEqual(got.Chains["th_b"], []string{"th_c"}) {
		t.Fatalf("chains[th_b] = %v, want [th_c]", got.Chains["th_b"])
	}
	if len(got.Chains["th_c"]) != 0 {
		t.Fatalf("chains[th_c] = %v, want chain end", got.Chains["th_c"])
	}

	// The minimality property itself: no frontier member reachable from
	// another member through the blocker graph.
	assertFrontierMinimal(t, got)

	// Finishing b moves the frontier to c (nearest unfinished).
	w.batch(state("th_b", "st_done"))
	got = analytics.BlockedBy(w.p, "th_t")
	if !reflect.DeepEqual(got.Frontier, []string{"th_c"}) {
		t.Fatalf("after b done: frontier = %v, want [th_c]", got.Frontier)
	}
	// Finishing c clears it.
	w.batch(state("th_c", "st_done"))
	if got = analytics.BlockedBy(w.p, "th_t"); len(got.Frontier) != 0 {
		t.Fatalf("all satisfied: frontier = %v, want empty", got.Frontier)
	}
}

func assertFrontierMinimal(t *testing.T, b analytics.Blocked) {
	t.Helper()
	reach := func(from, to string) bool {
		seen := map[string]bool{}
		var dfs func(n string) bool
		dfs = func(n string) bool {
			if n == to {
				return true
			}
			if seen[n] {
				return false
			}
			seen[n] = true
			for _, m := range b.Chains[n] {
				if dfs(m) {
					return true
				}
			}
			return false
		}
		for _, m := range b.Chains[from] {
			if dfs(m) {
				return true
			}
		}
		return false
	}
	for _, u := range b.Frontier {
		for _, v := range b.Frontier {
			if u != v && reach(v, u) {
				t.Fatalf("frontier not minimal: %s reachable from %s", u, v)
			}
		}
	}
}

// TestBlockedByCompositeTarget: an edge at a composite blocks on exactly the
// unfinished subtree leaves; asking about the composite itself ignores
// intra-subtree ordering (that is progress, not blockage).
func TestBlockedByCompositeTarget(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_t", "T"), thing("th_ws", "Workstream"))
	w.batch(childThing("th_s1", "S1", "th_ws"), childThing("th_s2", "S2", "th_ws"))
	w.batch(dep("dep_t", "th_t", "th_ws"))
	w.batch(state("th_s1", "st_done"))

	got := analytics.BlockedBy(w.p, "th_t")
	if !reflect.DeepEqual(got.Frontier, []string{"th_s2"}) {
		t.Fatalf("frontier = %v, want the unfinished leaf [th_s2]", got.Frontier)
	}

	// The composite's own blocked-by: s2 depends on s1 internally, an
	// external leaf th_x blocks s1 — only th_x is the composite's blocker.
	w.batch(thing("th_x", "External"))
	w.batch(dep("dep_int", "th_s2", "th_s1"), dep("dep_ext", "th_s1", "th_x"))
	got = analytics.BlockedBy(w.p, "th_ws")
	if !reflect.DeepEqual(got.Frontier, []string{"th_x"}) {
		t.Fatalf("composite frontier = %v, want [th_x] only (no internal leaves)", got.Frontier)
	}
}

// TestBlockedByAbandonedPolicy: an abandoned leaf blocks forever under a
// block-policy edge — it belongs on the frontier; under ignore it is no
// blocker at all.
func TestBlockedByAbandonedPolicy(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_t", "T"), thing("th_u", "U"), thing("th_gone", "Gone"))
	w.batch(
		c3{typ: "dependency.asserted", entity: "dep_b", data: `{"from":"th_t","to":"th_gone","on_abandoned":"block"}`},
		c3{typ: "dependency.asserted", entity: "dep_i", data: `{"from":"th_u","to":"th_gone","on_abandoned":"ignore"}`})
	w.batch(state("th_gone", "st_cancel"))

	if got := analytics.BlockedBy(w.p, "th_t"); !reflect.DeepEqual(got.Frontier, []string{"th_gone"}) {
		t.Fatalf("block policy: frontier = %v, want [th_gone]", got.Frontier)
	}
	if got := analytics.BlockedBy(w.p, "th_u"); len(got.Frontier) != 0 {
		t.Fatalf("ignore policy: frontier = %v, want empty", got.Frontier)
	}
}

// TestUnlocks: the inverse view — things made dependency-ready by a
// simulated completion, and nothing else.
func TestUnlocks(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_x", "X"), thing("th_y", "Y"), thing("th_z", "Z"), thing("th_w", "W"))
	// y depends only on x; z depends on x AND w; w is independent.
	w.batch(dep("dep_yx", "th_y", "th_x"), dep("dep_zx", "th_z", "th_x"), dep("dep_zw", "th_z", "th_w"))

	if got := analytics.Unlocks(w.p, "th_x"); !reflect.DeepEqual(got, []string{"th_y"}) {
		t.Fatalf("Unlocks(x) = %v, want [th_y] (z still needs w)", got)
	}
	// Once w is done, completing x would unlock z too.
	w.batch(state("th_w", "st_done"))
	if got := analytics.Unlocks(w.p, "th_x"); !reflect.DeepEqual(got, []string{"th_y", "th_z"}) {
		t.Fatalf("Unlocks(x) = %v, want [th_y th_z]", got)
	}
	// Already dependency-ready things are not "unlocked" again, and
	// non-pending dependents never count.
	w.batch(state("th_x", "st_done"))
	if got := analytics.Unlocks(w.p, "th_x"); len(got) != 0 {
		t.Fatalf("Unlocks of a finished thing = %v, want empty (dependents already ready)", got)
	}
}

// TestUnlocksRespectsAbandonedPolicy is the reviewer's probe case: WS is a
// composite {S1 pending, S2 abandoned}; D depends on WS with
// on_abandoned=block, I with ignore. Simulated completion finishes S1 but
// S2 STAYS abandoned — so D can never become dependency-ready and must not
// be counted, while I unlocks (and, once real, would carry the warning
// badge).
func TestUnlocksRespectsAbandonedPolicy(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_ws", "WS"), thing("th_d", "D"), thing("th_i", "I"))
	w.batch(childThing("th_s1", "S1", "th_ws"), childThing("th_s2", "S2", "th_ws"))
	w.batch(
		c3{typ: "dependency.asserted", entity: "dep_d", data: `{"from":"th_d","to":"th_ws","on_abandoned":"block"}`},
		c3{typ: "dependency.asserted", entity: "dep_i", data: `{"from":"th_i","to":"th_ws","on_abandoned":"ignore"}`})
	w.batch(state("th_s2", "st_cancel"))

	if got := analytics.Unlocks(w.p, "th_ws"); !reflect.DeepEqual(got, []string{"th_i"}) {
		t.Fatalf("Unlocks(WS) = %v, want [th_i] only — th_d is behind a block edge forever", got)
	}
	if got := analytics.CriticalityOf(w.p, "th_ws").ImmediateUnlock; got != 1 {
		t.Fatalf("ImmediateUnlock(WS) = %d, want 1", got)
	}
	// Reality check: actually finishing S1 must agree with the simulation.
	w.batch(state("th_s1", "st_done"))
	if got := w.p.Derive("th_d"); got.Status != "blocked" {
		t.Fatalf("after real completion th_d = %+v, want blocked", got)
	}
	if got := w.p.Derive("th_i"); got.Status != "ready" || !got.Badges.AbandonedDependency {
		t.Fatalf("after real completion th_i = %+v, want ready + badge", got)
	}
}

// TestUnlocksComposite: completing a composite means all its leaves —
// dependents on the composite become dependency-ready.
func TestUnlocksComposite(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_ws", "Workstream"), thing("th_d", "Dependent"))
	w.batch(childThing("th_s1", "S1", "th_ws"), childThing("th_s2", "S2", "th_ws"))
	w.batch(dep("dep_d", "th_d", "th_ws"))

	if got := analytics.Unlocks(w.p, "th_ws"); !reflect.DeepEqual(got, []string{"th_d"}) {
		t.Fatalf("Unlocks(composite) = %v, want [th_d]", got)
	}
	// Completing only one child does not unlock the dependent.
	if got := analytics.Unlocks(w.p, "th_s1"); len(got) != 0 {
		t.Fatalf("Unlocks(one child) = %v, want empty", got)
	}
}
