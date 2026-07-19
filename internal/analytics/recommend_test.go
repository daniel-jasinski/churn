package analytics_test

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"churn/internal/analytics"
	"churn/internal/event"
)

// TestRecommendGolden scores the contended scenario with two dependents
// hung off th_r1 and hand-checks the §3.4 formula term by term.
func TestRecommendGolden(t *testing.T) {
	w := contendedWS(t)
	w.batch(thing("th_d1", "D1"), thing("th_d2", "D2"))
	w.batch(dep("dep_d1", "th_d1", "th_r1"), dep("dep_d2", "th_d2", "th_r1"))

	s := analytics.DefaultSettings()
	recs := analytics.Recommend(w.p, s)

	// Exactly the six ready things (th_pin is resource_blocked, th_d1/d2
	// blocked).
	var order []string
	for _, r := range recs {
		order = append(order, r.Thing)
	}
	want := []string{"th_r1", "th_r2", "th_r3", "th_r4", "th_r5", "th_r6"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v (r1 by score, rest by id)", order, want)
	}

	// th_r1: unlock 2, reach 2, depth 2, age 0, pressure 4/6.
	r1 := recs[0]
	pressure := 4.0 / 6.0
	wantScore := s.ImmediateUnlock*2 + s.DownstreamReach*2 + s.RemainingDepth*2 - s.ScarcityPenalty*pressure
	if math.Abs(r1.Score-wantScore) > 1e-9 {
		t.Fatalf("score(th_r1) = %v, want %v", r1.Score, wantScore)
	}
	if len(r1.Terms) != 5 {
		t.Fatalf("terms = %+v", r1.Terms)
	}
	names := []string{"immediate_unlock", "downstream_reach", "remaining_depth", "waiting_age", "resource_scarcity_penalty"}
	sum := 0.0
	for i, term := range r1.Terms {
		if term.Name != names[i] {
			t.Fatalf("term %d = %q, want %q", i, term.Name, names[i])
		}
		if term.Detail == "" {
			t.Fatalf("term %s has no explanation", term.Name)
		}
		sum += term.Contribution
	}
	if math.Abs(sum-r1.Score) > 1e-9 {
		t.Fatalf("terms sum %v != score %v", sum, r1.Score)
	}
	if pen := r1.Terms[4]; pen.Contribution >= 0 || !strings.Contains(pen.Detail, "cap_appr") {
		t.Fatalf("penalty term = %+v, want negative contribution naming cap_appr", pen)
	}

	// th_r2: no dependents — 0+0+depth(1) + 0 − penalty.
	r2 := recs[1]
	wantScore2 := s.RemainingDepth*1 - s.ScarcityPenalty*pressure
	if math.Abs(r2.Score-wantScore2) > 1e-9 {
		t.Fatalf("score(th_r2) = %v, want %v", r2.Score, wantScore2)
	}

	// Determinism: identical output on a second run.
	if again := analytics.Recommend(w.p, s); !reflect.DeepEqual(recs, again) {
		t.Fatal("two Recommend runs differ")
	}
}

