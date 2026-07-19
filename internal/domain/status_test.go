package domain

import (
	"reflect"
	"testing"
	"time"

	"churn/internal/event"
)

// statusWS builds a workspace with the shared fixtures the status tests use:
// baseline vocabulary (newWS) plus a second capability cap_appr and an
// editors pool rs_pool (capacity 2, cap_edit).
func statusWS(t *testing.T) *tb {
	b := newWS(t)
	b.must(cmd3{event.TypeCapabilityDefined, "cap_appr", `{"name":"approval"}`})
	b.must(cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Editors","kind":"reusable","capacity":2}`},
		cmd3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_edit"}`})
	return b
}

func derive(t *testing.T, b *tb, id string) Derived {
	t.Helper()
	return b.p.Derive(id)
}

// TestStatusTable exercises every row of the §2.2 table on leaves.
func TestStatusTable(t *testing.T) {
	b := statusWS(t)
	b.must(thing("th_dep", "Dependency"), thing("th_a", "A"))
	b.must(cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_a","to":"th_dep"}`})

	// blocked: some dependency not satisfied.
	if got := derive(t, b, "th_a").Status; got != StatusBlocked {
		t.Fatalf("unsatisfied dep: status = %q, want blocked", got)
	}
	// ready: deps satisfied ∧ requirements satisfiable (here: none).
	b.must(cmd3{event.TypeThingStateChanged, "th_dep", `{"state":"st_done"}`})
	if got := derive(t, b, "th_a").Status; got != StatusReady {
		t.Fatalf("satisfied dep, no requirements: status = %q, want ready", got)
	}
	// resource_blocked: deps satisfied ∧ some requirement not satisfiable.
	b.must(cmd3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":1,"capabilities":["cap_appr"]}`})
	if got := derive(t, b, "th_a").Status; got != StatusResourceBlocked {
		t.Fatalf("no approval capacity: status = %q, want resource_blocked", got)
	}
	// ...until a resource carries the capability.
	b.must(cmd3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_appr"}`})
	if got := derive(t, b, "th_a").Status; got != StatusReady {
		t.Fatalf("approval granted: status = %q, want ready", got)
	}

	// The state-semantic mirrors.
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_a"}`})
	if got := derive(t, b, "th_a").Status; got != StatusWorking {
		t.Fatalf("active: status = %q, want working", got)
	}
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_hold"}`},
		cmd3{event.TypeAllocationClosed, "al_1", `{}`})
	if got := derive(t, b, "th_a"); got.Status != StatusHeld || !got.ResumableNow {
		t.Fatalf("paused: %+v, want held + resumable-now", got)
	}
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_done"}`})
	if got := derive(t, b, "th_a").Status; got != StatusFinished {
		t.Fatalf("satisfied: status = %q, want finished", got)
	}
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_cancel"}`})
	if got := derive(t, b, "th_a"); got.Status != StatusDropped || !got.HasAbandoned {
		t.Fatalf("abandoned: %+v, want dropped + has_abandoned", got)
	}
}

// TestStatusPrecedence: the state-semantic mirrors win over
// blocked/ready/resource_blocked — a satisfied thing that gains an
// unsatisfied dependency reads finished with the consistency warning, never
// blocked (§2.2).
func TestStatusPrecedence(t *testing.T) {
	b := statusWS(t)
	b.must(thing("th_done", "Done"), thing("th_late", "Late dependency"))
	b.must(cmd3{event.TypeThingStateChanged, "th_done", `{"state":"st_done"}`})
	b.must(cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_done","to":"th_late"}`})

	got := derive(t, b, "th_done")
	if got.Status != StatusFinished {
		t.Fatalf("status = %q, want finished (mirrors win)", got.Status)
	}
	if !got.Badges.FinishedUnsatisfiedDeps {
		t.Fatal("want the finished-with-unsatisfied-dependency consistency warning")
	}
	// A working thing with an unsatisfied dep reads working, no such badge.
	b.must(thing("th_w", "W"))
	b.must(cmd3{event.TypeDependencyAsserted, "dep_2", `{"from":"th_w","to":"th_late"}`})
	b.must(cmd3{event.TypeThingStateChanged, "th_w", `{"state":"st_act"}`})
	if got := derive(t, b, "th_w"); got.Status != StatusWorking || got.Badges.FinishedUnsatisfiedDeps {
		t.Fatalf("working precedence: %+v", got)
	}
}

// TestResumableNow: held things carry the indicator, computed by the same
// feasibility routine as ready — it flips off when the capacity is taken.
func TestResumableNow(t *testing.T) {
	b := statusWS(t)
	b.must(thing("th_a", "A"), thing("th_b", "B"))
	b.must(
		cmd3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":2,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeRequirementAsserted, "req_b", `{"thing":"th_b","quantity":1,"capabilities":["cap_edit"]}`})
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_hold"}`})

	if got := derive(t, b, "th_a"); got.Status != StatusHeld || !got.ResumableNow {
		t.Fatalf("free pool: %+v, want held + resumable", got)
	}
	// th_b takes one of the two units: th_a's 2×cap_edit no longer fits.
	b.must(
		cmd3{event.TypeThingStateChanged, "th_b", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_b", `{"thing":"th_b","resource":"rs_pool","quantity":1,"requirement":"req_b"}`})
	if got := derive(t, b, "th_a"); got.Status != StatusHeld || got.ResumableNow {
		t.Fatalf("pool contended: %+v, want held + NOT resumable", got)
	}
	// Non-held things never carry the indicator.
	if got := derive(t, b, "th_b"); got.ResumableNow {
		t.Fatal("working thing must not carry resumable-now")
	}
}

// TestReadyIsAnAssignmentNotACount pins §2.4: one dual-capability unit
// cannot serve two requirements at once — the greedy per-requirement count
// would call the thing ready; the matching must not.
func TestReadyIsAnAssignmentNotACount(t *testing.T) {
	b := statusWS(t)
	b.must(cmd3{event.TypeResourceCreated, "rs_dual", `{"name":"Dual","kind":"reusable","capacity":1}`},
		cmd3{event.TypeCapabilityGranted, "rs_dual", `{"capability":"cap_edit"}`},
		cmd3{event.TypeCapabilityGranted, "rs_dual", `{"capability":"cap_appr"}`},
		cmd3{event.TypeResourceAvailabilityChanged, "rs_pool", `{"available":false,"note":"down"}`})
	b.must(thing("th_a", "A"))
	b.must(
		cmd3{event.TypeRequirementAsserted, "req_e", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeRequirementAsserted, "req_p", `{"thing":"th_a","quantity":1,"capabilities":["cap_appr"]}`})

	// Only rs_dual is up: each requirement is individually satisfiable, the
	// pair is not.
	if got := derive(t, b, "th_a").Status; got != StatusResourceBlocked {
		t.Fatalf("status = %q, want resource_blocked (assignment, not count)", got)
	}
	if _, ok := b.p.ProposeAllocations("th_a"); ok {
		t.Fatal("proposer must refuse an infeasible assignment")
	}
	// The pool coming back adds a cap_edit unit: now a distinct-units
	// assignment exists, and the proposer routes around the dual unit.
	b.must(cmd3{event.TypeResourceAvailabilityChanged, "rs_pool", `{"available":true}`})
	if got := derive(t, b, "th_a").Status; got != StatusReady {
		t.Fatalf("status = %q, want ready", got)
	}
	proposal, ok := b.p.ProposeAllocations("th_a")
	if !ok {
		t.Fatal("proposer must find the assignment")
	}
	want := []ProposedAllocation{
		{Requirement: "req_e", Resource: "rs_pool", Quantity: 1},
		{Requirement: "req_p", Resource: "rs_dual", Quantity: 1},
	}
	if !reflect.DeepEqual(proposal, want) {
		t.Fatalf("proposal = %+v, want %+v", proposal, want)
	}
	// The proposal is exactly what a transition batch commits.
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_e"}`},
		cmd3{event.TypeAllocationOpened, "al_2", `{"thing":"th_a","resource":"rs_dual","quantity":1,"requirement":"req_p"}`})
}

// TestAbandonedDependencyBadge: policy ignore unblocks with the warning
// badge; policy block keeps the dependent blocked (§2.1/§2.2).
func TestAbandonedDependencyBadge(t *testing.T) {
	b := statusWS(t)
	b.must(thing("th_gone", "Cancelled"), thing("th_ign", "Ignores"), thing("th_blk", "Blocks"))
	b.must(cmd3{event.TypeThingStateChanged, "th_gone", `{"state":"st_cancel"}`})
	b.must(
		cmd3{event.TypeDependencyAsserted, "dep_i", `{"from":"th_ign","to":"th_gone","on_abandoned":"ignore"}`},
		cmd3{event.TypeDependencyAsserted, "dep_b", `{"from":"th_blk","to":"th_gone","on_abandoned":"block"}`})

	if got := derive(t, b, "th_ign"); got.Status != StatusReady || !got.Badges.AbandonedDependency {
		t.Fatalf("ignore policy: %+v, want ready + abandoned-dependency badge", got)
	}
	if got := derive(t, b, "th_blk"); got.Status != StatusBlocked || got.Badges.AbandonedDependency {
		t.Fatalf("block policy: %+v, want blocked, no badge", got)
	}
}

// TestCompositeTargetedEdgePolicy: an edge at a composite applies ITS OWN
// policy to the subtree's has_abandoned flag, and abandonment never
// accelerates satisfaction (§2.1).
func TestCompositeTargetedEdgePolicy(t *testing.T) {
	b := statusWS(t)
	b.must(thing("th_c", "Composite"))
	b.must(childThing("th_c1", "Step 1", "th_c"), childThing("th_c2", "Step 2", "th_c"))
	b.must(thing("th_ign", "Ignores"), thing("th_blk", "Blocks"))
	b.must(
		cmd3{event.TypeDependencyAsserted, "dep_i", `{"from":"th_ign","to":"th_c","on_abandoned":"ignore"}`},
		cmd3{event.TypeDependencyAsserted, "dep_b", `{"from":"th_blk","to":"th_c","on_abandoned":"block"}`})

	// One leaf abandoned, one still pending: satisfies NO ONE.
	b.must(cmd3{event.TypeThingStateChanged, "th_c1", `{"state":"st_cancel"}`})
	if got := derive(t, b, "th_ign").Status; got != StatusBlocked {
		t.Fatalf("pending leaf remains: %q, want blocked (abandonment never accelerates)", got)
	}
	// All leaves terminal: ignore-edge satisfied with badge, block-edge not.
	b.must(cmd3{event.TypeThingStateChanged, "th_c2", `{"state":"st_done"}`})
	if got := derive(t, b, "th_ign"); got.Status != StatusReady || !got.Badges.AbandonedDependency {
		t.Fatalf("ignore edge: %+v, want ready + badge", got)
	}
	if got := derive(t, b, "th_blk").Status; got != StatusBlocked {
		t.Fatalf("block edge: %q, want blocked while a subtree leaf is abandoned", got)
	}
	// The composite itself rolls up finished + has_abandoned (§2.1 table).
	if got := derive(t, b, "th_c"); got.Status != StatusFinished || !got.HasAbandoned {
		t.Fatalf("rollup: %+v, want finished + has_abandoned", got)
	}
}

// TestCompositeRollupTable exercises the four §2.1 rollup rows.
func TestCompositeRollupTable(t *testing.T) {
	b := statusWS(t)
	b.must(thing("th_c", "Composite"))
	b.must(childThing("th_1", "S1", "th_c"), childThing("th_2", "S2", "th_c"))

	if got := derive(t, b, "th_c").Status; got != StatusPending {
		t.Fatalf("fresh children: %q, want pending", got)
	}
	b.must(cmd3{event.TypeThingStateChanged, "th_1", `{"state":"st_act"}`})
	if got := derive(t, b, "th_c").Status; got != StatusWorking {
		t.Fatalf("one active: %q, want working", got)
	}
	// No active, one paused, one pending → still pending (not held).
	b.must(cmd3{event.TypeThingStateChanged, "th_1", `{"state":"st_hold"}`})
	if got := derive(t, b, "th_c").Status; got != StatusPending {
		t.Fatalf("paused+pending: %q, want pending", got)
	}
	// No active, some paused, no pending → held.
	b.must(cmd3{event.TypeThingStateChanged, "th_2", `{"state":"st_done"}`})
	if got := derive(t, b, "th_c").Status; got != StatusHeld {
		t.Fatalf("paused+satisfied: %q, want held", got)
	}
	// Every leaf terminal → finished (has_abandoned tracks the cancel).
	b.must(cmd3{event.TypeThingStateChanged, "th_1", `{"state":"st_cancel"}`})
	if got := derive(t, b, "th_c"); got.Status != StatusFinished || !got.HasAbandoned {
		t.Fatalf("all terminal: %+v, want finished + has_abandoned", got)
	}
}

// TestOverAllocatedBadge: §2.5 reality wins — capacity lowered or
// availability off under open allocations badges the resource and the
// affected active things; nothing is force-closed.
func TestOverAllocatedBadge(t *testing.T) {
	b := statusWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":2,"capabilities":["cap_edit"]}`})
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":2,"requirement":"req_a"}`})

	if got := derive(t, b, "th_a"); got.Badges.OverAllocated {
		t.Fatalf("healthy allocation badged: %+v", got)
	}
	b.must(cmd3{event.TypeResourceSuperseded, "rs_pool", `{"name":"Editors","kind":"reusable","capacity":1}`})
	if got := derive(t, b, "th_a"); !got.Badges.OverAllocated || got.Status != StatusWorking {
		t.Fatalf("capacity lowered: %+v, want working + over-allocated badge", got)
	}
	if !b.p.ResourceOverAllocated("rs_pool") {
		t.Fatal("resource must carry the over-allocated badge too")
	}
	// Restore capacity: badge clears. Then availability off: badge returns
	// (the pinned-resource-down flavor of the same warning).
	b.must(cmd3{event.TypeResourceSuperseded, "rs_pool", `{"name":"Editors","kind":"reusable","capacity":2}`})
	if got := derive(t, b, "th_a"); got.Badges.OverAllocated {
		t.Fatalf("restored capacity still badged: %+v", got)
	}
	b.must(cmd3{event.TypeResourceAvailabilityChanged, "rs_pool", `{"available":false,"note":"down"}`})
	if got := derive(t, b, "th_a"); !got.Badges.OverAllocated {
		t.Fatalf("availability off: %+v, want over-allocated badge", got)
	}
}

// TestPinnedResourceDownBadge: a named resource going unavailable while
// pinned-and-allocated badges the active thing (§2.5) — a normal case, not
// an error.
func TestPinnedResourceDownBadge(t *testing.T) {
	b := statusWS(t)
	b.must(cmd3{event.TypeResourceCreated, "rs_anna", `{"name":"Anna","kind":"reusable","named":true,"capacity":1}`})
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":1,"resource":"rs_anna"}`})
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_anna","quantity":1,"requirement":"req_a"}`})
	b.must(cmd3{event.TypeResourceAvailabilityChanged, "rs_anna", `{"available":false,"note":"on leave"}`})

	got := derive(t, b, "th_a")
	if got.Status != StatusWorking || !got.Badges.OverAllocated {
		t.Fatalf("pinned resource down: %+v, want working + badge", got)
	}
	if b.p.SemanticOf(b.p.Things["th_a"]) != event.SemanticActive || !b.p.Allocations["al_1"].Open {
		t.Fatal("nothing may be force-closed")
	}
}

// TestOutOfStepBadge: superseding a requirement while active drifts the open
// allocations out of step (version comparison, §2.5); the atomic re-propose
// clears it. A requirement asserted while active is out of step by coverage.
func TestOutOfStepBadge(t *testing.T) {
	b := activeThing(t)
	if got := derive(t, b, "th_a"); got.Badges.AllocationsOutOfStep {
		t.Fatalf("fresh allocation badged: %+v", got)
	}
	b.must(cmd3{event.TypeRequirementSuperseded, "req_1", `{"quantity":1,"capabilities":["cap_edit"]}`})
	if got := derive(t, b, "th_a"); !got.Badges.AllocationsOutOfStep {
		t.Fatalf("superseded requirement: %+v, want out-of-step badge", got)
	}
	// Atomic re-propose reconciles.
	b.must(
		cmd3{event.TypeAllocationClosed, "al_1", `{}`},
		cmd3{event.TypeAllocationOpened, "al_2", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_1"}`})
	if got := derive(t, b, "th_a"); got.Badges.AllocationsOutOfStep {
		t.Fatalf("after re-propose: %+v, want no badge", got)
	}
	// A requirement asserted mid-active has no allocation: same badge.
	b.must(cmd3{event.TypeRequirementAsserted, "req_2", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
	if got := derive(t, b, "th_a"); !got.Badges.AllocationsOutOfStep {
		t.Fatalf("uncovered new requirement: %+v, want out-of-step badge", got)
	}
}

// TestStatusEntryTimestamps: the projection records, per leaf, the derived
// status and the commit ts of the batch at whose boundary it was entered —
// batch-boundary timestamps only, stable while the status is unchanged.
func TestStatusEntryTimestamps(t *testing.T) {
	b := statusWS(t)
	b.ts = "2026-07-19T11:00:00.000Z"
	b.must(thing("th_a", "A"))
	rec := b.p.Statuses["th_a"]
	if rec == nil || rec.Status != StatusReady || rec.SinceTS != "2026-07-19T11:00:00.000Z" {
		t.Fatalf("creation boundary: %+v", rec)
	}
	// An unrelated batch later must not disturb the entry ts.
	b.ts = "2026-07-19T12:00:00.000Z"
	b.must(thing("th_other", "Other"))
	if rec := b.p.Statuses["th_a"]; rec.Status != StatusReady || rec.SinceTS != "2026-07-19T11:00:00.000Z" {
		t.Fatalf("unchanged status must keep its entry ts: %+v", rec)
	}
	// A status change stamps the new boundary.
	b.ts = "2026-07-19T13:00:00.000Z"
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`})
	if rec := b.p.Statuses["th_a"]; rec.Status != StatusWorking || rec.SinceTS != "2026-07-19T13:00:00.000Z" {
		t.Fatalf("status change: %+v", rec)
	}
	// Composites carry no entry; demotion re-creates one.
	b.ts = "2026-07-19T14:00:00.000Z"
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	b.must(childThing("th_kid", "Kid", "th_a"))
	if _, ok := b.p.Statuses["th_a"]; ok {
		t.Fatal("composites must carry no status entry")
	}
	// Retraction drops the entry.
	b.must(cmd3{event.TypeThingRetracted, "th_kid", `{}`},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	b.must(cmd3{event.TypeThingRetracted, "th_other", `{}`})
	if _, ok := b.p.Statuses["th_other"]; ok {
		t.Fatal("retracted things must carry no status entry")
	}
	if rec := b.p.Statuses["th_a"]; rec == nil || rec.SinceTS != "2026-07-19T14:00:00.000Z" {
		t.Fatalf("demoted leaf re-enters bookkeeping at the demotion boundary: %+v", rec)
	}
}

// TestStarvationCreditSurvivesReadyFlip is the §3.4 spec-critical case: a
// thing resource_blocked for N ticks accrues credit that MUST survive the
// flip to ready when capacity frees — credit that expired the moment
// capacity freed would influence nothing.
func TestStarvationCreditSurvivesReadyFlip(t *testing.T) {
	b := statusWS(t)
	b.ts = "2026-07-19T11:00:00.000Z"
	b.must(thing("th_hog", "Hog"), thing("th_starved", "Starved"))
	b.must(
		cmd3{event.TypeRequirementAsserted, "req_h", `{"thing":"th_hog","quantity":2,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeRequirementAsserted, "req_s", `{"thing":"th_starved","quantity":1,"capabilities":["cap_edit"]}`})

	// The hog takes the whole pool at 12:00 → th_starved resource_blocked.
	b.ts = "2026-07-19T12:00:00.000Z"
	b.must(
		cmd3{event.TypeThingStateChanged, "th_hog", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_h", `{"thing":"th_hog","resource":"rs_pool","quantity":2,"requirement":"req_h"}`})
	if rec := b.p.Statuses["th_starved"]; rec.Status != StatusResourceBlocked || rec.SinceTS != "2026-07-19T12:00:00.000Z" {
		t.Fatalf("stint start: %+v", rec)
	}

	// Capacity frees at 18:00: the flip to ready must retain 6h of credit.
	b.ts = "2026-07-19T18:00:00.000Z"
	b.must(
		cmd3{event.TypeThingStateChanged, "th_hog", `{"state":"st_done"}`},
		cmd3{event.TypeAllocationClosed, "al_h", `{}`})
	rec := b.p.Statuses["th_starved"]
	if rec.Status != StatusReady || rec.SinceTS != "2026-07-19T18:00:00.000Z" {
		t.Fatalf("flip to ready: %+v", rec)
	}
	if rec.BlockedFor != 6*time.Hour {
		t.Fatalf("credit = %v, want 6h retained across the flip", rec.BlockedFor)
	}

	// The credit persists while ready…
	b.ts = "2026-07-19T19:00:00.000Z"
	b.must(thing("th_noise", "Noise"))
	if rec := b.p.Statuses["th_starved"]; rec.BlockedFor != 6*time.Hour {
		t.Fatalf("credit while ready = %v, want 6h", rec.BlockedFor)
	}
	// …accumulates across a second stint…
	b.ts = "2026-07-19T20:00:00.000Z"
	b.must(
		cmd3{event.TypeThingStateChanged, "th_hog", `{"state":"st_act2"}`},
		cmd3{event.TypeAllocationOpened, "al_h2", `{"thing":"th_hog","resource":"rs_pool","quantity":2,"requirement":"req_h"}`})
	b.ts = "2026-07-19T21:30:00.000Z"
	b.must(
		cmd3{event.TypeThingStateChanged, "th_hog", `{"state":"st_done"}`},
		cmd3{event.TypeAllocationClosed, "al_h2", `{}`})
	if rec := b.p.Statuses["th_starved"]; rec.BlockedFor != 7*time.Hour+30*time.Minute {
		t.Fatalf("accumulated credit = %v, want 7h30m", rec.BlockedFor)
	}
	// …and resets only when the thing itself holds allocations.
	b.ts = "2026-07-19T22:00:00.000Z"
	b.must(
		cmd3{event.TypeThingStateChanged, "th_starved", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_s", `{"thing":"th_starved","resource":"rs_pool","quantity":1,"requirement":"req_s"}`})
	if rec := b.p.Statuses["th_starved"]; rec.BlockedFor != 0 {
		t.Fatalf("credit after holding allocations = %v, want 0", rec.BlockedFor)
	}
}
