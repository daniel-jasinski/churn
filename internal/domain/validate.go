package domain

import (
	"fmt"
	"sort"

	"churn/internal/event"
	"churn/internal/match"
)

// ValidateBatch validates one event batch against the projection and, on
// success, returns the candidate projection with the batch applied (p itself
// is never mutated — the writer publishes the candidate after commit).
//
// Events are validated in order against the evolving intra-batch state, so a
// batch may reference entities it created earlier in itself; batch-level
// invariants (expanded-leaf acyclicity, active-entry allocation coverage,
// demotion transitions, capacity accounting) are checked once against the
// end-of-batch state. expected is the optional expected_versions
// precondition of the batch command: entity id → the version (seq) the
// client last saw, checked against the pre-batch projection; a mismatch is a
// KindStaleVersion conflict. It is a command, not a fact — nothing of it is
// persisted (§5.2).
//
// Every invariant rejection is a *Error; envelope-hygiene and payload-shape
// failures surface as plain errors from the fold.
func ValidateBatch(p *Projection, evs []event.Envelope, expected map[string]int64) (*Projection, error) {
	if len(evs) == 0 {
		return nil, fmt.Errorf("domain: empty batch")
	}

	if len(expected) > 0 {
		var stale []string
		for _, id := range sortedKeys(expected) {
			if p.Versions[id] != expected[id] {
				stale = append(stale, id)
			}
		}
		if len(stale) > 0 {
			return nil, errf(KindStaleVersion, stale, "expected versions are stale")
		}
	}

	sim := p.Clone()
	tr := newBatchTrace(p)
	for i, ev := range evs {
		payload, err := event.Decode(ev.Type, ev.V, ev.Data)
		if err != nil {
			return nil, fmt.Errorf("domain: event %s (seq %d): %w", ev.ID, ev.Seq, err)
		}
		if err := validateEvent(sim, ev, payload); err != nil {
			return nil, err
		}
		tr.before(sim, i, ev, payload)
		if err := sim.apply(ev, payload); err != nil {
			return nil, err
		}
		tr.after(sim, i)
	}
	if err := validateBatchEnd(p, sim, tr); err != nil {
		return nil, err
	}
	// The batch is committed as one unit: close its status boundary (§3.3)
	// so the candidate the writer publishes carries the bookkeeping the fold
	// would rebuild on replay.
	sim.refreshStatuses(sim.LastTS)
	return sim, nil
}

// batchTrace records what validateBatchEnd needs to know about the PATH the
// batch took, not just its end state: transient promotions/demotions, the
// order of leaf transitions vs state changes, and which things and
// allocations the batch's allocation events touched.
type batchTrace struct {
	// everComposite holds every thing that had children at any point:
	// pre-batch composites plus things promoted during the batch.
	everComposite map[string]struct{}
	// lastLeaf maps a thing to the index of the last batch event that took
	// its child count to zero (made it a leaf again).
	lastLeaf map[string]int
	// stateChanged maps a thing to the indexes of its thing.state_changed
	// events in this batch.
	stateChanged map[string][]int
	// allocThings holds every thing referenced by an allocation event
	// (opened or closed) in this batch.
	allocThings map[string]struct{}
	// opened lists the allocation ids opened by this batch, in order.
	opened []string

	// per-event scratch, filled by before and consumed by after.
	gained []string // parents that gain a child in this event
	lost   []string // parents that lose a child in this event
}

func newBatchTrace(pre *Projection) *batchTrace {
	tr := &batchTrace{
		everComposite: map[string]struct{}{},
		lastLeaf:      map[string]int{},
		stateChanged:  map[string][]int{},
		allocThings:   map[string]struct{}{},
	}
	for id, th := range pre.Things {
		if len(th.Children) > 0 {
			tr.everComposite[id] = struct{}{}
		}
	}
	return tr
}