// TestStarvationCreditDisclosedAfterReadyFlip is the §3.4 spec-critical
// flow at the analytics surface: a thing resource_blocked for 6 hours flips
// to ready when capacity frees — the credit survives, shows up in
// Starvations, feeds waiting_age, and is disclosed in the explanation.
func TestStarvationCreditDisclosedAfterReadyFlip(t *testing.T) {
	w := newWS(t)
	w.batch(
		c3{event.TypeResourceCreated, "rs_pool", `{"name":"Editors","kind":"reusable","capacity":1}`},
		c3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_edit"}`})
	w.batch(thing("th_hog", "Hog"), thing("th_starved", "Starved"))
	w.batch(
		c3{event.TypeRequirementAsserted, "req_h", `{"thing":"th_hog","quantity":1,"capabilities":["cap_edit"]}`},
		c3{event.TypeRequirementAsserted, "req_s", `{"thing":"th_starved","quantity":1,"capabilities":["cap_edit"]}`})

	// 11:00 — the hog takes the only unit; th_starved starts starving.
	w.at("2026-07-19T11:00:00.000Z").batch(
		state("th_hog", "st_act"),
		c3{event.TypeAllocationOpened, "al_h", `{"thing":"th_hog","resource":"rs_pool","quantity":1,"requirement":"req_h"}`})

	// 14:00 — mid-stint reading: current stint 3h, credit 3h.
	w.at("2026-07-19T14:00:00.000Z").batch(thing("th_noise1", "Noise"))
	sts := analytics.Starvations(w.p)
	if len(sts) != 1 || sts[0].Thing != "th_starved" ||
		sts[0].CurrentStint != 3*time.Hour || sts[0].Credit != 3*time.Hour {
		t.Fatalf("mid-stint starvations = %+v", sts)
	}

	// 17:00 — capacity frees; th_starved flips to ready with 6h credit.
	w.at("2026-07-19T17:00:00.000Z").batch(
		state("th_hog", "st_done"),
		c3{event.TypeAllocationClosed, "al_h", `{}`})
	if got := w.p.Derive("th_starved").Status; got != "ready" {
		t.Fatalf("status = %q, want ready", got)
	}
	sts = analytics.Starvations(w.p)
	if len(sts) != 1 || sts[0].CurrentStint != 0 || sts[0].Credit != 6*time.Hour {
		t.Fatalf("post-flip starvations = %+v, want credit 6h, stint 0", sts)
	}

	// The recommendation reads and DISCLOSES the credit. Ready: th_starved
	// and th_noise1 (th_hog is finished).
	recs := analytics.Recommend(w.p, analytics.DefaultSettings())
	if len(recs) != 2 {
		t.Fatalf("recs = %+v, want th_starved and th_noise1", recs)
	}
	var starved *analytics.Recommendation
	for i := range recs {
		if recs[i].Thing == "th_starved" {
			starved = &recs[i]
		}
	}
	if starved == nil {
		t.Fatalf("th_starved missing from recommendations: %+v", recs)
	}
	age := starved.Terms[3]
	if age.Name != "waiting_age" || math.Abs(age.Value-0.25) > 1e-9 { // 6h = 0.25d
		t.Fatalf("waiting_age term = %+v, want value 0.25 days", age)
	}
	if !strings.Contains(age.Detail, "waited 0d6h") {
		t.Fatalf("explanation must disclose the wait: %q", age.Detail)
	}
	// And it outranks an otherwise-identical thing with no credit.
	var starvedIdx, noiseIdx int
	for i, r := range recs {
		switch r.Thing {
		case "th_starved":
			starvedIdx = i
		case "th_noise1":
			noiseIdx = i
		}
	}
	if starvedIdx > noiseIdx {
		t.Fatalf("starved thing must outrank the fresh one: %+v", recs)
	}
}

// TestStarvationsExcludeTerminalThings: a cancelled (or finished) thing
// keeps its bookkeeping credit — spec-pinned semantics — but the listing
// must not show it forever: its credit can never matter again.
func TestStarvationsExcludeTerminalThings(t *testing.T) {
	w := newWS(t)
	w.batch(
		c3{event.TypeResourceCreated, "rs_pool", `{"name":"Editors","kind":"reusable","capacity":1}`},
		c3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_edit"}`})
	w.batch(thing("th_hog", "Hog"), thing("th_starved", "Starved"))
	w.batch(
		c3{event.TypeRequirementAsserted, "req_h", `{"thing":"th_hog","quantity":1,"capabilities":["cap_edit"]}`},
		c3{event.TypeRequirementAsserted, "req_s", `{"thing":"th_starved","quantity":1,"capabilities":["cap_edit"]}`})
	w.at("2026-07-19T11:00:00.000Z").batch(
		state("th_hog", "st_act"),
		c3{event.TypeAllocationOpened, "al_h", `{"thing":"th_hog","resource":"rs_pool","quantity":1,"requirement":"req_h"}`})
	// 4h of stint, then the starving thing is cancelled.
	w.at("2026-07-19T15:00:00.000Z").batch(state("th_starved", "st_cancel"))

	if rec := w.p.Statuses["th_starved"]; rec.BlockedFor != 4*time.Hour {
		t.Fatalf("bookkeeping must keep the credit: %+v", rec)
	}
	if got := analytics.Starvations(w.p); len(got) != 0 {
		t.Fatalf("starvations = %+v, want none (dropped thing filtered)", got)
	}
	// A reopen (cancelled → pending) brings the credit back into view.
	w.at("2026-07-19T16:00:00.000Z").batch(state("th_starved", "st_todo"))
	got := analytics.Starvations(w.p)
	if len(got) != 1 || got[0].Thing != "th_starved" || got[0].Credit != 4*time.Hour {
		t.Fatalf("after reopen = %+v, want the 4h credit visible again", got)
	}
}

// TestReadyListFilters: the §3.1 filters — project, type, subtree,
// capability — and the score-ordered listing.
func TestReadyListFilters(t *testing.T) {
	w := newWS(t)
	w.batch(
		c3{event.TypeResourceCreated, "rs_pool", `{"name":"Editors","kind":"reusable","capacity":4}`},
		c3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_edit"}`})
	w.batch(
		thing("th_a", "A"),
		c3{event.TypeThingCreated, "th_beta", `{"name":"Beta thing","project":"pr_2","type":"ty_task"}`},
		c3{event.TypeThingCreated, "th_rev", `{"name":"Review","project":"pr_1","type":"ty_review"}`},
		thing("th_ws", "Workstream"))
	w.batch(childThing("th_kid", "Kid", "th_ws"))
	w.batch(c3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})

	s := analytics.DefaultSettings()
	all := analytics.Ready(w.p, s, analytics.ReadyFilter{})
	if got := entryIDs(all); !reflect.DeepEqual(got, []string{"th_a", "th_beta", "th_kid", "th_rev"}) {
		t.Fatalf("unfiltered ready = %v", got)
	}
	if got := entryIDs(analytics.Ready(w.p, s, analytics.ReadyFilter{Project: "pr_2"})); !reflect.DeepEqual(got, []string{"th_beta"}) {
		t.Fatalf("project filter = %v", got)
	}
	if got := entryIDs(analytics.Ready(w.p, s, analytics.ReadyFilter{Type: "ty_review"})); !reflect.DeepEqual(got, []string{"th_rev"}) {
		t.Fatalf("type filter = %v", got)
	}
	if got := entryIDs(analytics.Ready(w.p, s, analytics.ReadyFilter{Subtree: "th_ws"})); !reflect.DeepEqual(got, []string{"th_kid"}) {
		t.Fatalf("subtree filter = %v", got)
	}
	if got := entryIDs(analytics.Ready(w.p, s, analytics.ReadyFilter{Capability: "cap_edit"})); !reflect.DeepEqual(got, []string{"th_a"}) {
		t.Fatalf("capability filter = %v", got)
	}
	// Entries carry their requirements.
	if len(all[0].Requirements) != 1 || all[0].Requirements[0].ID != "req_a" {
		t.Fatalf("th_a entry requirements = %+v", all[0].Requirements)
	}
}

func entryIDs(es []analytics.ReadyEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Thing)
	}
	return out
}
