// Package domaintest supports invariant fuzzing: a generator of random VALID
// command batches against an evolving projection, and an independent global
// invariant checker. It is shared by the domain fuzz test and the writer's
// replay-determinism test — the generator emits writer-agnostic commands the
// caller renders into envelopes (Envelopes) or writer commands.
package domaintest

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"churn/internal/canonjson"
	"churn/internal/domain"
	"churn/internal/event"
)

// Cmd is one generated event command: the writer-agnostic subset of what a
// batch needs (the harness supplies ids, timestamps, and batch grouping).
type Cmd struct {
	Type    string
	V       int
	Entity  string
	Payload event.Payload
}

// Envelopes renders cmds as one batch of envelopes appended directly after
// p, for feeding domain.ValidateBatch in tests. Event ids are synthetic —
// uniqueness is the store's concern, not the fold's. Timestamps are
// synthesized deterministically from the batch's first seq (one commit ts
// per batch, monotone with seq) so the §3.3 status-entry and duration
// bookkeeping is genuinely exercised — and replay-compared — by the fuzz.
// Every seventh batch REPEATS the previous batch's commit ts: the writer's
// monotone clamp makes equal consecutive batch timestamps legal, so replay
// identity must cover zero-duration boundaries too.
func Envelopes(p *domain.Projection, batch string, cmds []Cmd) []event.Envelope {
	evs := make([]event.Envelope, len(cmds))
	ts := syntheticTS(p.LastSeq + 1)
	if p.LastTS != "" && (p.LastSeq+1)%7 == 0 {
		ts = p.LastTS
	}
	for i, c := range cmds {
		data, err := canonjson.Encode(c.Payload)
		if err != nil {
			panic(fmt.Sprintf("domaintest: encoding %s payload: %v", c.Type, err))
		}
		seq := p.LastSeq + 1 + int64(i)
		evs[i] = event.Envelope{
			Seq:    seq,
			ID:     fmt.Sprintf("%s_%d", batch, i),
			Origin: "wr_fuzz",
			Batch:  batch,
			TS:     ts,
			Actor:  "fuzz",
			Type:   c.Type,
			V:      c.V,
			Entity: c.Entity,
			Data:   data,
		}
	}
	return evs
}

// syntheticEpoch is 2026-07-19T10:00:00Z — at or after every ts a test
// harness seeds its log with, so synthesized timestamps stay monotone.
const syntheticEpoch = 1784455200

// syntheticTS derives a deterministic, seq-monotone batch commit timestamp.
func syntheticTS(seq int64) string {
	return time.Unix(syntheticEpoch+seq, 0).UTC().Format("2006-01-02T15:04:05.000Z")
}