// before inspects event i against the pre-apply state sim.
func (tr *batchTrace) before(sim *Projection, i int, ev event.Envelope, payload event.Payload) {
	tr.gained, tr.lost = nil, nil
	switch pl := payload.(type) {
	case *event.ThingCreated:
		if pl.Parent != "" {
			tr.gained = append(tr.gained, pl.Parent)
		}
	case *event.ThingSuperseded:
		if th, ok := sim.Things[ev.Entity]; ok && th.Parent != pl.Parent {
			if th.Parent != "" {
				tr.lost = append(tr.lost, th.Parent)
			}
			if pl.Parent != "" {
				tr.gained = append(tr.gained, pl.Parent)
			}
		}
	case *event.ThingRetracted:
		if th, ok := sim.Things[ev.Entity]; ok && th.Parent != "" {
			tr.lost = append(tr.lost, th.Parent)
		}
	case *event.ThingStateChanged:
		tr.stateChanged[ev.Entity] = append(tr.stateChanged[ev.Entity], i)
	case *event.AllocationOpened:
		tr.allocThings[pl.Thing] = struct{}{}
		tr.opened = append(tr.opened, ev.Entity)
	case *event.AllocationClosed:
		if al, ok := sim.Allocations[ev.Entity]; ok {
			tr.allocThings[al.Thing] = struct{}{}
		}
	}
}

// after folds event i's structural effects into the trace, against the
// post-apply state sim.
func (tr *batchTrace) after(sim *Projection, i int) {
	for _, pid := range tr.gained {
		tr.everComposite[pid] = struct{}{}
	}
	for _, pid := range tr.lost {
		if pt, ok := sim.Things[pid]; ok && len(pt.Children) == 0 {
			tr.lastLeaf[pid] = i
		}
	}
	tr.gained, tr.lost = nil, nil
}

