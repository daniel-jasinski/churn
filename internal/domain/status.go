package domain

import (
	"sort"
	"time"

	"churn/internal/event"
	"churn/internal/match"
)

// Status is the engine-owned vocabulary of conclusions (§2.2): users extend
// the vocabulary of facts (states), the engine computes statuses. Derived
// status is never stored — it is recomputed from the projection on demand;
// the projection only bookkeeps WHEN a leaf entered its current status
// (ThingStatus, §3.3).
type Status string

// The derived statuses of the §2.2 table. The state-semantic mirrors
// (working/finished/held/dropped) win by precedence; blocked, ready and
// resource_blocked refine pending-semantic leaves only.
const (
	StatusBlocked         Status = "blocked"
	StatusReady           Status = "ready"
	StatusResourceBlocked Status = "resource_blocked"
	StatusWorking         Status = "working"
	StatusFinished        Status = "finished"
	StatusHeld            Status = "held"
	StatusDropped         Status = "dropped"
	// StatusPending is a composite rollup reading only (§2.1: "some leaf
	// still pending"). Leaves in pending-semantic states always refine to
	// blocked/ready/resource_blocked instead.
	StatusPending Status = "pending"
)

// Badges are the computed warning markers of §2.2 and §2.5. All false is the
// healthy state.
type Badges struct {
	// AbandonedDependency: some dependency edge binding this leaf is
	// satisfied only because its ignore policy tolerates abandoned leaves in
	// the target subtree — unblocked with a warning (§2.2).
	AbandonedDependency bool
	// FinishedUnsatisfiedDeps: the §2.2 consistency warning — a satisfied
	// leaf whose dependencies are (no longer) satisfied reads finished, not
	// blocked, but the inconsistency is surfaced.
	FinishedUnsatisfiedDeps bool
	// OverAllocated: the thing holds an open allocation on a resource whose
	// open total exceeds its effective capacity — capacity lowered or
	// availability toggled off mid-work, including a pinned resource going
	// down (§2.5 "reality wins; warnings follow").
	OverAllocated bool
	// AllocationsOutOfStep: the thing's open allocations no longer mirror
	// its current requirements — an open allocation was made against an
	// older requirement version, or (while active) a current requirement is
	// not quantity-exactly covered (§2.5). One-click re-propose reconciles.
	AllocationsOutOfStep bool
}

// Derived is the full computed view of one thing: the §2.2 status table for
// leaves, the §2.1 rollup table for composites, plus indicators and badges.
type Derived struct {
	Status Status
	// HasAbandoned is the composite subtree flag of §2.1 (leaves: whether
	// the leaf itself is abandoned).
	HasAbandoned bool
	// ResumableNow is computed for held leaves only: whether a feasible
	// matching of the leaf's requirements onto distinct free units exists
	// right now — the same routine that powers ready (§2.2).
	ResumableNow bool
	Badges       Badges
}

// ThingStatus is the per-leaf bookkeeping the projection maintains at batch
// boundaries (§3.3): the current derived status, the commit timestamp of the
// batch at whose boundary the leaf entered it, and the starvation credit.
// It is a pure function of the log — replay rebuilds it identically — and
// all timestamps are batch commit timestamps (§3.6), never wall-clock reads.
type ThingStatus struct {
	Status Status
	// SinceTS is the commit ts of the batch at whose boundary the leaf
	// entered Status (the leaf's creation batch for a never-changed status).
	SinceTS string
	// BlockedFor accumulates COMPLETED resource_blocked stints since the
	// thing last held open allocations at a batch boundary (§3.4). It is
	// deliberately RETAINED when the thing flips to ready — the credit's
	// entire point is to give long-starved work first claim on freed
	// capacity. The still-running stint is not included; analytics adds
	// LastTS − SinceTS for currently resource_blocked things.
	BlockedFor time.Duration
}

func (s *ThingStatus) clone() *ThingStatus { return clonePtr(s) }

// ── matcher inputs: the one place domain facts become matching instances ──

// FreeResources renders every resource for the matcher with its free unit
// count: max(0, effective capacity − open allocated units) — availability
// off means 0, and over-allocation (§2.5 reality wins) clamps at zero rather
// than going negative. Sorted by resource id.
func (p *Projection) FreeResources() []match.Resource {
	allocated := map[string]int{}
	for _, al := range p.Allocations {
		if al.Open {
			allocated[al.Resource] += al.Quantity
		}
	}
	out := make([]match.Resource, 0, len(p.Resources))
	for _, id := range sortedKeys(p.Resources) {
		rs := p.Resources[id]
		free := rs.EffectiveCapacity() - allocated[id]
		if free < 0 {
			free = 0
		}
		out = append(out, match.Resource{ID: id, Free: free, Capabilities: rs.Capabilities})
	}
	return out
}