// BigLog builds a deterministic, foldable log at perf-test scale: nThings
// things (a mix of composites with children and top-level leaves), nDeps
// acyclic dependency edges with composite endpoints on both sides, a shared
// resource pool, requirements on a third of the top leaves, and a spread of
// satisfied/abandoned/pending states. Everything after log.initialized is
// ONE batch, so folding it costs exactly one status-boundary refresh.
//
// Acyclicity by construction: dependency edges always point from a
// higher-indexed containment ROOT to a lower-indexed one, and containment
// subtrees are whole roots — so the expanded leaf graph only ever crosses
// root buckets downward.
func BigLog(nThings, nDeps int) []event.Envelope {
	rng := rand.New(rand.NewSource(99))
	const ts = "2026-07-19T09:00:00.000Z"
	var evs []event.Envelope
	add := func(typ, entity string, payload event.Payload) {
		data, err := canonjson.Encode(payload)
		if err != nil {
			panic(fmt.Sprintf("domaintest: encoding %s payload: %v", typ, err))
		}
		seq := int64(len(evs) + 1)
		batch := "b_big"
		if seq == 1 {
			batch = "b_init"
		}
		evs = append(evs, event.Envelope{
			Seq: seq, ID: fmt.Sprintf("ev_%06d", seq), Origin: "wr_big",
			Batch: batch, TS: ts, Actor: "perf",
			Type: typ, V: 1, Entity: entity, Data: data,
		})
	}

	add(event.TypeLogInitialized, "", &event.LogInitialized{WorkspaceID: "ws_big"})
	add(event.TypeStateDefined, "st_p", &event.StateDefined{Name: "todo", Semantic: event.SemanticPending})
	add(event.TypeStateDefined, "st_s", &event.StateDefined{Name: "done", Semantic: event.SemanticSatisfied})
	add(event.TypeStateDefined, "st_x", &event.StateDefined{Name: "cancelled", Semantic: event.SemanticAbandoned})
	add(event.TypeTypeDefined, "ty_t", &event.TypeDefined{Name: "task"})
	add(event.TypeCapabilityDefined, "cap_a", &event.CapabilityDefined{Name: "a"})
	add(event.TypeCapabilityDefined, "cap_b", &event.CapabilityDefined{Name: "b"})
	add(event.TypeProjectCreated, "pr_big", &event.ProjectCreated{Name: "Big"})
	caps := []string{"cap_a", "cap_b"}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("rs_%02d", i)
		add(event.TypeResourceCreated, id, &event.ResourceCreated{
			Name: id, Kind: event.KindReusable, Capacity: 3,
		})
		add(event.TypeCapabilityGranted, id, &event.CapabilityGranted{Capability: caps[i%2]})
	}

	// Containment roots: composites (4 children each) first, then top
	// leaves. roots[i] is the bucket edges are ordered over.
	nComposites := nThings / 12
	children := nComposites * 4
	topLeaves := nThings - nComposites - children
	var roots []string
	newThing := func(id, parent string) {
		add(event.TypeThingCreated, id, &event.ThingCreated{
			Project: "pr_big", Name: id, Type: "ty_t", Parent: parent,
		})
	}
	var leaves []string
	for i := 0; i < nComposites; i++ {
		id := fmt.Sprintf("th_c%04d", i)
		newThing(id, "")
		roots = append(roots, id)
		for j := 0; j < 4; j++ {
			cid := fmt.Sprintf("%s_k%d", id, j)
			newThing(cid, id)
			leaves = append(leaves, cid)
		}
	}
	for i := 0; i < topLeaves; i++ {
		id := fmt.Sprintf("th_l%04d", i)
		newThing(id, "")
		roots = append(roots, id)
		leaves = append(leaves, id)
	}

	// States: ~20% satisfied, ~5% abandoned, rest pending.
	for _, id := range leaves {
		switch r := rng.Intn(100); {
		case r < 20:
			add(event.TypeThingStateChanged, id, &event.ThingStateChanged{State: "st_s"})
		case r < 25:
			add(event.TypeThingStateChanged, id, &event.ThingStateChanged{State: "st_x"})
		}
	}

	// Dependencies: root i → root j, i > j (declared endpoints may be
	// composites — the expansion is the load being tested).
	seen := map[[2]int]bool{}
	for d := 0; len(seen) < nDeps && d < nDeps*20; d++ {
		i, j := rng.Intn(len(roots)), rng.Intn(len(roots))
		if i == j {
			continue
		}
		if i < j {
			i, j = j, i
		}
		if seen[[2]int{i, j}] {
			continue
		}
		seen[[2]int{i, j}] = true
		add(event.TypeDependencyAsserted, fmt.Sprintf("dep_%05d", len(seen)),
			&event.DependencyAsserted{From: roots[i], To: roots[j]})
	}

	// Requirements on ~a third of the top leaves.
	for i := 0; i < topLeaves; i += 3 {
		set := []string{caps[i%2]}
		add(event.TypeRequirementAsserted, fmt.Sprintf("req_%05d", i), &event.RequirementAsserted{
			Thing: fmt.Sprintf("th_l%04d", i), Quantity: 1 + i%2, Capabilities: set,
		})
	}
	return evs
}

// Generator produces random valid batches. Every emitted batch has passed
// domain.ValidateBatch against the projection it was generated for; batches
// whose randomly chosen dependency edges would create an expanded-graph
// cycle are silently skipped (the one rule cheaper to probe than to
// pre-compute) — any other rejection is a generator bug and panics.
type Generator struct {
	rng *rand.Rand
	n   int // id/batch counter
	// Skipped counts proposals dropped for expanded-graph cycles.
	Skipped int
}

// NewGenerator returns a deterministic generator for one seed.
func NewGenerator(seed int64) *Generator {
	return &Generator{rng: rand.New(rand.NewSource(seed))}
}

func (g *Generator) id(prefix string) string {
	g.n++
	return fmt.Sprintf("%sfz%06d", prefix, g.n)
}