// validateEvent checks one event's cross-entity invariants against the
// intra-batch state sim (the projection with all earlier batch events
// applied). Payload shape is already validated by event.Decode.
func validateEvent(sim *Projection, ev event.Envelope, payload event.Payload) error {
	id := ev.Entity
	switch pl := payload.(type) {
	case *event.LogInitialized, *event.WriterStarted:
		// Lifecycle events: position rules live in the fold.
		return nil

	case *event.StateDefined:
		return checkNewID(sim, id)
	case *event.StateSuperseded:
		st, ok := sim.States[id]
		if !ok {
			return errf(KindUnknownEntity, []string{id}, "state %s does not exist", id)
		}
		if st.Semantic != pl.Semantic {
			if occupying := sim.thingsInState(id); len(occupying) > 0 {
				return errf(KindSemanticImmutable, occupying,
					"state %s: semantic is immutable while things are in it (%s → %s)",
					id, st.Semantic, pl.Semantic)
			}
		}
	case *event.StateRetracted:
		if _, ok := sim.States[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "state %s does not exist", id)
		}
		if occupying := sim.thingsInState(id); len(occupying) > 0 {
			return errf(KindRetractionBlocked, occupying,
				"state %s cannot be retracted while things are in it", id)
		}

	case *event.TypeDefined:
		return checkNewID(sim, id)
	case *event.TypeSuperseded:
		if _, ok := sim.Types[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "type %s does not exist", id)
		}
	case *event.TypeRetracted:
		if _, ok := sim.Types[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "type %s does not exist", id)
		}
		var using []string
		for tid, th := range sim.Things {
			if th.Type == id {
				using = append(using, tid)
			}
		}
		if len(using) > 0 {
			sort.Strings(using)
			return errf(KindRetractionBlocked, using,
				"type %s cannot be retracted while things reference it", id)
		}

	case *event.CapabilityDefined:
		return checkNewID(sim, id)
	case *event.CapabilitySuperseded:
		if _, ok := sim.Capabilities[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "capability %s does not exist", id)
		}
	case *event.CapabilityRetracted:
		if _, ok := sim.Capabilities[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "capability %s does not exist", id)
		}
		var using []string
		for rid, req := range sim.Requirements {
			for _, c := range req.Capabilities {
				if c == id {
					using = append(using, rid)
					break
				}
			}
		}
		for rid, rs := range sim.Resources {
			if _, ok := rs.Capabilities[id]; ok {
				using = append(using, rid)
			}
		}
		if len(using) > 0 {
			sort.Strings(using)
			return errf(KindRetractionBlocked, using,
				"capability %s cannot be retracted while referenced", id)
		}

	case *event.ProjectCreated:
		return checkNewID(sim, id)
	case *event.ProjectSuperseded:
		if _, ok := sim.Projects[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "project %s does not exist", id)
		}
	case *event.ProjectRetracted:
		if _, ok := sim.Projects[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "project %s does not exist", id)
		}
		var using []string
		for tid, th := range sim.Things {
			if th.Project == id {
				using = append(using, tid)
			}
		}
		if len(using) > 0 {
			sort.Strings(using)
			return errf(KindRetractionBlocked, using,
				"project %s cannot be retracted while it contains things", id)
		}

	case *event.ThingCreated:
		if err := checkNewID(sim, id); err != nil {
			return err
		}
		if _, ok := sim.Projects[pl.Project]; !ok {
			return errf(KindUndefinedReference, []string{pl.Project},
				"thing %s: project %s does not exist", id, pl.Project)
		}
		if _, ok := sim.Types[pl.Type]; !ok {
			return errf(KindUndefinedReference, []string{pl.Type},
				"thing %s: type %s is not defined", id, pl.Type)
		}
		if pl.Parent != "" {
			return checkParent(sim, id, pl.Project, pl.Parent)
		}
	case *event.ThingSuperseded:
		th, ok := sim.Things[id]
		if !ok {
			return errf(KindUnknownEntity, []string{id}, "thing %s does not exist", id)
		}
		if _, ok := sim.Types[pl.Type]; !ok {
			return errf(KindUndefinedReference, []string{pl.Type},
				"thing %s: type %s is not defined", id, pl.Type)
		}
		if pl.Parent != th.Parent && pl.Parent != "" {
			return checkParent(sim, id, th.Project, pl.Parent)
		}
	case *event.ThingRetracted:
		th, ok := sim.Things[id]
		if !ok {
			return errf(KindUnknownEntity, []string{id}, "thing %s does not exist", id)
		}
		var refs []string
		refs = append(refs, sortedKeys(th.Children)...)
		for did, dep := range sim.Dependencies {
			if dep.From == id || dep.To == id {
				refs = append(refs, did)
			}
		}
		for rid, req := range sim.Requirements {
			if req.Thing == id {
				refs = append(refs, rid)
			}
		}
		refs = append(refs, sim.OpenAllocationsOf(id)...)
		if len(refs) > 0 {
			sort.Strings(refs)
			return errf(KindRetractionBlocked, refs,
				"thing %s cannot be retracted while referenced", id)
		}
	case *event.ThingStateChanged:
		th, ok := sim.Things[id]
		if !ok {
			return errf(KindUnknownEntity, []string{id}, "thing %s does not exist", id)
		}
		if len(th.Children) > 0 {
			return errf(KindCompositeState, []string{id},
				"thing %s is a composite: its state is a computed rollup, not a fact", id)
		}
		if _, ok := sim.States[pl.State]; !ok {
			return errf(KindUndefinedReference, []string{pl.State},
				"thing %s: state %s is not defined", id, pl.State)
		}

	case *event.DependencyAsserted:
		if err := checkNewID(sim, id); err != nil {
			return err
		}
		for _, end := range []string{pl.From, pl.To} {
			if _, ok := sim.Things[end]; !ok {
				return errf(KindUndefinedReference, []string{end},
					"dependency %s: thing %s does not exist", id, end)
			}
		}
	case *event.DependencyRetracted:
		if _, ok := sim.Dependencies[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "dependency %s does not exist", id)
		}

	case *event.RequirementAsserted:
		if err := checkNewID(sim, id); err != nil {
			return err
		}
		th, ok := sim.Things[pl.Thing]
		if !ok {
			return errf(KindUndefinedReference, []string{pl.Thing},
				"requirement %s: thing %s does not exist", id, pl.Thing)
		}
		if len(th.Children) > 0 {
			return errf(KindCompositeRequirement, []string{pl.Thing},
				"thing %s is a composite: composites carry no requirements", pl.Thing)
		}
		return checkRequirementRefs(sim, id, pl.Capabilities, pl.Resource)
	case *event.RequirementSuperseded:
		if _, ok := sim.Requirements[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "requirement %s does not exist", id)
		}
		// Supersession while the thing is active is legal — that is the
		// "allocations out of step" flow (§2.5). Only retraction is blocked.
		return checkRequirementRefs(sim, id, pl.Capabilities, pl.Resource)
	case *event.RequirementRetracted:
		if _, ok := sim.Requirements[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "requirement %s does not exist", id)
		}
		var using []string
		for aid, al := range sim.Allocations {
			if al.Open && al.Requirement == id {
				using = append(using, aid)
			}
		}
		if len(using) > 0 {
			sort.Strings(using)
			return errf(KindRetractionBlocked, using,
				"requirement %s cannot be retracted while open allocations reference it", id)
		}

	case *event.ResourceCreated:
		return checkNewID(sim, id)
	case *event.ResourceSuperseded:
		rs, ok := sim.Resources[id]
		if !ok {
			return errf(KindUnknownEntity, []string{id}, "resource %s does not exist", id)
		}
		if rs.Named && !pl.Named {
			if pins := sim.pinsOn(id); len(pins) > 0 {
				return errf(KindPinViolation, pins,
					"resource %s cannot lose named while requirements pin it", id)
			}
		}
	case *event.ResourceRetracted:
		if _, ok := sim.Resources[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "resource %s does not exist", id)
		}
		refs := sim.pinsOn(id)
		for aid, al := range sim.Allocations {
			if al.Open && al.Resource == id {
				refs = append(refs, aid)
			}
		}
		if len(refs) > 0 {
			sort.Strings(refs)
			return errf(KindRetractionBlocked, refs,
				"resource %s cannot be retracted while referenced", id)
		}
	case *event.ResourceAvailabilityChanged:
		// Never blocked: reality wins, warnings follow (§2.5).
		if _, ok := sim.Resources[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "resource %s does not exist", id)
		}

	case *event.CapabilityGranted:
		if _, ok := sim.Resources[id]; !ok {
			return errf(KindUnknownEntity, []string{id}, "resource %s does not exist", id)
		}
		if _, ok := sim.Capabilities[pl.Capability]; !ok {
			return errf(KindUndefinedReference, []string{pl.Capability},
				"resource %s: capability %s is not defined", id, pl.Capability)
		}
	case *event.CapabilityRevoked:
		rs, ok := sim.Resources[id]
		if !ok {
			return errf(KindUnknownEntity, []string{id}, "resource %s does not exist", id)
		}
		if _, ok := rs.Capabilities[pl.Capability]; !ok {
			return errf(KindUndefinedReference, []string{pl.Capability},
				"resource %s does not carry capability %s", id, pl.Capability)
		}

	case *event.AllocationOpened:
		if err := checkNewID(sim, id); err != nil {
			return err
		}
		th, ok := sim.Things[pl.Thing]
		if !ok {
			return errf(KindUndefinedReference, []string{pl.Thing},
				"allocation %s: thing %s does not exist", id, pl.Thing)
		}
		if len(th.Children) > 0 {
			return errf(KindAllocation, []string{pl.Thing},
				"allocation %s: thing %s is a composite; only leaves hold allocations", id, pl.Thing)
		}
		req, ok := sim.Requirements[pl.Requirement]
		if !ok {
			return errf(KindUndefinedReference, []string{pl.Requirement},
				"allocation %s: requirement %s does not exist", id, pl.Requirement)
		}
		if req.Thing != pl.Thing {
			return errf(KindAllocation, []string{id, pl.Requirement, pl.Thing},
				"allocation %s: requirement %s belongs to thing %s, not %s",
				id, pl.Requirement, req.Thing, pl.Thing)
		}
		if _, ok := sim.Resources[pl.Resource]; !ok {
			return errf(KindUndefinedReference, []string{pl.Resource},
				"allocation %s: resource %s does not exist", id, pl.Resource)
		}
	case *event.AllocationClosed:
		al, ok := sim.Allocations[id]
		if !ok {
			return errf(KindUnknownEntity, []string{id}, "allocation %s does not exist", id)
		}
		if !al.Open {
			return errf(KindAllocation, []string{id}, "allocation %s is already closed", id)
		}
	}
	return nil
}

