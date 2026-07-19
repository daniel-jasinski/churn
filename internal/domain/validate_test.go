package domain

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"churn/internal/event"
)

// cmd3 is the compact test form of one event: type, entity, payload JSON.
type cmd3 struct {
	typ    string
	entity string
	data   string
}

// tb drives ValidateBatch against an evolving projection.
type tb struct {
	t *testing.T
	p *Projection
	n int // batch counter, for distinct batch ids
	// ts, when set, is the commit timestamp stamped on subsequent batches
	// (must be monotone); the status-bookkeeping tests advance it per batch.
	ts string
}

func (b *tb) envelopes(cmds []cmd3) []event.Envelope {
	b.n++
	ts := b.ts
	if ts == "" {
		ts = t0
	}
	evs := make([]event.Envelope, len(cmds))
	for i, c := range cmds {
		seq := b.p.LastSeq + 1 + int64(i)
		evs[i] = event.Envelope{
			Seq:    seq,
			ID:     fmt.Sprintf("ev_%06d", seq),
			Origin: "wr_1",
			Batch:  fmt.Sprintf("b_%04d", b.n),
			TS:     ts,
			Actor:  "test",
			Type:   c.typ,
			V:      1,
			Entity: c.entity,
			Data:   []byte(c.data),
		}
	}
	return evs
}

// try validates one batch; on success the projection advances.
func (b *tb) try(expected map[string]int64, cmds ...cmd3) error {
	cand, err := ValidateBatch(b.p, b.envelopes(cmds), expected)
	if err == nil {
		b.p = cand
	}
	return err
}

// must applies a batch that has to be valid.
func (b *tb) must(cmds ...cmd3) {
	b.t.Helper()
	if err := b.try(nil, cmds...); err != nil {
		b.t.Fatalf("batch unexpectedly rejected: %v", err)
	}
}

// reject asserts the batch is rejected with the given kind (and exactly the
// given ids when wantIDs is non-nil) and that the projection did not move.
func (b *tb) reject(kind string, wantIDs []string, cmds ...cmd3) *Error {
	b.t.Helper()
	before := b.p
	err := b.try(nil, cmds...)
	if err == nil {
		b.t.Fatalf("batch unexpectedly accepted (want %s)", kind)
	}
	var de *Error
	if !errors.As(err, &de) {
		b.t.Fatalf("not a structured *Error: %v", err)
	}
	if de.Kind != kind {
		b.t.Fatalf("kind = %q, want %q (%v)", de.Kind, kind, err)
	}
	if wantIDs != nil && !reflect.DeepEqual(de.IDs, wantIDs) {
		b.t.Fatalf("ids = %v, want %v", de.IDs, wantIDs)
	}
	if b.p != before {
		b.t.Fatal("rejected batch moved the projection")
	}
	return de
}

// newWS returns a workspace with baseline vocabulary: the five semantics as
// states, a thing type, a capability, and a project.
func newWS(t *testing.T) *tb {
	p, err := Fold([]event.Envelope{initEv(1, t0)})
	if err != nil {
		t.Fatal(err)
	}
	b := &tb{t: t, p: p}
	b.must(
		cmd3{event.TypeStateDefined, "st_todo", `{"name":"todo","semantic":"pending"}`},
		cmd3{event.TypeStateDefined, "st_act", `{"name":"in_progress","semantic":"active"}`},
		cmd3{event.TypeStateDefined, "st_act2", `{"name":"executing","semantic":"active"}`},
		cmd3{event.TypeStateDefined, "st_done", `{"name":"done","semantic":"satisfied"}`},
		cmd3{event.TypeStateDefined, "st_hold", `{"name":"on_hold","semantic":"paused"}`},
		cmd3{event.TypeStateDefined, "st_cancel", `{"name":"cancelled","semantic":"abandoned"}`},
		cmd3{event.TypeTypeDefined, "ty_task", `{"name":"task"}`},
		cmd3{event.TypeCapabilityDefined, "cap_edit", `{"name":"editing"}`},
		cmd3{event.TypeProjectCreated, "pr_1", `{"name":"Alpha"}`},
	)
	return b
}

func thing(id, name string) cmd3 {
	return cmd3{event.TypeThingCreated, id, fmt.Sprintf(`{"name":%q,"project":"pr_1","type":"ty_task"}`, name)}
}

