package analytics_test

import (
	"math"
	"reflect"
	"testing"

	"churn/internal/analytics"
	"churn/internal/event"
)

// contendedWS is the documented contention scenario — the spec's "a
// signature wanted by 6 ready things with marginal capacity 2 is a flashing
// light" (§3.3), plus a pinned demand waiting on a downed resource:
//
//   - rs_appr: capacity 2, cap_appr;
//   - th_r1…th_r6: ready, each requiring 1×cap_appr;
//   - rs_anna: named, unavailable;
//   - th_pin: resource_blocked, pinned to rs_anna.
func contendedWS(t *testing.T) *ws {
	w := newWS(t)
	w.batch(
		c3{event.TypeResourceCreated, "rs_appr", `{"name":"Approvers","kind":"reusable","capacity":2}`},
		c3{event.TypeCapabilityGranted, "rs_appr", `{"capability":"cap_appr"}`},
		c3{event.TypeResourceCreated, "rs_anna", `{"name":"Anna","kind":"reusable","named":true,"capacity":1}`},
		c3{event.TypeResourceAvailabilityChanged, "rs_anna", `{"available":false,"note":"on leave"}`})
	for _, id := range []string{"th_r1", "th_r2", "th_r3", "th_r4", "th_r5", "th_r6"} {
		w.batch(thing(id, id))
		w.batch(c3{event.TypeRequirementAsserted, "req_" + id, `{"thing":"` + id + `","quantity":1,"capabilities":["cap_appr"]}`})
	}
	w.batch(thing("th_pin", "Pinned"))
	w.batch(c3{event.TypeRequirementAsserted, "req_pin", `{"thing":"th_pin","quantity":1,"resource":"rs_anna"}`})
	return w
}

func TestContentionGolden(t *testing.T) {
	w := contendedWS(t)
	got := analytics.Contention(w.p)

	// Authoritative totals: 7 demand units (6 approvals + 1 pin), 2 fit.
	if got.Demand != 7 || got.Matched != 2 || got.Unmet != 5 {
		t.Fatalf("totals = %d/%d/%d, want demand 7, matched 2, unmet 5",
			got.Demand, got.Matched, got.Unmet)
	}
	if !got.AttributionIndicative {
		t.Fatal("attribution must be labeled indicative")
	}

	if len(got.Signatures) != 2 {
		t.Fatalf("signatures = %+v, want cap_appr and pin:rs_anna", got.Signatures)
	}
	appr := got.Signatures[0]
	if appr.Signature != "cap_appr" || appr.Demand != 6 || appr.Matched != 2 || appr.Unmet != 4 {
		t.Fatalf("cap_appr signature = %+v", appr)
	}
	if math.Abs(appr.Pressure-4.0/6.0) > 1e-9 {
		t.Fatalf("cap_appr pressure = %v, want 4/6", appr.Pressure)
	}
	wantThings := []string{"th_r1", "th_r2", "th_r3", "th_r4", "th_r5", "th_r6"}
	if !reflect.DeepEqual(appr.Things, wantThings) {
		t.Fatalf("cap_appr things = %v", appr.Things)
	}
	pin := got.Signatures[1]
	if pin.Signature != "pin:rs_anna" || pin.Demand != 1 || pin.Unmet != 1 || pin.Pressure != 1 {
		t.Fatalf("pin signature = %+v", pin)
	}

	// Per-resource attribution: the two approver units are taken; Anna
	// offers zero free units while unavailable.
	byRes := map[string]analytics.ResourceContention{}
	for _, r := range got.Resources {
		byRes[r.Resource] = r
	}
	if r := byRes["rs_appr"]; r.Free != 2 || r.Used != 2 {
		t.Fatalf("rs_appr = %+v", r)
	}
	if r := byRes["rs_anna"]; r.Free != 0 || r.Used != 0 {
		t.Fatalf("rs_anna = %+v", r)
	}

	// Heuristic tag ratio: 6 demand over 2 free, labeled heuristic.
	if len(got.TagRatios) != 1 {
		t.Fatalf("tag ratios = %+v", got.TagRatios)
	}
	tr := got.TagRatios[0]
	if tr.Capability != "cap_appr" || tr.DemandUnits != 6 || tr.FreeUnits != 2 || tr.Ratio != 3 || !tr.Heuristic {
		t.Fatalf("tag ratio = %+v", tr)
	}
}

// TestContentionCountsResourceBlockedDemand: the frontier (resource_blocked
// things) is part of the demand — th_pin's unit shows up even though the
// thing can't start; a downed pin is exactly what the dashboard must show.
func TestContentionCountsResourceBlockedDemand(t *testing.T) {
	w := contendedWS(t)
	if got := w.p.Derive("th_pin").Status; got != "resource_blocked" {
		t.Fatalf("th_pin status = %q", got)
	}
	rep := analytics.Contention(w.p)
	if rep.Signatures[1].Signature != "pin:rs_anna" || rep.Signatures[1].Unmet != 1 {
		t.Fatalf("pinned demand missing: %+v", rep.Signatures)
	}
	// Blocked things (deps unsatisfied) contribute NO demand.
	w.batch(thing("th_blocked", "Blocked"), thing("th_gate", "Gate"))
	w.batch(dep("dep_g", "th_blocked", "th_gate"))
	w.batch(c3{event.TypeRequirementAsserted, "req_blk", `{"thing":"th_blocked","quantity":5,"capabilities":["cap_appr"]}`})
	rep2 := analytics.Contention(w.p)
	if rep2.Demand != rep.Demand {
		t.Fatalf("blocked thing added demand: %d → %d", rep.Demand, rep2.Demand)
	}
}

// TestContentionDeterministic: byte-identical reports across runs.
func TestContentionDeterministic(t *testing.T) {
	w := contendedWS(t)
	a := analytics.Contention(w.p)
	b := analytics.Contention(w.p)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two contention runs differ")
	}
}

// TestTagRatioInfinity: demand against a capability with zero free units is
// +Inf pressure — flagged, not divided by zero.
func TestTagRatioInfinity(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_a", "A"))
	w.batch(c3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
	rep := analytics.Contention(w.p)
	if len(rep.TagRatios) != 1 || !math.IsInf(rep.TagRatios[0].Ratio, 1) {
		t.Fatalf("tag ratios = %+v, want +Inf for cap_edit", rep.TagRatios)
	}
}