// checkNewID guards creates: ids are stable and never reused.
func checkNewID(sim *Projection, id string) error {
	if _, ok := sim.Versions[id]; ok {
		return errf(KindDuplicateID, []string{id}, "entity id %s already exists: ids are never reused", id)
	}
	return nil
}

// checkParent enforces the containment rules of §2.1 for parenting child
// (which lives in childProject) under parent: the parent must exist, live in
// the same project, not be in child's own subtree (containment stays a
// tree), and — if it is a leaf — be in a pending-semantic state with no
// requirements (the promotion rule; the UI's conversion affordance is just a
// batch that establishes these preconditions first).
func checkParent(sim *Projection, child, childProject, parent string) error {
	pt, ok := sim.Things[parent]
	if !ok {
		return errf(KindUndefinedReference, []string{parent},
			"thing %s: parent %s does not exist", child, parent)
	}
	if pt.Project != childProject {
		return errf(KindContainment, []string{child, parent},
			"no cross-project parenting: thing %s is in %s, parent %s in %s",
			child, childProject, parent, pt.Project)
	}
	for a := parent; a != ""; {
		if a == child {
			return errf(KindContainment, []string{child, parent},
				"parenting %s under %s would make containment cyclic", child, parent)
		}
		at, ok := sim.Things[a]
		if !ok {
			break
		}
		a = at.Parent
	}
	if len(pt.Children) == 0 {
		if sem := sim.SemanticOf(pt); sem != event.SemanticPending {
			return errf(KindContainment, []string{parent},
				"parent %s is a %s-semantic leaf: parenting requires a pending leaf (convert it first, §2.1)",
				parent, sem)
		}
		var reqs []string
		for rid, req := range sim.Requirements {
			if req.Thing == parent {
				reqs = append(reqs, rid)
			}
		}
		if len(reqs) > 0 {
			sort.Strings(reqs)
			return errf(KindContainment, append([]string{parent}, reqs...),
				"parent %s carries requirements: parenting requires a requirement-free leaf (move them to a child step, §2.1)",
				parent)
		}
	}
	return nil
}