// MatchRequirementsOf renders the thing's requirements for the matcher,
// sorted by requirement id.
func (p *Projection) MatchRequirementsOf(thing string) []match.Requirement {
	var out []match.Requirement
	for _, rid := range sortedKeys(p.Requirements) {
		req := p.Requirements[rid]
		if req.Thing != thing {
			continue
		}
		out = append(out, match.Requirement{
			ID: rid, Quantity: req.Quantity,
			Capabilities: req.Capabilities, Pin: req.Resource,
		})
	}
	return out
}

// ProposedAllocation is one entry of the allocation proposer's output
// (§2.5): Quantity units of Resource to satisfy Requirement.
type ProposedAllocation struct {
	Requirement string
	Resource    string
	Quantity    int
}

// ProposeAllocations computes the concrete allocation set for entering thing
// into an active state: a feasible matching of its current requirements onto
// distinct free units, as (requirement, resource, quantity) triples ready to
// become allocation.opened events. ok is false when no feasible assignment
// exists right now. Deterministic under the match tie-break; sorted by
// (requirement, resource). A thing without requirements yields an empty
// proposal, ok = true.
func (p *Projection) ProposeAllocations(thing string) (proposal []ProposedAllocation, ok bool) {
	asg, ok := match.Feasible(p.MatchRequirementsOf(thing), p.FreeResources())
	if !ok {
		return nil, false
	}
	proposal = make([]ProposedAllocation, len(asg))
	for i, a := range asg {
		proposal[i] = ProposedAllocation{Requirement: a.Requirement, Resource: a.Resource, Quantity: a.Units}
	}
	return proposal, true
}

// ── dependency satisfaction (§2.1/§2.2) ──

// edgeVerdict is THE implementation of the §2.1 edge rule, shared by the
// one-off DepSatisfied and the bulk DepView: an edge is satisfied ⇔ every
// target leaf is terminal ∧ every abandoned leaf is permitted by the edge's
// policy. warn is true iff satisfied while tolerating abandoned leaves
// (policy ignore). Leaves in assume are treated as satisfied.
func edgeVerdict(targetLeaves []string, policy string, assume map[string]struct{}, semOf func(string) string) (ok, warn bool) {
	ok = true
	for _, l := range targetLeaves {
		if _, s := assume[l]; s {
			continue
		}
		switch semOf(l) {
		case event.SemanticSatisfied:
		case event.SemanticAbandoned:
			if policy == event.OnAbandonedBlock {
				ok = false
			} else {
				warn = true
			}
		default:
			ok = false
		}
	}
	if !ok {
		warn = false
	}
	return ok, warn
}

// DepSatisfied reports whether one dependency edge is satisfied: every leaf
// of the target subtree is terminal (satisfied or abandoned) AND every
// abandoned leaf is permitted by THIS edge's on_abandoned policy. warn is
// true iff the edge is satisfied while tolerating abandoned leaves (policy
// ignore) — the §2.2 warning badge. Leaves in assume are treated as
// satisfied (the simulated-completion device behind §3.2's inverse view and
// §3.3's immediate unlock).
func (p *Projection) DepSatisfied(dep *Dependency, assume map[string]struct{}) (ok, warn bool) {
	return edgeVerdict(p.Leaves(dep.To), dep.OnAbandoned, assume, func(l string) string {
		return p.SemanticOf(p.Things[l])
	})
}

// edgeBlockers is the blocker side of the edge rule: the target leaves
// preventing satisfaction — non-terminal leaves, plus abandoned leaves when
// the policy is block (§2.1). Input order is preserved.
func edgeBlockers(targetLeaves []string, policy string, semOf func(string) string) []string {
	var out []string
	for _, l := range targetLeaves {
		switch semOf(l) {
		case event.SemanticSatisfied:
		case event.SemanticAbandoned:
			if policy == event.OnAbandonedBlock {
				out = append(out, l)
			}
		default:
			out = append(out, l)
		}
	}
	return out
}