// Next returns the next valid batch for p, or nil when the generator could
// not produce one this round (retry with an evolved projection or accept
// fewer batches).
func (g *Generator) Next(p *domain.Projection) []Cmd {
	for tries := 0; tries < 25; tries++ {
		cmds := g.propose(p)
		if cmds == nil {
			continue
		}
		g.n++
		evs := Envelopes(p, fmt.Sprintf("bfz%06d", g.n), cmds)
		if _, err := domain.ValidateBatch(p, evs, nil); err != nil {
			var de *domain.Error
			if errors.As(err, &de) && de.Kind == domain.KindCycle {
				g.Skipped++
				continue
			}
			panic(fmt.Sprintf("domaintest: generator produced an invalid batch: %v\ncmds: %+v", err, cmds))
		}
		return cmds
	}
	return nil
}

// propose picks one weighted applicable operation and renders it, returning
// nil when the chosen operation turns out inapplicable in detail.
func (g *Generator) propose(p *domain.Projection) []Cmd {
	type op struct {
		weight int
		f      func() []Cmd
	}
	ops := []op{
		{2, func() []Cmd { return g.defineState(p) }},
		{1, func() []Cmd { return g.defineType(p) }},
		{1, func() []Cmd { return g.defineCapability(p) }},
		{1, func() []Cmd { return g.createProject(p) }},
		{1, func() []Cmd { return g.supersedeState(p) }},
		{8, func() []Cmd { return g.createThing(p) }},
		{3, func() []Cmd { return g.supersedeThing(p) }},
		{2, func() []Cmd { return g.reparentThing(p) }},
		{2, func() []Cmd { return g.retractThing(p) }},
		{6, func() []Cmd { return g.assertDependency(p) }},
		{2, func() []Cmd { return g.retractDependency(p) }},
		{5, func() []Cmd { return g.assertRequirement(p) }},
		{2, func() []Cmd { return g.supersedeRequirement(p) }},
		{1, func() []Cmd { return g.retractRequirement(p) }},
		{3, func() []Cmd { return g.createResource(p) }},
		{1, func() []Cmd { return g.supersedeResource(p) }},
		{1, func() []Cmd { return g.retractResource(p) }},
		{1, func() []Cmd { return g.toggleAvailability(p) }},
		{2, func() []Cmd { return g.grantCapability(p) }},
		{1, func() []Cmd { return g.revokeCapability(p) }},
		{10, func() []Cmd { return g.transition(p) }},
		{2, func() []Cmd { return g.repropose(p) }},
	}
	total := 0
	for _, o := range ops {
		total += o.weight
	}
	pick := g.rng.Intn(total)
	for _, o := range ops {
		if pick < o.weight {
			return o.f()
		}
		pick -= o.weight
	}
	return nil
}

// ── deterministic accessors ──