// checkRequirementRefs validates a requirement's declared-before-use and pin
// references.
func checkRequirementRefs(sim *Projection, id string, capabilities []string, resource string) error {
	for _, c := range capabilities {
		if _, ok := sim.Capabilities[c]; !ok {
			return errf(KindUndefinedReference, []string{c},
				"requirement %s: capability %s is not defined", id, c)
		}
	}
	if resource != "" {
		rs, ok := sim.Resources[resource]
		if !ok {
			return errf(KindUndefinedReference, []string{resource},
				"requirement %s: pinned resource %s does not exist", id, resource)
		}
		if !rs.Named {
			return errf(KindPinViolation, []string{id, resource},
				"requirement %s pins resource %s, which is not named", id, resource)
		}
	}
	return nil
}

// pinsOn returns the sorted ids of requirements pinning a resource.
func (p *Projection) pinsOn(resource string) []string {
	var pins []string
	for rid, req := range p.Requirements {
		if req.Resource == resource {
			pins = append(pins, rid)
		}
	}
	sort.Strings(pins)
	return pins
}

// thingsInState returns the sorted ids of things currently in a state.
func (p *Projection) thingsInState(state string) []string {
	var ids []string
	for tid, th := range p.Things {
		if th.State == state {
			ids = append(ids, tid)
		}
	}
	sort.Strings(ids)
	return ids
}