// DepBlockers returns the target-subtree leaves currently preventing the
// edge from being satisfied: non-terminal leaves, plus abandoned leaves when
// the policy is block (§2.1). Sorted by id; empty iff the edge is satisfied.
func (p *Projection) DepBlockers(dep *Dependency) []string {
	// p.Leaves is sorted, so the result is sorted.
	return edgeBlockers(p.Leaves(dep.To), dep.OnAbandoned, func(l string) string {
		return p.SemanticOf(p.Things[l])
	})
}

// DepsSatisfied reports whether every dependency edge binding leaf — edges
// originating at the leaf or inherited from an ancestor composite (§2.1) —
// is satisfied, treating leaves in assume as satisfied. warn aggregates the
// per-edge abandoned-tolerated warning across satisfied edges.
//
// One-off convenience: a caller sweeping many leaves builds a DepView once
// instead.
func (p *Projection) DepsSatisfied(leaf string, assume map[string]struct{}) (ok, warn bool) {
	ok = true
	for _, did := range sortedKeys(p.Dependencies) {
		dep := p.Dependencies[did]
		if !p.subtreeContains(dep.From, leaf) {
			continue
		}
		edgeOK, edgeWarn := p.DepSatisfied(dep, assume)
		ok = ok && edgeOK
		warn = warn || edgeWarn
	}
	return ok, warn
}

// DepView is a one-sweep snapshot of the dependency layer: every subtree
// expanded once, every edge's §2.1 verdict computed once, plus the expanded
// leaf adjacency in both directions. It exists to make full-workspace
// sweeps (status refresh, DeriveAll, analytics) linear instead of
// re-deriving per leaf — it is a per-call view over one immutable
// projection, NOT a persistent cache (nothing to invalidate; drop it when
// the projection changes).
type DepView struct {
	p *Projection
	// TargetLeaves maps each edge id to the sorted leaves of its target
	// subtree.
	TargetLeaves map[string][]string
	// Satisfied and Warn hold each edge's §2.1 verdict (warn: satisfied
	// while tolerating abandoned leaves under policy ignore).
	Satisfied map[string]bool
	Warn      map[string]bool
	// depOK / depWarn aggregate the verdicts per bound leaf (absent = no
	// binding edges = trivially satisfied); fromLeaves keeps each edge's
	// origin expansion for the lazy indexes below. The status sweep needs
	// only the aggregates; binding/graph/rev are built on first (analytics)
	// use — a DepView is a per-call, single-goroutine view.
	depOK      map[string]bool
	depWarn    map[string]bool
	fromLeaves map[string][]string
	binding    map[string][]string
	graph      map[string][]string
	rev        map[string][]string
}

// DepView builds the snapshot in one pass over the dependencies. Maps are
// pre-sized and semantics read straight off the projection: the build runs
// at every batch boundary, so allocation and GC churn are the constants
// that matter (§5 replay budget).
func (p *Projection) DepView() *DepView {
	nDeps := len(p.Dependencies)
	v := &DepView{
		p:            p,
		TargetLeaves: make(map[string][]string, nDeps),
		Satisfied:    make(map[string]bool, nDeps),
		Warn:         make(map[string]bool, nDeps),
		depOK:        make(map[string]bool, nDeps),
		depWarn:      make(map[string]bool, nDeps),
		fromLeaves:   make(map[string][]string, nDeps),
	}
	leavesMemo := make(map[string][]string, 2*nDeps)
	leavesOf := func(id string) []string {
		if ls, ok := leavesMemo[id]; ok {
			return ls
		}
		ls := p.Leaves(id)
		leavesMemo[id] = ls
		return ls
	}
	for did, dep := range p.Dependencies {
		to := leavesOf(dep.To)
		from := leavesOf(dep.From)
		v.TargetLeaves[did] = to
		v.fromLeaves[did] = from
		ok, warn := edgeVerdict(to, dep.OnAbandoned, nil, v.semOf)
		v.Satisfied[did], v.Warn[did] = ok, warn
		for _, f := range from {
			if prev, seen := v.depOK[f]; seen {
				v.depOK[f] = prev && ok
			} else {
				v.depOK[f] = ok
			}
			v.depWarn[f] = v.depWarn[f] || warn
		}
	}
	return v
}

// Binding returns leaf → sorted ids of the edges binding it — edges
// originating at the leaf or inherited from an ancestor (§2.1). Built
// lazily: the per-boundary status sweep only needs the aggregated verdicts.
func (v *DepView) Binding() map[string][]string {
	if v.binding == nil {
		v.binding = make(map[string][]string, len(v.depOK))
		for _, did := range sortedKeys(v.fromLeaves) {
			for _, f := range v.fromLeaves[did] {
				v.binding[f] = append(v.binding[f], did) // did ascending ⇒ sorted
			}
		}
	}
	return v.binding
}

