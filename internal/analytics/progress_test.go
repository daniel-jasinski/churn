package analytics_test

import (
	"reflect"
	"testing"

	"churn/internal/analytics"
)

// TestProgressRollup: satisfied/non-abandoned per composite, rolled up the
// containment tree (§3.5).
func TestProgressRollup(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_top", "Top"))
	w.batch(childThing("th_mid", "Mid", "th_top"), childThing("th_l1", "L1", "th_top"))
	w.batch(childThing("th_l2", "L2", "th_mid"), childThing("th_l3", "L3", "th_mid"))
	// l1 done, l2 done, l3 cancelled.
	w.batch(state("th_l1", "st_done"), state("th_l2", "st_done"), state("th_l3", "st_cancel"))

	// th_mid: leaves l2 (done), l3 (abandoned → out of the denominator).
	mid := analytics.ProgressOf(w.p, "th_mid")
	if mid.Satisfied != 1 || mid.Total != 1 || !mid.HasAbandoned || mid.Display != "1/1" {
		t.Fatalf("mid = %+v", mid)
	}
	// th_top rolls up the whole subtree: l1+l2 of the 2 non-abandoned.
	top := analytics.ProgressOf(w.p, "th_top")
	if top.Satisfied != 2 || top.Total != 2 || !top.HasAbandoned || top.Display != "2/2" {
		t.Fatalf("top = %+v", top)
	}
	if f, ok := top.Fraction(); !ok || f != 1 {
		t.Fatalf("top fraction = %v/%v", f, ok)
	}

	// ProgressAll: every composite, sorted by id.
	all := analytics.ProgressAll(w.p)
	var ids []string
	for _, pr := range all {
		ids = append(ids, pr.Thing)
	}
	if !reflect.DeepEqual(ids, []string{"th_mid", "th_top"}) {
		t.Fatalf("ProgressAll ids = %v", ids)
	}
}

// TestProgressAbandonedOnly: a composite whose every leaf is abandoned has
// no denominator — it displays "—" with the abandoned badge, never a
// division by zero, never a silent 100% (§3.5).
func TestProgressAbandonedOnly(t *testing.T) {
	w := newWS(t)
	w.batch(thing("th_dead", "Dead end"))
	w.batch(childThing("th_d1", "D1", "th_dead"), childThing("th_d2", "D2", "th_dead"))
	w.batch(state("th_d1", "st_cancel"), state("th_d2", "st_cancel"))

	pr := analytics.ProgressOf(w.p, "th_dead")
	if pr.Total != 0 || pr.Satisfied != 0 || !pr.HasAbandoned || pr.Display != "—" {
		t.Fatalf("abandoned-only = %+v, want the display marker", pr)
	}
	if _, ok := pr.Fraction(); ok {
		t.Fatal("no denominator: Fraction must report not-ok")
	}
	// And per §2.1 the rollup is finished + has_abandoned.
	if d := w.p.Derive("th_dead"); d.Status != "finished" || !d.HasAbandoned {
		t.Fatalf("rollup = %+v", d)
	}

	// A pending leaf counts into the denominator as unsatisfied.
	w.batch(childThing("th_d3", "D3", "th_dead"))
	pr = analytics.ProgressOf(w.p, "th_dead")
	if pr.Satisfied != 0 || pr.Total != 1 || pr.Display != "0/1" {
		t.Fatalf("after new leaf = %+v", pr)
	}
}