// validateBatchEnd checks the invariants that only make sense on the
// end-of-batch state (plus the trace of the path there): expanded-leaf
// acyclicity, the allocation⇔active coupling, demotion transitions, and
// capacity accounting.
func validateBatchEnd(pre, post *Projection, tr *batchTrace) error {
	// Acyclicity on the expanded leaf graph (§2.1).
	if cycle := post.expandedCycle(); cycle != nil {
		return errf(KindCycle, cycle,
			"dependency batch would make the expanded leaf graph cyclic")
	}

	// Every open allocation must belong to a leaf in an active-semantic
	// state. This post-state rule enforces both directions: opening only for
	// things entering/in active semantics, and leaving active semantics only
	// after closing every open allocation.
	openedSet := make(map[string]struct{}, len(tr.opened))
	for _, id := range tr.opened {
		openedSet[id] = struct{}{}
	}
	for _, aid := range sortedKeys(post.Allocations) {
		al := post.Allocations[aid]
		if !al.Open {
			continue
		}
		th, ok := post.Things[al.Thing]
		if !ok || len(th.Children) > 0 || post.SemanticOf(th) != event.SemanticActive {
			if _, fresh := openedSet[aid]; fresh {
				return errf(KindAllocation, []string{aid, al.Thing},
					"allocation %s: thing %s is not in an active-semantic state at end of batch", aid, al.Thing)
			}
			return errf(KindAllocationCoverage, append([]string{al.Thing}, post.OpenAllocationsOf(al.Thing)...),
				"thing %s leaves active semantics: the batch must close all its open allocations", al.Thing)
		}
	}

	// Allocation events — opened OR closed — are only legitimate around a
	// thing's active phase: the thing must be active-semantic at batch end
	// (entry, re-propose) or have been active-semantic before the batch
	// (exit). Anything else would fabricate work history on a thing that
	// never held the allocation's semantics.
	for _, tid := range sortedKeys(tr.allocThings) {
		if isActiveLeaf(post, tid) || isActiveLeaf(pre, tid) {
			continue
		}
		return errf(KindAllocation, []string{tid},
			"allocation events for thing %s, which neither is active-semantic at end of batch nor was before it", tid)
	}

	// Quantity-exact allocation coverage of every current requirement is
	// demanded (a) on entering active semantics — atomically in this batch
	// (§2.2, §5.2) — and (b) for a thing that stays active while the batch
	// touches its allocations: mid-active allocation changes must be exactly
	// the §2.5 atomic re-propose, never a partial close/open. A batch that
	// only supersedes a requirement (no allocation events) still legally
	// drifts out of step — that is the badge's job, not validation's.
	for _, tid := range sortedKeys(post.Things) {
		th := post.Things[tid]
		if len(th.Children) > 0 || post.SemanticOf(th) != event.SemanticActive {
			continue
		}
		preSem := event.SemanticPending // new things start pending
		if preTh, ok := pre.Things[tid]; ok && len(preTh.Children) == 0 {
			preSem = pre.SemanticOf(preTh)
		}
		if _, touched := tr.allocThings[tid]; preSem == event.SemanticActive && !touched {
			continue // active → active with allocations untouched
		}
		covered := map[string]int{}
		for _, al := range post.Allocations {
			if al.Open && al.Thing == tid {
				covered[al.Requirement] += al.Quantity
			}
		}
		for _, rid := range sortedKeys(post.Requirements) {
			req := post.Requirements[rid]
			if req.Thing != tid {
				continue
			}
			if covered[rid] != req.Quantity {
				return errf(KindAllocationCoverage, []string{tid, rid},
					"thing %s is active at end of batch: requirement %s needs quantity %d allocated, open allocations cover %d",
					tid, rid, req.Quantity, covered[rid])
			}
		}
	}

	// Demotion (§2.1): any thing that ends the batch as a leaf but was a
	// composite at ANY point of its application — a pre-batch composite
	// losing its last child, or a transient batch-internal promotion — must
	// be put into a pending-semantic state by an explicitly appended
	// state_changed in the SAME batch, applied AFTER it last became a leaf.
	// Stale pre-composite facts are never resurrected.
	//
	// ORDER-SENSITIVE BY DESIGN (M5's /batch documentation must state this):
	// the child-removing event comes first, the thing.state_changed after
	// it. A state_changed while the thing is still composite is rejected
	// per-event (KindCompositeState), and one applied before a later
	// promotion was wiped by that promotion and does not count.
	for _, tid := range sortedKeys(tr.everComposite) {
		th, ok := post.Things[tid]
		if !ok || len(th.Children) > 0 {
			continue
		}
		lastLeaf := -1
		if i, ok := tr.lastLeaf[tid]; ok {
			lastLeaf = i
		}
		explicit := false
		for _, i := range tr.stateChanged[tid] {
			if i > lastLeaf {
				explicit = true
				break
			}
		}
		if !explicit || th.State == "" || post.SemanticOf(th) != event.SemanticPending {
			return errf(KindDemotion, []string{tid},
				"thing %s is demoted to a leaf: the batch must explicitly transition it into a pending-semantic state after the demoting event", tid)
		}
	}

	return checkAllocationFeasibility(post, tr.opened)
}