func childThing(id, name, parent string) cmd3 {
	return cmd3{event.TypeThingCreated, id, fmt.Sprintf(`{"name":%q,"parent":%q,"project":"pr_1","type":"ty_task"}`, name, parent)}
}

// ── acyclicity on the expanded leaf graph ──

func TestAncestorSubtreeCycleRejected(t *testing.T) {
	// The spec's canonical case (§2.1): a leaf depending on its own
	// ancestor's subtree — expansion makes it depend on itself.
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(childThing("th_l1", "L1", "th_a"), childThing("th_l2", "L2", "th_a"))
	de := b.reject(KindCycle, nil,
		cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_l1","to":"th_a"}`})
	if len(de.IDs) == 0 {
		t.Fatal("cycle error must list the offending expanded cycle")
	}
	found := false
	for _, id := range de.IDs {
		if id == "th_l1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expanded cycle %v must contain th_l1 (the self-dependent leaf)", de.IDs)
	}
}

func TestInheritedEdgeAcyclicity(t *testing.T) {
	// An edge ORIGINATING at a composite is inherited by all its leaves:
	// A(⊃L1) depends on B, and B depends on L1 → expanded cycle L1 ↔ B.
	b := newWS(t)
	b.must(thing("th_a", "A"), childThing("th_l1", "L1", "th_a"), thing("th_b", "B"))
	b.must(cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_a","to":"th_b"}`})
	de := b.reject(KindCycle, nil,
		cmd3{event.TypeDependencyAsserted, "dep_2", `{"from":"th_b","to":"th_l1"}`})
	if len(de.IDs) != 2 {
		t.Fatalf("expanded cycle = %v, want the two leaves th_b and th_l1", de.IDs)
	}
}

func TestDirectCycleRejected(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"), thing("th_b", "B"))
	b.must(cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_a","to":"th_b"}`})
	b.reject(KindCycle, []string{"th_a", "th_b"},
		cmd3{event.TypeDependencyAsserted, "dep_2", `{"from":"th_b","to":"th_a"}`})
	// Retracting dep_1 in the same batch makes the reversal legal.
	b.must(
		cmd3{event.TypeDependencyRetracted, "dep_1", `{}`},
		cmd3{event.TypeDependencyAsserted, "dep_2", `{"from":"th_b","to":"th_a"}`})
}

func TestSelfDependencyRejected(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.reject(KindCycle, []string{"th_a"},
		cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_a","to":"th_a"}`})
}

func TestDependencyOnCompositeIsLegal(t *testing.T) {
	// Composite-targeted edges are the ergonomic shortcut; acyclic ones pass.
	b := newWS(t)
	b.must(thing("th_a", "A"), childThing("th_l1", "L1", "th_a"), thing("th_b", "B"))
	b.must(cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_b","to":"th_a","on_abandoned":"block"}`})
	if dep := b.p.Dependencies["dep_1"]; dep.OnAbandoned != event.OnAbandonedBlock {
		t.Fatalf("policy = %q", dep.OnAbandoned)
	}
}

func TestOnAbandonedDefaultsToIgnore(t *testing.T) {
	// §2.2: default is unblock-with-warning, i.e. policy "ignore".
	b := newWS(t)
	b.must(thing("th_a", "A"), thing("th_b", "B"))
	b.must(cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_a","to":"th_b"}`})
	if dep := b.p.Dependencies["dep_1"]; dep.OnAbandoned != event.OnAbandonedIgnore {
		t.Fatalf("default policy = %q, want ignore", dep.OnAbandoned)
	}
}

// ── declared before use ──

func TestDeclaredBeforeUse(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.reject(KindUndefinedReference, []string{"ty_ghost"},
		cmd3{event.TypeThingCreated, "th_x", `{"name":"X","project":"pr_1","type":"ty_ghost"}`})
	b.reject(KindUndefinedReference, []string{"pr_ghost"},
		cmd3{event.TypeThingCreated, "th_x", `{"name":"X","project":"pr_ghost","type":"ty_task"}`})
	b.reject(KindUndefinedReference, []string{"st_ghost"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_ghost"}`})
	b.reject(KindUndefinedReference, []string{"cap_ghost"},
		cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"capabilities":["cap_ghost"]}`})
	b.must(cmd3{event.TypeResourceCreated, "rs_1", `{"name":"R","kind":"reusable","capacity":1}`})
	b.reject(KindUndefinedReference, []string{"cap_ghost"},
		cmd3{event.TypeCapabilityGranted, "rs_1", `{"capability":"cap_ghost"}`})
	// Declared earlier in the SAME batch is fine.
	b.must(
		cmd3{event.TypeCapabilityDefined, "cap_new", `{"name":"approval"}`},
		cmd3{event.TypeCapabilityGranted, "rs_1", `{"capability":"cap_new"}`})
}

func TestRevokeRequiresGrant(t *testing.T) {
	b := newWS(t)
	b.must(cmd3{event.TypeResourceCreated, "rs_1", `{"name":"R","kind":"reusable","capacity":1}`})
	b.reject(KindUndefinedReference, []string{"cap_edit"},
		cmd3{event.TypeCapabilityRevoked, "rs_1", `{"capability":"cap_edit"}`})
	b.must(cmd3{event.TypeCapabilityGranted, "rs_1", `{"capability":"cap_edit"}`})
	b.must(cmd3{event.TypeCapabilityRevoked, "rs_1", `{"capability":"cap_edit"}`})
}

// ── containment ──

func TestContainmentRules(t *testing.T) {
	b := newWS(t)
	b.must(cmd3{event.TypeProjectCreated, "pr_2", `{"name":"Beta"}`})
	b.must(thing("th_a", "A"), thing("th_b", "B"))

	// Cross-project parenting.
	b.reject(KindContainment, nil,
		cmd3{event.TypeThingCreated, "th_x", `{"name":"X","parent":"th_a","project":"pr_2","type":"ty_task"}`})

	// Parenting under a non-pending leaf.
	b.must(cmd3{event.TypeThingStateChanged, "th_b", `{"state":"st_done"}`})
	b.reject(KindContainment, []string{"th_b"}, childThing("th_x", "X", "th_b"))

	// Parenting under a requirement-carrying leaf.
	b.must(cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
	b.reject(KindContainment, []string{"th_a", "req_1"}, childThing("th_x", "X", "th_a"))
	b.must(cmd3{event.TypeRequirementRetracted, "req_1", `{}`})

	// Now th_a is a pending, requirement-free leaf: promotion happens.
	b.must(childThing("th_c", "C", "th_a"))
	if !b.p.IsComposite("th_a") {
		t.Fatal("th_a should be a composite now")
	}

	// Reparenting a thing under its own descendant makes containment cyclic.
	b.reject(KindContainment, []string{"th_a", "th_c"},
		cmd3{event.TypeThingSuperseded, "th_a", `{"name":"A","type":"ty_task","parent":"th_c"}`})
	// Self-parenting is the one-node case.
	b.reject(KindContainment, []string{"th_c", "th_c"},
		cmd3{event.TypeThingSuperseded, "th_c", `{"name":"C","type":"ty_task","parent":"th_c"}`})
}

func TestCompositeCarriesNoStateOrRequirements(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"), childThing("th_c", "C", "th_a"))
	b.reject(KindCompositeState, []string{"th_a"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	b.reject(KindCompositeRequirement, []string{"th_a"},
		cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
}

func TestPromotionClearsState(t *testing.T) {
	// Promotion happens the moment a first child is parented under a pending
	// leaf; the former leaf's current state ceases to apply (§2.1).
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	b.must(childThing("th_c", "C", "th_a"))
	if got := b.p.Things["th_a"].State; got != "" {
		t.Fatalf("composite state = %q, want cleared", got)
	}
}

func TestConversionFlowIsOneBatch(t *testing.T) {
	// The UI's one-click conversion (§2.1): move the leaf's requirements and
	// state onto an auto-created child step — a single batch the domain only
	// checks the preconditions of.
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`},
		cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
	b.must(
		cmd3{event.TypeRequirementRetracted, "req_1", `{}`},
		childThing("th_step", "Step", "th_a"),
		cmd3{event.TypeRequirementAsserted, "req_2", `{"thing":"th_step","quantity":1,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeThingStateChanged, "th_step", `{"state":"st_todo"}`})
	if !b.p.IsComposite("th_a") || b.p.Things["th_step"].State != "st_todo" {
		t.Fatal("conversion did not take")
	}
	if b.p.Requirements["req_2"].Thing != "th_step" {
		t.Fatal("requirement not moved")
	}
}

// ── demotion ──

func TestDemotionRequiresExplicitPendingTransition(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"), childThing("th_c", "C", "th_a"))

	// Retracting the last child without the explicit transition.
	b.reject(KindDemotion, []string{"th_a"},
		cmd3{event.TypeThingRetracted, "th_c", `{}`})

	// With a transition into a NON-pending state: still rejected.
	b.reject(KindDemotion, []string{"th_a"},
		cmd3{event.TypeThingRetracted, "th_c", `{}`},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_done"}`})

	// The spec-shaped batch: retraction + explicit pending transition.
	b.must(
		cmd3{event.TypeThingRetracted, "th_c", `{}`},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	if b.p.IsComposite("th_a") || b.p.Things["th_a"].State != "st_todo" {
		t.Fatal("demotion did not take")
	}
}

func TestDemotionViaReparentingAlsoRequiresTransition(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"), childThing("th_c", "C", "th_a"), thing("th_b", "B"))
	// Moving the only child away demotes th_a just the same.
	b.reject(KindDemotion, []string{"th_a"},
		cmd3{event.TypeThingSuperseded, "th_c", `{"name":"C","type":"ty_task","parent":"th_b"}`})
	b.must(
		cmd3{event.TypeThingSuperseded, "th_c", `{"name":"C","type":"ty_task","parent":"th_b"}`},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	if !b.p.IsComposite("th_b") || b.p.IsComposite("th_a") {
		t.Fatal("reparent did not take")
	}
}

func TestTransientPromotionDemotionRequiresStateChange(t *testing.T) {
	// A batch that promotes a leaf and demotes it again WITHIN the batch
	// (create child, retract child) must not wipe the leaf's state silently:
	// like any demotion, it needs the explicit pending transition.
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})

	// Without the transition: rejected.
	b.reject(KindDemotion, []string{"th_a"},
		childThing("th_c", "C", "th_a"),
		cmd3{event.TypeThingRetracted, "th_c", `{}`})

	// A state_changed applied BEFORE the transient promotion was wiped by
	// that promotion and does not count.
	b.reject(KindDemotion, []string{"th_a"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`},
		childThing("th_c", "C", "th_a"),
		cmd3{event.TypeThingRetracted, "th_c", `{}`})

	// With the explicit transition after the demoting event: accepted.
	b.must(
		childThing("th_c", "C", "th_a"),
		cmd3{event.TypeThingRetracted, "th_c", `{}`},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	if b.p.IsComposite("th_a") || b.p.Things["th_a"].State != "st_todo" {
		t.Fatalf("transient round trip left th_a as %+v", b.p.Things["th_a"])
	}
}

// TestDemotionBatchOrderIsRetractThenTransition pins the intended order
// sensitivity of demotion batches (M5's /batch docs must state it): the
// child-removing event comes first, the state_changed after — a state_changed
// while the thing is still a composite is rejected per-event.
func TestDemotionBatchOrderIsRetractThenTransition(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"), childThing("th_c", "C", "th_a"))

	// Wrong order: transition first hits the composite rule.
	b.reject(KindCompositeState, []string{"th_a"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`},
		cmd3{event.TypeThingRetracted, "th_c", `{}`})

	// Right order: accepted.
	b.must(
		cmd3{event.TypeThingRetracted, "th_c", `{}`},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
}

func TestRetractingCompositeAndChildTogether(t *testing.T) {
	// Retracting the whole subtree in one batch needs no demotion transition
	// — the parent no longer exists at end of batch.
	b := newWS(t)
	b.must(thing("th_a", "A"), childThing("th_c", "C", "th_a"))
	b.must(
		cmd3{event.TypeThingRetracted, "th_c", `{}`},
		cmd3{event.TypeThingRetracted, "th_a", `{}`})
	if len(b.p.Things) != 0 {
		t.Fatal("subtree not gone")
	}
}

// ── state semantics and vocabulary rules ──

func TestStateRetractionBlockedWhileOccupied(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	b.reject(KindRetractionBlocked, []string{"th_a"},
		cmd3{event.TypeStateRetracted, "st_todo", `{}`})
	// Move the thing out; retraction becomes legal.
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_hold"}`})
	b.must(cmd3{event.TypeStateRetracted, "st_todo", `{}`})
}

func TestSemanticImmutableWhileOccupied(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_todo"}`})
	// Semantic change while occupied: rejected, listing the occupants.
	b.reject(KindSemanticImmutable, []string{"th_a"},
		cmd3{event.TypeStateSuperseded, "st_todo", `{"name":"todo","semantic":"paused"}`})
	// Name/color/description supersede freely while occupied.
	b.must(cmd3{event.TypeStateSuperseded, "st_todo", `{"name":"backlog","semantic":"pending","color":"#123"}`})
	if st := b.p.States["st_todo"]; st.Name != "backlog" || st.Color != "#123" {
		t.Fatalf("supersession did not take: %+v", st)
	}
	// Unoccupied: semantic may change.
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_hold"}`})
	b.must(cmd3{event.TypeStateSuperseded, "st_todo", `{"name":"backlog","semantic":"paused"}`})
}

func TestVocabularyRetractionBlockedWhileReferenced(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.reject(KindRetractionBlocked, []string{"th_a"},
		cmd3{event.TypeTypeRetracted, "ty_task", `{}`})
	b.reject(KindRetractionBlocked, []string{"th_a"},
		cmd3{event.TypeProjectRetracted, "pr_1", `{}`})

	b.must(cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
	b.must(cmd3{event.TypeResourceCreated, "rs_1", `{"name":"R","kind":"reusable","capacity":1}`},
		cmd3{event.TypeCapabilityGranted, "rs_1", `{"capability":"cap_edit"}`})
	// Referenced by a requirement AND granted on a resource: both listed.
	b.reject(KindRetractionBlocked, []string{"req_1", "rs_1"},
		cmd3{event.TypeCapabilityRetracted, "cap_edit", `{}`})
	b.must(cmd3{event.TypeCapabilityRevoked, "rs_1", `{"capability":"cap_edit"}`})
	b.reject(KindRetractionBlocked, []string{"req_1"},
		cmd3{event.TypeCapabilityRetracted, "cap_edit", `{}`})
	b.must(cmd3{event.TypeRequirementRetracted, "req_1", `{}`})
	b.must(cmd3{event.TypeCapabilityRetracted, "cap_edit", `{}`})
}

// ── retraction blocked by inbound references ──

func TestThingRetractionEnumeratesReferences(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"), thing("th_b", "B"))
	b.must(
		cmd3{event.TypeDependencyAsserted, "dep_1", `{"from":"th_b","to":"th_a"}`},
		cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
	// The error enumerates every referencing id, sorted.
	b.reject(KindRetractionBlocked, []string{"dep_1", "req_1"},
		cmd3{event.TypeThingRetracted, "th_a", `{}`})
	// A parent lists its children too.
	b.must(childThing("th_c", "C", "th_b"))
	b.reject(KindRetractionBlocked, []string{"dep_1", "th_c"},
		cmd3{event.TypeThingRetracted, "th_b", `{}`})
	// No implicit cascade — but a client-composed cascade batch is just a batch.
	b.must(
		cmd3{event.TypeDependencyRetracted, "dep_1", `{}`},
		cmd3{event.TypeRequirementRetracted, "req_1", `{}`},
		cmd3{event.TypeThingRetracted, "th_a", `{}`},
		cmd3{event.TypeThingRetracted, "th_c", `{}`},
		cmd3{event.TypeThingRetracted, "th_b", `{}`})
	if len(b.p.Things) != 0 {
		t.Fatal("cascade batch incomplete")
	}
}

// ── pins and named resources ──

func TestPinRules(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(
		cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":4}`},
		cmd3{event.TypeResourceCreated, "rs_w4", `{"name":"W-04","kind":"reusable","named":true,"capacity":1}`})

	// Pinning an unnamed resource.
	b.reject(KindPinViolation, []string{"req_1", "rs_pool"},
		cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"resource":"rs_pool"}`})
	// Pinning the named one works.
	b.must(cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"resource":"rs_w4"}`})

	// A pinned resource cannot lose named…
	b.reject(KindPinViolation, []string{"req_1"},
		cmd3{event.TypeResourceSuperseded, "rs_w4", `{"name":"W-04","kind":"reusable","named":false,"capacity":2}`})
	// …and cannot be retracted while pinned.
	b.reject(KindRetractionBlocked, []string{"req_1"},
		cmd3{event.TypeResourceRetracted, "rs_w4", `{}`})

	// Un-pin; both become legal.
	b.must(cmd3{event.TypeRequirementSuperseded, "req_1", `{"quantity":1,"capabilities":["cap_edit"]}`})
	b.must(cmd3{event.TypeResourceSuperseded, "rs_w4", `{"name":"W-04","kind":"reusable","named":false,"capacity":2}`})
	b.must(cmd3{event.TypeResourceRetracted, "rs_w4", `{}`})
}

// ── allocations and the active coupling ──

// activeThing builds a workspace with th_a (requirement req_1: 2 × cap_edit)
// entered into st_act with allocation al_1 of quantity 2 on rs_pool (which
// carries cap_edit — allocations must sit on eligible resources).
func activeThing(t *testing.T) *tb {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":2,"capabilities":["cap_edit"]}`})
	b.must(cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":4}`},
		cmd3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_edit"}`})
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":2,"requirement":"req_1"}`})
	return b
}

func TestActiveEntryRequiresFullCoverage(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":2,"capabilities":["cap_edit"]}`})
	b.must(cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":4}`},
		cmd3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_edit"}`})

	// No allocations at all.
	b.reject(KindAllocationCoverage, []string{"th_a", "req_1"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`})
	// Partial coverage (1 of 2).
	b.reject(KindAllocationCoverage, []string{"th_a", "req_1"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_1"}`})
	// Over-coverage (3 of 2) is not quantity-exact either.
	b.reject(KindAllocationCoverage, []string{"th_a", "req_1"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":3,"requirement":"req_1"}`})
	// Exact coverage, split across two allocations: fine.
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_1"}`},
		cmd3{event.TypeAllocationOpened, "al_2", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_1"}`})
}

func TestOpenAllocationRequiresActiveThing(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
	b.must(cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":4}`})
	// Opening without any transition: the thing is pending at end of batch.
	b.reject(KindAllocation, []string{"al_1", "th_a"},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_1"}`})
}

func TestAllocationMustReferenceOwnRequirement(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"), thing("th_b", "B"))
	b.must(
		cmd3{event.TypeRequirementAsserted, "req_b", `{"thing":"th_b","quantity":1,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":4}`})
	b.reject(KindAllocation, nil,
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_b"}`})
}

func TestAllocationEventsOnlyAroundActivePhase(t *testing.T) {
	// An open+close pair inside one batch leaves no open allocation for the
	// end-state checks — but it would fabricate work history on a thing that
	// never held active semantics. Rejected on pending and satisfied things
	// alike; the legitimate entry/exit flows are covered elsewhere.
	b := newWS(t)
	b.must(thing("th_a", "A"), thing("th_b", "B"))
	b.must(
		cmd3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeRequirementAsserted, "req_b", `{"thing":"th_b","quantity":1,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":4}`})

	// th_a is pending.
	b.reject(KindAllocation, []string{"th_a"},
		cmd3{event.TypeAllocationOpened, "al_x", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_a"}`},
		cmd3{event.TypeAllocationClosed, "al_x", `{}`})

	// th_b is satisfied (was never active).
	b.must(cmd3{event.TypeThingStateChanged, "th_b", `{"state":"st_done"}`})
	b.reject(KindAllocation, []string{"th_b"},
		cmd3{event.TypeAllocationOpened, "al_y", `{"thing":"th_b","resource":"rs_pool","quantity":1,"requirement":"req_b"}`},
		cmd3{event.TypeAllocationClosed, "al_y", `{}`})
}

func TestBareCloseWhileActiveRejected(t *testing.T) {
	// Mid-active allocation changes must be the atomic §2.5 re-propose:
	// closing an allocation while the thing stays active without opening the
	// quantity-exact replacement leaves it active and uncovered — rejected.
	b := activeThing(t)
	b.reject(KindAllocationCoverage, []string{"th_a", "req_1"},
		cmd3{event.TypeAllocationClosed, "al_1", `{}`})

	// The atomic close+open re-propose is the accepted shape.
	b.must(
		cmd3{event.TypeAllocationClosed, "al_1", `{}`},
		cmd3{event.TypeAllocationOpened, "al_2", `{"thing":"th_a","resource":"rs_pool","quantity":2,"requirement":"req_1"}`})
	if !b.p.Allocations["al_2"].Open || b.p.Allocations["al_1"].Open {
		t.Fatal("re-propose did not take")
	}
}

func TestLeavingActiveMustCloseAllocations(t *testing.T) {
	b := activeThing(t)
	b.reject(KindAllocationCoverage, []string{"th_a", "al_1"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_done"}`})
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_done"}`},
		cmd3{event.TypeAllocationClosed, "al_1", `{}`})
	al := b.p.Allocations["al_1"]
	if al.Open || al.ClosedSeq == 0 {
		t.Fatalf("allocation not closed: %+v", al)
	}
}

func TestActiveToActiveKeepsAllocations(t *testing.T) {
	b := activeThing(t)
	b.must(cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act2"}`})
	if !b.p.Allocations["al_1"].Open {
		t.Fatal("active → active must keep allocations untouched")
	}
}

func TestCloseRequiresOpenAllocation(t *testing.T) {
	b := activeThing(t)
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_hold"}`},
		cmd3{event.TypeAllocationClosed, "al_1", `{}`})
	b.reject(KindAllocation, []string{"al_1"},
		cmd3{event.TypeAllocationClosed, "al_1", `{}`})
	b.reject(KindUnknownEntity, []string{"al_ghost"},
		cmd3{event.TypeAllocationClosed, "al_ghost", `{}`})
}

func TestRequirementRetractionBlockedByOpenAllocation(t *testing.T) {
	b := activeThing(t)
	b.reject(KindRetractionBlocked, []string{"al_1"},
		cmd3{event.TypeRequirementRetracted, "req_1", `{}`})
}

func TestRequirementSupersessionWhileActive(t *testing.T) {
	// §2.5: requirements MAY be edited on an active thing (the "allocations
	// out of step" flow); the fold tracks versions so the badge is derivable.
	b := activeThing(t)
	openVersion := b.p.Allocations["al_1"].RequirementVersion
	b.must(cmd3{event.TypeRequirementSuperseded, "req_1", `{"quantity":3,"capabilities":["cap_edit"]}`})
	req := b.p.Requirements["req_1"]
	if req.Quantity != 3 {
		t.Fatal("supersession did not take")
	}
	if req.Version <= openVersion {
		t.Fatalf("requirement version %d must move past the allocation's %d", req.Version, openVersion)
	}
	// One-click re-propose: close the obsolete allocation and open its
	// replacement atomically while the thing stays active.
	b.must(
		cmd3{event.TypeAllocationClosed, "al_1", `{}`},
		cmd3{event.TypeAllocationOpened, "al_2", `{"thing":"th_a","resource":"rs_pool","quantity":3,"requirement":"req_1"}`})
	if got := b.p.Allocations["al_2"]; !got.Open || got.RequirementVersion != req.Version {
		t.Fatalf("re-proposed allocation = %+v, want open at version %d", got, req.Version)
	}
}

func TestThingRetractionBlockedByOpenAllocation(t *testing.T) {
	b := activeThing(t)
	b.reject(KindRetractionBlocked, []string{"al_1", "req_1"},
		cmd3{event.TypeThingRetracted, "th_a", `{}`})
}

// ── the feasibility gate: eligibility (matcher predicate) + capacity ──

func TestAllocationEligibilityEnforced(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`})
	// rs_bare has capacity but NOT cap_edit.
	b.must(cmd3{event.TypeResourceCreated, "rs_bare", `{"name":"Bare","kind":"reusable","capacity":4}`})
	b.reject(KindInfeasible, []string{"al_1", "req_1", "rs_bare"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_bare","quantity":1,"requirement":"req_1"}`})
	// Granting the capability makes the same batch valid.
	b.must(cmd3{event.TypeCapabilityGranted, "rs_bare", `{"capability":"cap_edit"}`})
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_bare","quantity":1,"requirement":"req_1"}`})
}

func TestPinnedAllocationMustUseThePin(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(
		cmd3{event.TypeResourceCreated, "rs_anna", `{"name":"Anna","kind":"reusable","named":true,"capacity":1}`},
		cmd3{event.TypeResourceCreated, "rs_bob", `{"name":"Bob","kind":"reusable","named":true,"capacity":1}`})
	b.must(cmd3{event.TypeRequirementAsserted, "req_1", `{"thing":"th_a","quantity":1,"resource":"rs_anna"}`})
	// Bob cannot serve a requirement pinned to Anna, capabilities or not.
	b.reject(KindInfeasible, []string{"al_1", "req_1", "rs_bob"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_bob","quantity":1,"requirement":"req_1"}`})
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_1", `{"thing":"th_a","resource":"rs_anna","quantity":1,"requirement":"req_1"}`})
}

// ── capacity accounting (backing the matcher's distinct-units guarantee) ──

func TestCapacityAccounting(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"), thing("th_b", "B"))
	b.must(
		cmd3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":2,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeRequirementAsserted, "req_b", `{"thing":"th_b","quantity":2,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":3}`},
		cmd3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_edit"}`})
	b.must(
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_a", `{"thing":"th_a","resource":"rs_pool","quantity":2,"requirement":"req_a"}`})
	// 2 of 3 units taken; th_b needs 2 more.
	b.reject(KindCapacity, []string{"rs_pool"},
		cmd3{event.TypeThingStateChanged, "th_b", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_b", `{"thing":"th_b","resource":"rs_pool","quantity":2,"requirement":"req_b"}`})
}

func TestUnavailableResourceHasZeroCapacity(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(
		cmd3{event.TypeRequirementAsserted, "req_a", `{"thing":"th_a","quantity":1,"capabilities":["cap_edit"]}`},
		cmd3{event.TypeResourceCreated, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":3}`},
		cmd3{event.TypeCapabilityGranted, "rs_pool", `{"capability":"cap_edit"}`},
		cmd3{event.TypeResourceAvailabilityChanged, "rs_pool", `{"available":false,"note":"maintenance"}`})
	b.reject(KindCapacity, []string{"rs_pool"},
		cmd3{event.TypeThingStateChanged, "th_a", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_a", `{"thing":"th_a","resource":"rs_pool","quantity":1,"requirement":"req_a"}`})
}

func TestRealityWinsAllocationsNeverForceClosed(t *testing.T) {
	// §2.5: availability-off and capacity-lowering clamp free capacity at
	// max(0, effective − allocated) but never close open allocations or
	// reject the resource event.
	b := activeThing(t)
	b.must(cmd3{event.TypeResourceAvailabilityChanged, "rs_pool", `{"available":false,"note":"down"}`})
	if !b.p.Allocations["al_1"].Open {
		t.Fatal("availability-off must not close allocations")
	}
	if b.p.SemanticOf(b.p.Things["th_a"]) != event.SemanticActive {
		t.Fatal("thing must stay active")
	}
	// Capacity lowered below the allocated total: legal, over-allocated.
	b.must(cmd3{event.TypeResourceSuperseded, "rs_pool", `{"name":"Pool","kind":"reusable","capacity":1}`})
	if got := b.p.AllocatedQuantity("rs_pool"); got != 2 {
		t.Fatalf("allocated = %d, want 2 (still open)", got)
	}
	// But NEW allocations on the clamped resource are rejected.
	b.must(thing("th_b", "B"),
		cmd3{event.TypeRequirementAsserted, "req_b", `{"thing":"th_b","quantity":1,"capabilities":["cap_edit"]}`})
	b.reject(KindCapacity, []string{"rs_pool"},
		cmd3{event.TypeThingStateChanged, "th_b", `{"state":"st_act"}`},
		cmd3{event.TypeAllocationOpened, "al_b", `{"thing":"th_b","resource":"rs_pool","quantity":1,"requirement":"req_b"}`})
}

// ── expected_versions ──

func TestExpectedVersionsConflict(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	v := b.p.Version("th_a")
	if v == 0 {
		t.Fatal("created thing must have a version")
	}

	// Correct expectation: accepted.
	if err := b.try(map[string]int64{"th_a": v},
		cmd3{event.TypeThingSuperseded, "th_a", `{"name":"A2","type":"ty_task"}`}); err != nil {
		t.Fatal(err)
	}
	if b.p.Version("th_a") == v {
		t.Fatal("version must advance with the supersession")
	}

	// Stale expectation: structured conflict listing the stale ids.
	err := b.try(map[string]int64{"th_a": v},
		cmd3{event.TypeThingSuperseded, "th_a", `{"name":"A3","type":"ty_task"}`})
	var de *Error
	if !errors.As(err, &de) || de.Kind != KindStaleVersion || !reflect.DeepEqual(de.IDs, []string{"th_a"}) {
		t.Fatalf("want stale_version [th_a], got %v", err)
	}
	if b.p.Things["th_a"].Name != "A2" {
		t.Fatal("stale batch must not apply")
	}

	// Expecting version 0 for a new entity: accepted.
	if err := b.try(map[string]int64{"th_new": 0}, thing("th_new", "N")); err != nil {
		t.Fatal(err)
	}
}

// ── duplicate ids ──

func TestDuplicateIDRejected(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.reject(KindDuplicateID, []string{"th_a"}, thing("th_a", "again"))
	// Retraction does not free the id.
	b.must(cmd3{event.TypeThingRetracted, "th_a", `{}`})
	b.reject(KindDuplicateID, []string{"th_a"}, thing("th_a", "reborn"))
}