// Graph returns the expanded leaf adjacency (dependent → sorted, deduped
// dependencies) — the §2.1 graph all leaf-level analytics are measured
// over. Built lazily on first use.
func (v *DepView) Graph() map[string][]string {
	if v.graph == nil {
		v.graph = map[string][]string{}
		for f, dids := range v.Binding() {
			set := map[string]struct{}{}
			for _, did := range dids {
				for _, t := range v.TargetLeaves[did] {
					set[t] = struct{}{}
				}
			}
			v.graph[f] = sortedKeys(set)
		}
	}
	return v.graph
}

// Reverse returns the transpose of Graph (dependency → sorted dependents).
func (v *DepView) Reverse() map[string][]string {
	if v.rev == nil {
		v.rev = map[string][]string{}
		for f, tos := range v.Graph() {
			for _, t := range tos {
				v.rev[t] = append(v.rev[t], f)
			}
		}
		for t := range v.rev {
			sort.Strings(v.rev[t])
		}
	}
	return v.rev
}

// EdgeBlockers returns the sorted target leaves preventing the edge's
// satisfaction (empty iff satisfied) — DepView's cached-input form of
// Projection.DepBlockers.
func (v *DepView) EdgeBlockers(did string) []string {
	dep, ok := v.p.Dependencies[did]
	if !ok {
		return nil
	}
	return edgeBlockers(v.TargetLeaves[did], dep.OnAbandoned, v.semOf)
}

// semOf reads a leaf's semantic — two map hits, deliberately uncached: a
// pre-built semantic map costs more to fill per boundary than it saves.
func (v *DepView) semOf(leaf string) string {
	return v.p.SemanticOf(v.p.Things[leaf])
}

// DepsSatisfied is DepView's O(1) form of Projection.DepsSatisfied.
func (v *DepView) DepsSatisfied(leaf string) (ok, warn bool) {
	okv, bound := v.depOK[leaf]
	if !bound {
		return true, false // no binding edges
	}
	return okv, v.depWarn[leaf]
}

// DepsSatisfiedAssuming reports whether every edge binding leaf would be
// satisfied if the leaves in assume were satisfied — the simulated
// completion behind §3.2's inverse view and §3.3's immediate unlock. Only
// edges whose target subtree intersects assume are re-evaluated; abandoned
// leaves NOT in assume keep blocking per their edge's policy.
func (v *DepView) DepsSatisfiedAssuming(leaf string, assume map[string]struct{}) bool {
	for _, did := range v.Binding()[leaf] {
		if v.Satisfied[did] {
			continue
		}
		touched := false
		for _, t := range v.TargetLeaves[did] {
			if _, in := assume[t]; in {
				touched = true
				break
			}
		}
		if !touched {
			return false
		}
		dep := v.p.Dependencies[did]
		if ok, _ := edgeVerdict(v.TargetLeaves[did], dep.OnAbandoned, assume, v.semOf); !ok {
			return false
		}
	}
	return true
}

// subtreeContains reports whether leaf lies in the containment subtree
// rooted at root (walking parents up from leaf — cheap and allocation-free).
func (p *Projection) subtreeContains(root, leaf string) bool {
	for id := leaf; id != ""; {
		if id == root {
			return true
		}
		th, ok := p.Things[id]
		if !ok {
			return false
		}
		id = th.Parent
	}
	return false
}

// ── derivation ──

// statusEval computes derived statuses for one projection state. It builds
// the dependency view plus per-thing requirement, free-unit, and open-
// allocation indexes once, so that a full sweep (refreshStatuses, DeriveAll)
// is one pass over each collection instead of quadratic re-scans.
type statusEval struct {
	p         *Projection
	view      *DepView
	reqs      map[string][]match.Requirement // thing → its matcher requirements (unordered)
	free      []match.Resource               // unordered; the matcher sorts by id
	allocated map[string]int                 // resource → open units
	openAlloc map[string][]string            // thing → open allocation ids (unordered)
}