func (g *Generator) pick(ids []string) string {
	return ids[g.rng.Intn(len(ids))]
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func leafThings(p *domain.Projection) []string {
	var out []string
	for _, id := range sortedKeys(p.Things) {
		if len(p.Things[id].Children) == 0 {
			out = append(out, id)
		}
	}
	return out
}

func requirementsOf(p *domain.Projection, thing string) []string {
	var out []string
	for _, id := range sortedKeys(p.Requirements) {
		if p.Requirements[id].Thing == thing {
			out = append(out, id)
		}
	}
	return out
}

func statesOfSemantic(p *domain.Projection, semantic string) []string {
	var out []string
	for _, id := range sortedKeys(p.States) {
		if p.States[id].Semantic == semantic {
			out = append(out, id)
		}
	}
	return out
}

func namedResources(p *domain.Projection) []string {
	var out []string
	for _, id := range sortedKeys(p.Resources) {
		if p.Resources[id].Named {
			out = append(out, id)
		}
	}
	return out
}

// rawFree is effective capacity minus allocated units — deliberately NOT
// clamped at zero: on an over-allocated resource (§2.5 reality wins) the
// deficit must eat into any freed-units credit before new opens fit.
func rawFree(p *domain.Projection, rs string) int {
	return p.Resources[rs].EffectiveCapacity() - p.AllocatedQuantity(rs)
}

// eligibleParents lists things a new child in project may be parented under:
// composites, and pending-semantic requirement-free leaves (§2.1).
func eligibleParents(p *domain.Projection, project string) []string {
	var out []string
	for _, id := range sortedKeys(p.Things) {
		th := p.Things[id]
		if th.Project != project {
			continue
		}
		if len(th.Children) > 0 {
			out = append(out, id)
			continue
		}
		if p.SemanticOf(th) == event.SemanticPending && len(requirementsOf(p, id)) == 0 {
			out = append(out, id)
		}
	}
	return out
}

// subtree returns the set of things in the containment subtree rooted at id.
func subtree(p *domain.Projection, id string) map[string]struct{} {
	out := map[string]struct{}{id: {}}
	var walk func(string)
	walk = func(n string) {
		for _, c := range sortedKeys(p.Things[n].Children) {
			out[c] = struct{}{}
			walk(c)
		}
	}
	walk(id)
	return out
}

// dependenciesTouching lists dependency ids with an endpoint at thing.
func dependenciesTouching(p *domain.Projection, thing string) []string {
	var out []string
	for _, id := range sortedKeys(p.Dependencies) {
		d := p.Dependencies[id]
		if d.From == thing || d.To == thing {
			out = append(out, id)
		}
	}
	return out
}

// ── vocabulary ops ──

func (g *Generator) defineState(p *domain.Projection) []Cmd {
	if len(p.States) >= 12 {
		return nil
	}
	sem := event.Semantics[g.rng.Intn(len(event.Semantics))]
	id := g.id(event.PrefixState)
	return []Cmd{{event.TypeStateDefined, 1, id,
		&event.StateDefined{Name: "state " + id, Semantic: sem}}}
}

func (g *Generator) supersedeState(p *domain.Projection) []Cmd {
	if len(p.States) == 0 {
		return nil
	}
	// Cosmetic supersession only: semantic changes need occupancy analysis.
	id := g.pick(sortedKeys(p.States))
	st := p.States[id]
	return []Cmd{{event.TypeStateSuperseded, 1, id,
		&event.StateSuperseded{Name: st.Name + "'", Semantic: st.Semantic, Color: "#abc"}}}
}

func (g *Generator) defineType(p *domain.Projection) []Cmd {
	if len(p.Types) >= 6 {
		return nil
	}
	id := g.id(event.PrefixType)
	return []Cmd{{event.TypeTypeDefined, 1, id, &event.TypeDefined{Name: "type " + id}}}
}

func (g *Generator) defineCapability(p *domain.Projection) []Cmd {
	if len(p.Capabilities) >= 8 {
		return nil
	}
	id := g.id(event.PrefixCapability)
	return []Cmd{{event.TypeCapabilityDefined, 1, id, &event.CapabilityDefined{Name: "cap " + id}}}
}

func (g *Generator) createProject(p *domain.Projection) []Cmd {
	if len(p.Projects) >= 4 {
		return nil
	}
	id := g.id(event.PrefixProject)
	return []Cmd{{event.TypeProjectCreated, 1, id, &event.ProjectCreated{Name: "project " + id}}}
}

// ── thing ops ──

func (g *Generator) createThing(p *domain.Projection) []Cmd {
	if len(p.Projects) == 0 || len(p.Types) == 0 || len(p.Things) >= 150 {
		return nil
	}
	project := g.pick(sortedKeys(p.Projects))
	parent := ""
	if g.rng.Intn(10) < 4 {
		if parents := eligibleParents(p, project); len(parents) > 0 {
			parent = g.pick(parents)
		}
	}
	id := g.id(event.PrefixThing)
	return []Cmd{{event.TypeThingCreated, 1, id, &event.ThingCreated{
		Project: project, Name: "thing " + id,
		Type: g.pick(sortedKeys(p.Types)), Parent: parent,
	}}}
}

func (g *Generator) supersedeThing(p *domain.Projection) []Cmd {
	if len(p.Things) == 0 || len(p.Types) == 0 {
		return nil
	}
	id := g.pick(sortedKeys(p.Things))
	th := p.Things[id]
	return []Cmd{{event.TypeThingSuperseded, 1, id, &event.ThingSuperseded{
		Name: th.Name + "'", Type: g.pick(sortedKeys(p.Types)), Parent: th.Parent,
		Metadata: []byte(`{"rev":` + fmt.Sprint(g.rng.Intn(100)) + `}`),
	}}}
}

func (g *Generator) reparentThing(p *domain.Projection) []Cmd {
	if len(p.Things) < 2 {
		return nil
	}
	id := g.pick(sortedKeys(p.Things))
	th := p.Things[id]
	newParent := ""
	if g.rng.Intn(10) < 8 {
		var cands []string
		sub := subtree(p, id)
		for _, c := range eligibleParents(p, th.Project) {
			if _, in := sub[c]; !in && c != th.Parent {
				cands = append(cands, c)
			}
		}
		if len(cands) == 0 {
			return nil
		}
		newParent = g.pick(cands)
	} else if th.Parent == "" {
		return nil // already a root
	}
	cmds := []Cmd{{event.TypeThingSuperseded, 1, id, &event.ThingSuperseded{
		Name: th.Name, Type: th.Type, Parent: newParent,
	}}}
	// Moving the only child away demotes the old parent: the same batch must
	// transition it into a pending-semantic state (§2.1).
	if th.Parent != "" && len(p.Things[th.Parent].Children) == 1 {
		pending := statesOfSemantic(p, event.SemanticPending)
		if len(pending) == 0 {
			return nil
		}
		cmds = append(cmds, Cmd{event.TypeThingStateChanged, 1, th.Parent,
			&event.ThingStateChanged{State: g.pick(pending)}})
	}
	return cmds
}

func (g *Generator) retractThing(p *domain.Projection) []Cmd {
	var cands []string
	for _, id := range leafThings(p) {
		if len(dependenciesTouching(p, id)) == 0 &&
			len(requirementsOf(p, id)) == 0 &&
			len(p.OpenAllocationsOf(id)) == 0 {
			cands = append(cands, id)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	id := g.pick(cands)
	cmds := []Cmd{{event.TypeThingRetracted, 1, id, &event.ThingRetracted{}}}
	if parent := p.Things[id].Parent; parent != "" && len(p.Things[parent].Children) == 1 {
		pending := statesOfSemantic(p, event.SemanticPending)
		if len(pending) == 0 {
			return nil
		}
		cmds = append(cmds, Cmd{event.TypeThingStateChanged, 1, parent,
			&event.ThingStateChanged{State: g.pick(pending)}})
	}
	return cmds
}

// ── dependency ops ──

func (g *Generator) assertDependency(p *domain.Projection) []Cmd {
	ids := sortedKeys(p.Things)
	if len(ids) < 2 || len(p.Dependencies) >= 200 {
		return nil
	}
	from, to := g.pick(ids), g.pick(ids)
	if from == to {
		return nil
	}
	policy := [3]string{"", event.OnAbandonedBlock, event.OnAbandonedIgnore}[g.rng.Intn(3)]
	id := g.id(event.PrefixDependency)
	return []Cmd{{event.TypeDependencyAsserted, 1, id,
		&event.DependencyAsserted{From: from, To: to, OnAbandoned: policy}}}
}

func (g *Generator) retractDependency(p *domain.Projection) []Cmd {
	if len(p.Dependencies) == 0 {
		return nil
	}
	id := g.pick(sortedKeys(p.Dependencies))
	return []Cmd{{event.TypeDependencyRetracted, 1, id, &event.DependencyRetracted{}}}
}

// ── requirement ops ──

// requirementForm rolls a random valid (quantity, capabilities, resource).
func (g *Generator) requirementForm(p *domain.Projection) (int, []string, string) {
	if named := namedResources(p); len(named) > 0 && g.rng.Intn(10) < 3 {
		return 1, nil, g.pick(named)
	}
	caps := sortedKeys(p.Capabilities)
	if len(caps) == 0 {
		return 0, nil, ""
	}
	picked := []string{g.pick(caps)}
	if len(caps) > 1 && g.rng.Intn(2) == 0 {
		if second := g.pick(caps); second != picked[0] {
			picked = append(picked, second)
			sort.Strings(picked)
		}
	}
	return 1 + g.rng.Intn(2), picked, ""
}

func (g *Generator) assertRequirement(p *domain.Projection) []Cmd {
	leaves := leafThings(p)
	if len(leaves) == 0 {
		return nil
	}
	qty, caps, pin := g.requirementForm(p)
	if qty == 0 {
		return nil
	}
	// Only on non-active leaves: a new requirement on an active thing would
	// be instantly out of step — legal, but the generator keeps drift
	// attributable to supersessions.
	thing := g.pick(leaves)
	if p.SemanticOf(p.Things[thing]) == event.SemanticActive {
		return nil
	}
	id := g.id(event.PrefixRequirement)
	return []Cmd{{event.TypeRequirementAsserted, 1, id, &event.RequirementAsserted{
		Thing: thing, Quantity: qty, Capabilities: caps, Resource: pin,
	}}}
}

func (g *Generator) supersedeRequirement(p *domain.Projection) []Cmd {
	if len(p.Requirements) == 0 {
		return nil
	}
	qty, caps, pin := g.requirementForm(p)
	if qty == 0 {
		return nil
	}
	id := g.pick(sortedKeys(p.Requirements))
	return []Cmd{{event.TypeRequirementSuperseded, 1, id,
		&event.RequirementSuperseded{Quantity: qty, Capabilities: caps, Resource: pin}}}
}

func (g *Generator) retractRequirement(p *domain.Projection) []Cmd {
	var cands []string
	for _, id := range sortedKeys(p.Requirements) {
		blocked := false
		for _, al := range p.Allocations {
			if al.Open && al.Requirement == id {
				blocked = true
				break
			}
		}
		if !blocked {
			cands = append(cands, id)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	return []Cmd{{event.TypeRequirementRetracted, 1, g.pick(cands), &event.RequirementRetracted{}}}
}

// ── resource ops ──

func (g *Generator) createResource(p *domain.Projection) []Cmd {
	if len(p.Resources) >= 20 {
		return nil
	}
	id := g.id(event.PrefixResource)
	named := g.rng.Intn(10) < 3
	capacity := 1
	if !named {
		capacity = 1 + g.rng.Intn(3)
	}
	return []Cmd{{event.TypeResourceCreated, 1, id, &event.ResourceCreated{
		Name: "resource " + id, Kind: event.KindReusable, Named: named, Capacity: capacity,
	}}}
}

func (g *Generator) supersedeResource(p *domain.Projection) []Cmd {
	if len(p.Resources) == 0 {
		return nil
	}
	id := g.pick(sortedKeys(p.Resources))
	rs := p.Resources[id]
	capacity := 1
	if !rs.Named {
		// May drop below the allocated total: §2.5 reality wins, the fuzz
		// must survive over-allocated resources.
		capacity = 1 + g.rng.Intn(4)
	}
	return []Cmd{{event.TypeResourceSuperseded, 1, id, &event.ResourceSuperseded{
		Name: rs.Name + "'", Kind: rs.Kind, Named: rs.Named, Capacity: capacity,
	}}}
}

func (g *Generator) retractResource(p *domain.Projection) []Cmd {
	var cands []string
	for _, id := range sortedKeys(p.Resources) {
		if p.AllocatedQuantity(id) > 0 {
			continue
		}
		pinned := false
		for _, req := range p.Requirements {
			if req.Resource == id {
				pinned = true
				break
			}
		}
		if !pinned {
			cands = append(cands, id)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	return []Cmd{{event.TypeResourceRetracted, 1, g.pick(cands), &event.ResourceRetracted{}}}
}

func (g *Generator) toggleAvailability(p *domain.Projection) []Cmd {
	if len(p.Resources) == 0 {
		return nil
	}
	id := g.pick(sortedKeys(p.Resources))
	rs := p.Resources[id]
	return []Cmd{{event.TypeResourceAvailabilityChanged, 1, id,
		&event.ResourceAvailabilityChanged{Available: !rs.Available, Note: "fuzz toggle"}}}
}

func (g *Generator) grantCapability(p *domain.Projection) []Cmd {
	if len(p.Resources) == 0 || len(p.Capabilities) == 0 {
		return nil
	}
	id := g.pick(sortedKeys(p.Resources))
	var cands []string
	for _, c := range sortedKeys(p.Capabilities) {
		if _, ok := p.Resources[id].Capabilities[c]; !ok {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	return []Cmd{{event.TypeCapabilityGranted, 1, id,
		&event.CapabilityGranted{Capability: g.pick(cands)}}}
}

func (g *Generator) revokeCapability(p *domain.Projection) []Cmd {
	if len(p.Resources) == 0 {
		return nil
	}
	id := g.pick(sortedKeys(p.Resources))
	granted := sortedKeys(p.Resources[id].Capabilities)
	if len(granted) == 0 {
		return nil
	}
	return []Cmd{{event.TypeCapabilityRevoked, 1, id,
		&event.CapabilityRevoked{Capability: g.pick(granted)}}}
}

// ── transitions with allocations ──

func (g *Generator) transition(p *domain.Projection) []Cmd {
	leaves := leafThings(p)
	if len(leaves) == 0 || len(p.States) == 0 {
		return nil
	}
	thing := g.pick(leaves)
	th := p.Things[thing]
	target := g.pick(sortedKeys(p.States))
	// Bias a third of transitions toward active targets: entry/exit and
	// their allocation batches are the riskiest machinery, so every seed
	// should reach them even when few states carry active semantics.
	if actives := statesOfSemantic(p, event.SemanticActive); len(actives) > 0 && g.rng.Intn(3) == 0 {
		target = g.pick(actives)
	}
	from, to := p.SemanticOf(th), p.States[target].Semantic

	cmds := []Cmd{{event.TypeThingStateChanged, 1, thing, &event.ThingStateChanged{State: target}}}
	switch {
	case to == event.SemanticActive && from != event.SemanticActive:
		allocs, ok := g.coverRequirements(p, thing, nil)
		if !ok {
			return nil // no feasible coverage right now
		}
		cmds = append(cmds, allocs...)
	case from == event.SemanticActive && to != event.SemanticActive:
		for _, aid := range p.OpenAllocationsOf(thing) {
			cmds = append(cmds, Cmd{event.TypeAllocationClosed, 1, aid, &event.AllocationClosed{}})
		}
	}
	return cmds
}

// coverRequirements greedily builds allocation.opened commands covering every
// requirement of thing quantity-exactly from free capacity on ELIGIBLE
// resources (full capability AND-set, or the pin — the M3 feasibility gate);
// ok is false if greedy first-eligible coverage cannot be assembled right
// now. credit adds per-resource units freed earlier in the same batch
// (closed allocations). Greedy can miss assignments the matcher would find —
// then the transition is simply skipped this round; any assembled coverage
// is a valid assignment by construction.
func (g *Generator) coverRequirements(p *domain.Projection, thing string, credit map[string]int) (cmds []Cmd, ok bool) {
	used := map[string]int{} // resource → units taken by this batch
	for rs, c := range credit {
		used[rs] = -c
	}
	for _, rid := range requirementsOf(p, thing) {
		req := p.Requirements[rid]
		remaining := req.Quantity
		for _, rs := range eligibleResources(p, req) {
			if remaining == 0 {
				break
			}
			free := rawFree(p, rs) - used[rs]
			if free <= 0 {
				continue
			}
			take := free
			if take > remaining {
				take = remaining
			}
			used[rs] += take
			remaining -= take
			cmds = append(cmds, Cmd{event.TypeAllocationOpened, 1, g.id(event.PrefixAllocation),
				&event.AllocationOpened{Thing: thing, Resource: rs, Quantity: take, Requirement: rid}})
		}
		if remaining > 0 {
			return nil, false
		}
	}
	return cmds, true
}

// eligibleResources lists the resources whose units can serve the
// requirement — the pinned resource, or carriers of the full AND-set —
// sorted by id.
func eligibleResources(p *domain.Projection, req *domain.Requirement) []string {
	var out []string
	for _, id := range sortedKeys(p.Resources) {
		if req.Resource != "" {
			if id == req.Resource {
				out = append(out, id)
			}
			continue
		}
		eligible := true
		for _, c := range req.Capabilities {
			if _, has := p.Resources[id].Capabilities[c]; !has {
				eligible = false
				break
			}
		}
		if eligible {
			out = append(out, id)
		}
	}
	return out
}

// repropose is the §2.5 atomic re-propose: for one active thing, close ALL
// its open allocations and open a fresh quantity-exact coverage of its
// CURRENT requirements in the same batch (validation enforces exactly this
// all-or-nothing shape for mid-active allocation changes). Freed units count
// as credit, so re-proposing on a fully busy resource still works.
func (g *Generator) repropose(p *domain.Projection) []Cmd {
	var actives []string
	for _, id := range leafThings(p) {
		if len(p.OpenAllocationsOf(id)) > 0 {
			actives = append(actives, id)
		}
	}
	if len(actives) == 0 {
		return nil
	}
	thing := g.pick(actives)
	credit := map[string]int{}
	var cmds []Cmd
	for _, aid := range p.OpenAllocationsOf(thing) {
		al := p.Allocations[aid]
		credit[al.Resource] += al.Quantity
		cmds = append(cmds, Cmd{event.TypeAllocationClosed, 1, aid, &event.AllocationClosed{}})
	}
	opens, ok := g.coverRequirements(p, thing, credit)
	if !ok {
		return nil
	}
	return append(cmds, opens...)
}