// isActiveLeaf reports whether id is a leaf thing in an active-semantic
// state in p.
func isActiveLeaf(p *Projection, id string) bool {
	th, ok := p.Things[id]
	return ok && len(th.Children) == 0 && p.SemanticOf(th) == event.SemanticActive
}

// checkAllocationFeasibility is the feasibility gate for allocations opened
// by a batch, evaluated against the end-of-batch state (§2.4).
//
// The committed allocation set IS the assignment — the events name which
// resource serves which requirement — so feasibility is verified directly
// rather than searched for: every allocation opened by the batch (and still
// open at its end) must sit on a resource ELIGIBLE for the requirement it
// names — carrying the full capability AND-set, or being exactly the pin —
// via the same match.Eligible predicate that powers ready, resumable-now,
// and the proposer (the one-query-engine rule); and on every resource the
// batch allocates from, the total open quantity must fit effective capacity,
// which is what makes the units distinct: one unit serves one requirement of
// one thing. Quantity-exact coverage of every requirement is
// validateBatchEnd's coverage check; together the three checks are exactly
// "the open set is a feasible assignment".
//
// Pre-existing open allocations are exempt from the eligibility re-check:
// requirements may drift while active (§2.5) — surfacing that is the
// out-of-step badge's job, not validation's. Eligibility is judged against
// the requirement's END-of-batch version, consistent with the coverage
// check. Capacity-lowering and availability-off never force-close existing
// allocations ("reality wins") — over-allocation arising from resource
// events is legal and merely clamps free capacity at
// max(0, effective − allocated); only batches that OPEN allocations are held
// to the capacity line.
func checkAllocationFeasibility(post *Projection, opened []string) error {
	touched := map[string]struct{}{}
	for _, aid := range opened {
		al, ok := post.Allocations[aid]
		if !ok || !al.Open {
			continue // closed again within the same batch
		}
		touched[al.Resource] = struct{}{}
		req, okR := post.Requirements[al.Requirement]
		rs, okS := post.Resources[al.Resource]
		if !okR || !okS {
			continue // unreachable: per-event validation checked existence
		}
		eligible := match.Eligible(
			match.Requirement{ID: al.Requirement, Quantity: req.Quantity, Capabilities: req.Capabilities, Pin: req.Resource},
			match.Resource{ID: al.Resource, Capabilities: rs.Capabilities},
		)
		if !eligible {
			return errf(KindInfeasible, []string{aid, al.Requirement, al.Resource},
				"allocation %s: resource %s cannot satisfy requirement %s (capability/pin mismatch)",
				aid, al.Resource, al.Requirement)
		}
	}
	for _, rid := range sortedKeys(touched) {
		rs, ok := post.Resources[rid]
		if !ok {
			continue // unreachable: retraction is blocked by open allocations
		}
		if total := post.AllocatedQuantity(rid); total > rs.EffectiveCapacity() {
			return errf(KindCapacity, []string{rid},
				"resource %s: open allocations total %d exceed effective capacity %d",
				rid, total, rs.EffectiveCapacity())
		}
	}
	return nil
}