// newStatusEval builds the per-sweep indexes. The reqs, openAlloc, and free
// collections are deliberately built in map order: every consumer is
// order-free — the matcher sorts its inputs by id itself, and the badge and
// coverage computations are sums and ORs — so sorting here would buy
// nothing but per-boundary cost.
func (p *Projection) newStatusEval() *statusEval {
	e := &statusEval{
		p:         p,
		view:      p.DepView(),
		reqs:      make(map[string][]match.Requirement, len(p.Requirements)),
		allocated: map[string]int{},
		openAlloc: map[string][]string{},
	}
	for rid, req := range p.Requirements {
		e.reqs[req.Thing] = append(e.reqs[req.Thing], match.Requirement{
			ID: rid, Quantity: req.Quantity,
			Capabilities: req.Capabilities, Pin: req.Resource,
		})
	}
	for aid, al := range p.Allocations {
		if al.Open {
			e.allocated[al.Resource] += al.Quantity
			e.openAlloc[al.Thing] = append(e.openAlloc[al.Thing], aid)
		}
	}
	e.free = make([]match.Resource, 0, len(p.Resources))
	for id, rs := range p.Resources {
		free := rs.EffectiveCapacity() - e.allocated[id]
		if free < 0 {
			free = 0
		}
		e.free = append(e.free, match.Resource{ID: id, Free: free, Capabilities: rs.Capabilities})
	}
	return e
}

// feasible: a feasible matching of the thing's requirements onto distinct
// free units exists (§2.4) — the shared routine behind ready, resumable-now,
// and the proposer.
func (e *statusEval) feasible(thing string) bool {
	reqs := e.reqs[thing]
	if len(reqs) == 0 {
		return true
	}
	_, ok := match.Feasible(reqs, e.free)
	return ok
}

// leafStatus computes the §2.2 status of a leaf, plus the dependency facts
// the badges need: depsOK (all binding edges satisfied) and warn (abandoned
// leaves tolerated by an ignore edge).
func (e *statusEval) leafStatus(id string) (st Status, depsOK, warn bool) {
	depsOK, warn = e.view.DepsSatisfied(id)
	switch e.view.semOf(id) {
	case event.SemanticActive:
		return StatusWorking, depsOK, warn
	case event.SemanticSatisfied:
		return StatusFinished, depsOK, warn
	case event.SemanticPaused:
		return StatusHeld, depsOK, warn
	case event.SemanticAbandoned:
		return StatusDropped, depsOK, warn
	}
	if !depsOK {
		return StatusBlocked, depsOK, warn
	}
	if e.feasible(id) {
		return StatusReady, depsOK, warn
	}
	return StatusResourceBlocked, depsOK, warn
}

// rollup computes the §2.1 composite table over the subtree's leaves.
func (e *statusEval) rollup(id string) (Status, bool) {
	allTerminal := true
	var anyActive, anyPaused, anyPending, anyAbandoned bool
	for _, l := range e.p.Leaves(id) {
		switch e.view.semOf(l) {
		case event.SemanticSatisfied:
		case event.SemanticAbandoned:
			anyAbandoned = true
		case event.SemanticActive:
			anyActive, allTerminal = true, false
		case event.SemanticPaused:
			anyPaused, allTerminal = true, false
		default: // pending
			anyPending, allTerminal = true, false
		}
	}
	switch {
	case allTerminal:
		return StatusFinished, anyAbandoned
	case anyActive:
		return StatusWorking, anyAbandoned
	case anyPaused && !anyPending:
		return StatusHeld, anyAbandoned
	default:
		return StatusPending, anyAbandoned
	}
}

func (e *statusEval) derive(id string) Derived {
	th, ok := e.p.Things[id]
	if !ok {
		return Derived{}
	}
	if len(th.Children) > 0 {
		st, hasAbandoned := e.rollup(id)
		return Derived{Status: st, HasAbandoned: hasAbandoned}
	}

	st, depsOK, warn := e.leafStatus(id)
	d := Derived{Status: st}
	d.HasAbandoned = st == StatusDropped
	d.Badges.AbandonedDependency = warn
	d.Badges.FinishedUnsatisfiedDeps = st == StatusFinished && !depsOK
	if st == StatusHeld {
		d.ResumableNow = e.feasible(id)
	}

	covered := map[string]int{}
	for _, aid := range e.openAlloc[id] {
		al := e.p.Allocations[aid]
		covered[al.Requirement] += al.Quantity
		if req, ok := e.p.Requirements[al.Requirement]; ok && req.Version != al.RequirementVersion {
			d.Badges.AllocationsOutOfStep = true
		}
		if rs, ok := e.p.Resources[al.Resource]; ok && e.allocated[al.Resource] > rs.EffectiveCapacity() {
			d.Badges.OverAllocated = true
		}
	}
	if st == StatusWorking {
		// A requirement asserted or superseded while active can drift out of
		// coverage without touching any allocation — same badge (§2.5). An
		// UNCOVERED requirement on active ENTRY cannot occur by construction:
		// validation demands quantity-exact coverage on every
		// allocation-touching batch (asserted by test, not computed as a
		// dead badge).
		for _, r := range e.reqs[id] {
			if covered[r.ID] != r.Quantity {
				d.Badges.AllocationsOutOfStep = true
			}
		}
	}
	return d
}

// Derive computes the derived view of one thing: the §2.2 status table for a
// leaf (with precedence: state-semantic mirrors win; blocked/ready/
// resource_blocked refine pending leaves only), the §2.1 rollup table for a
// composite, plus resumable-now and the badges. Pure and recomputed from
// scratch — never cached, never stored. An unknown id yields the zero
// Derived.
func (p *Projection) Derive(id string) Derived {
	return p.newStatusEval().derive(id)
}

// DeriveAll computes the derived view of every thing in one pass. Callers
// needing deterministic order iterate the keys sorted.
func (p *Projection) DeriveAll() map[string]Derived {
	e := p.newStatusEval()
	out := make(map[string]Derived, len(p.Things))
	for id := range p.Things {
		out[id] = e.derive(id)
	}
	return out
}

// ResourceOverAllocated reports the §2.5 resource-side badge: open
// allocations exceed effective capacity (capacity lowered or availability
// off while units were checked out).
func (p *Projection) ResourceOverAllocated(id string) bool {
	rs, ok := p.Resources[id]
	return ok && p.AllocatedQuantity(id) > rs.EffectiveCapacity()
}

// ── batch-boundary bookkeeping (§3.3) ──

// refreshStatuses re-evaluates every leaf's derived status at a batch
// boundary and updates Statuses: ts is the boundary's batch commit
// timestamp (always the projection's LastTS). It is triggered by the fold
// when the first event of the NEXT batch arrives, and by
// Fold/ValidateBatch at end-of-log/end-of-batch — a boundary can therefore
// be visited twice, which the StatusSeq watermark turns into a free skip:
// once the boundary at LastSeq is refreshed, nothing can change until
// another event folds.
//
// Bookkeeping rules: a leaf keeps its SinceTS while its status is unchanged;
// on a status CHANGE the leaf enters the new status at ts, and a completed
// resource_blocked stint is credited to BlockedFor. A leaf holding open
// allocations at a boundary resets BlockedFor to zero ("since the thing last
// held allocations", §3.4); the flip resource_blocked → ready retains it.
// Composites and retracted things carry no entry.
//
// Per-leaf updates are independent of each other, so the sweeps iterate
// maps directly — the outcome is order-free.
func (p *Projection) refreshStatuses(ts string) {
	if p.StatusSeq == p.LastSeq {
		return // this boundary is already refreshed
	}
	e := p.newStatusEval()
	for id, th := range p.Things {
		if len(th.Children) > 0 {
			delete(p.Statuses, id)
			continue
		}
		st, _, _ := e.leafStatus(id)
		rec := p.Statuses[id]
		if rec == nil {
			p.Statuses[id] = &ThingStatus{Status: st, SinceTS: ts}
			rec = p.Statuses[id]
		} else if rec.Status != st {
			if rec.Status == StatusResourceBlocked {
				rec.BlockedFor += TSDelta(rec.SinceTS, ts)
			}
			rec.Status = st
			rec.SinceTS = ts
		}
		if len(e.openAlloc[id]) > 0 {
			rec.BlockedFor = 0
		}
	}
	// Entries for retracted things were already dropped by the fold
	// (thing.retracted deletes eagerly); composites were dropped above.
	p.StatusSeq = p.LastSeq
}

// TSDelta returns the duration between two writer timestamps, clamped at
// zero, and zero when either does not parse (validated logs always carry
// writer-formatted timestamps; synthetic test logs may not).
func TSDelta(from, to string) time.Duration {
	a, errA := time.Parse(time.RFC3339Nano, from)
	b, errB := time.Parse(time.RFC3339Nano, to)
	if errA != nil || errB != nil {
		return 0
	}
	if d := b.Sub(a); d > 0 {
		return d
	}
	return 0
}
