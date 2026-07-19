// Package domain holds the pure projection core: the in-memory model, the
// fold that builds it from the event log, and the batch validation that
// enforces every DESIGN.md §5.2 invariant before events are appended. It
// performs no I/O, reads no clock, and generates no ids — ids for new
// entities are minted by the writer/command layer and arrive inside events.
//
// Division of labor: ValidateBatch rejects invalid facts before they reach
// the log; the fold (Apply) is a total, deterministic function over validated
// logs — any event that passed validation applies without error, and fold
// errors occur only on structurally impossible logs (fail closed).
package domain

import (
	"fmt"
	"sort"

	"churn/internal/event"
)

// Projection is the current in-memory model, a deterministic fold over the
// log. A published *Projection is immutable: the writer clones, applies, and
// atomically swaps — readers never see a half-applied batch.
//
// Retracted entities are removed from the maps (a tombstone ends existence
// now; that the entity existed remains recorded in the log). Closed
// allocations are the one exception: they are kept forever as work history
// (§2.5), distinguished by Allocation.Open.
type Projection struct {
	// WorkspaceID is the immutable id recorded by log.initialized.
	WorkspaceID string
	// Origin is the current writer lineage, updated by writer.started.
	Origin string
	// LastSeq is the seq of the last event folded in; 0 for an empty log.
	LastSeq int64
	// LastTS is the timestamp of the last event folded in; "" for an empty
	// log. Fixed-width UTC form, so string order is time order.
	LastTS string
	// LastBatch is the batch id of the last event folded in; "" for an
	// empty log. The fold uses it to detect batch boundaries, where derived
	// statuses are re-evaluated into Statuses (§3.3).
	LastBatch string

	// States, Types, ResourceTypes, and Capabilities are the vocabulary
	// registries (§5.3), keyed by st_/ty_/rt_/cap_ id.
	States        map[string]*State
	Types         map[string]*ThingType
	ResourceTypes map[string]*ResourceType
	Capabilities  map[string]*Capability
	// Projects is keyed by pr_ id.
	Projects map[string]*Project
	// Things is keyed by th_ id.
	Things map[string]*Thing
	// Dependencies is keyed by dep_ id.
	Dependencies map[string]*Dependency
	// Requirements is keyed by req_ id.
	Requirements map[string]*Requirement
	// Resources is keyed by rs_ id.
	Resources map[string]*Resource
	// Allocations is keyed by al_ id and holds open and closed allocations.
	Allocations map[string]*Allocation
	// Versions maps entity id → seq of the last event whose entity column
	// was that id — the per-entity version expected_versions checks against.
	// Entries are never removed, even on retraction: Versions doubles as the
	// registry that makes "ids are never reused" checkable.
	Versions map[string]int64
	// Statuses is the per-leaf status-entry bookkeeping of §3.3, keyed by
	// thing id: the derived status re-evaluated after every committed batch,
	// the batch commit ts it was entered at, and the cumulative
	// resource-blocked credit (§3.4). A pure function of the log — replay
	// rebuilds it identically. Derived status itself is never stored; this
	// records only WHEN the (recomputable) status was entered.
	Statuses map[string]*ThingStatus
	// StatusSeq is the LastSeq at which Statuses was last refreshed — the
	// watermark that lets a twice-visited batch boundary skip the second,
	// identical refresh. Not a cache of derived state (nothing to
	// invalidate): between StatusSeq == LastSeq and the next folded event,
	// a refresh has, provably, nothing new to read.
	StatusSeq int64
}

// NewProjection returns the projection of an empty log.
func NewProjection() *Projection {
	return &Projection{
		States:        map[string]*State{},
		Types:         map[string]*ThingType{},
		ResourceTypes: map[string]*ResourceType{},
		Capabilities:  map[string]*Capability{},
		Projects:      map[string]*Project{},
		Things:        map[string]*Thing{},
		Dependencies:  map[string]*Dependency{},
		Requirements:  map[string]*Requirement{},
		Resources:     map[string]*Resource{},
		Allocations:   map[string]*Allocation{},
		Versions:      map[string]int64{},
		Statuses:      map[string]*ThingStatus{},
	}
}

// Clone returns an independent copy the writer can apply a candidate batch
// to without disturbing the published projection.
//
// MAINTENANCE OBLIGATION: every reference-typed field of Projection (and of
// the entity structs) MUST be explicitly deep-copied here — a shared
// reference would let a candidate batch mutate the published projection
// mid-flight, exactly the bug the clone-apply-swap discipline exists to
// prevent. Every new reference-typed field also gets a case in
// TestCloneIsIndependent.
func (p *Projection) Clone() *Projection {
	c := *p
	c.States = cloneMap(p.States, clonePtr)
	c.Types = cloneMap(p.Types, (*ThingType).clone)
	c.ResourceTypes = cloneMap(p.ResourceTypes, (*ResourceType).clone)
	c.Capabilities = cloneMap(p.Capabilities, clonePtr)
	c.Projects = cloneMap(p.Projects, clonePtr)
	c.Things = cloneMap(p.Things, (*Thing).clone)
	c.Dependencies = cloneMap(p.Dependencies, clonePtr)
	c.Requirements = cloneMap(p.Requirements, (*Requirement).clone)
	c.Resources = cloneMap(p.Resources, (*Resource).clone)
	c.Allocations = cloneMap(p.Allocations, clonePtr)
	c.Versions = cloneMap(p.Versions, func(v int64) int64 { return v })
	c.Statuses = cloneMap(p.Statuses, (*ThingStatus).clone)
	return &c
}

// Apply folds one event into the projection, validating envelope hygiene
// (contiguous seq, monotone ts, first-event rule, typed entity prefix) and
// the event's own payload shape. Unknown event types or versions fail
// closed. Cross-entity invariants are NOT checked here — that is
// ValidateBatch's job before append; Apply is the total fold over validated
// logs and errors only on structurally impossible ones. On error the
// projection may be partially updated — callers apply to a Clone and discard
// it on failure.
func (p *Projection) Apply(ev event.Envelope) error {
	payload, err := event.Decode(ev.Type, ev.V, ev.Data)
	if err != nil {
		return fmt.Errorf("domain: event %s (seq %d): %w", ev.ID, ev.Seq, err)
	}
	return p.apply(ev, payload)
}

// apply is Apply after payload decoding — the shared step of the fold and of
// ValidateBatch (which decodes once for validation and reuses the payload).
func (p *Projection) apply(ev event.Envelope, payload event.Payload) error {
	// Batch boundary: the first event of a new batch means the previous
	// batch is complete — re-evaluate derived statuses at its commit ts
	// (§3.3). Fold and ValidateBatch handle the final batch, whose boundary
	// no successor event announces; visiting a boundary twice is a no-op.
	if p.LastBatch != "" && ev.Batch != p.LastBatch {
		p.refreshStatuses(p.LastTS)
	}
	if ev.Seq != p.LastSeq+1 {
		return fmt.Errorf("domain: event %s: seq %d, want %d", ev.ID, ev.Seq, p.LastSeq+1)
	}
	if ev.TS < p.LastTS {
		return fmt.Errorf("domain: event %s: ts %q before predecessor %q", ev.ID, ev.TS, p.LastTS)
	}
	prefix, _ := event.EntityPrefix(ev.Type, ev.V)
	if prefix == "" {
		if ev.Entity != "" {
			return fmt.Errorf("domain: event %s: %s carries entity %q, want none", ev.ID, ev.Type, ev.Entity)
		}
	} else if err := checkEntityID(ev.Entity, prefix); err != nil {
		return fmt.Errorf("domain: event %s: %s entity: %w", ev.ID, ev.Type, err)
	}

	if err := p.fold(ev, payload); err != nil {
		return fmt.Errorf("domain: event %s (seq %d): %w", ev.ID, ev.Seq, err)
	}

	if p.WorkspaceID == "" {
		return fmt.Errorf("domain: first event is %q, must be %s", ev.Type, event.TypeLogInitialized)
	}
	if ev.Entity != "" {
		p.Versions[ev.Entity] = ev.Seq
	}
	p.LastSeq = ev.Seq
	p.LastTS = ev.TS
	p.LastBatch = ev.Batch
	return nil
}

func checkEntityID(id, prefix string) error {
	if id == "" {
		return fmt.Errorf("entity must not be empty")
	}
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix {
		return fmt.Errorf("entity %q must have prefix %q and a non-empty suffix", id, prefix)
	}
	return nil
}

// fold applies the type-specific state change. Existence errors here are the
// "structurally impossible log" cases: a validated log can never produce
// them, so hitting one means the log is corrupt and the fold fails closed.
func (p *Projection) fold(ev event.Envelope, payload event.Payload) error {
	id := ev.Entity
	switch pl := payload.(type) {
	case *event.LogInitialized:
		if ev.Seq != 1 {
			return fmt.Errorf("log.initialized at seq %d: must be the first event", ev.Seq)
		}
		p.WorkspaceID = pl.WorkspaceID
		p.Origin = ev.Origin
	case *event.WriterStarted:
		if p.WorkspaceID == "" {
			return fmt.Errorf("writer.started at seq %d before log.initialized", ev.Seq)
		}
		p.Origin = ev.Origin

	case *event.StateDefined:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		p.States[id] = &State{Name: pl.Name, Semantic: pl.Semantic, Color: pl.Color, Description: pl.Description}
	case *event.StateSuperseded:
		if _, ok := p.States[id]; !ok {
			return fmt.Errorf("state %s does not exist", id)
		}
		p.States[id] = &State{Name: pl.Name, Semantic: pl.Semantic, Color: pl.Color, Description: pl.Description}
	case *event.StateRetracted:
		if _, ok := p.States[id]; !ok {
			return fmt.Errorf("state %s does not exist", id)
		}
		delete(p.States, id)

	case *event.TypeDefined:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		p.Types[id] = &ThingType{Name: pl.Name, Color: pl.Color, Description: pl.Description,
			Fields: foldFields(pl.Fields)}
	case *event.TypeSuperseded:
		if _, ok := p.Types[id]; !ok {
			return fmt.Errorf("type %s does not exist", id)
		}
		p.Types[id] = &ThingType{Name: pl.Name, Color: pl.Color, Description: pl.Description,
			Fields: foldFields(pl.Fields)}
	case *event.TypeRetracted:
		if _, ok := p.Types[id]; !ok {
			return fmt.Errorf("type %s does not exist", id)
		}
		delete(p.Types, id)

	case *event.CapabilityDefined:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		p.Capabilities[id] = &Capability{Name: pl.Name, Description: pl.Description}
	case *event.CapabilitySuperseded:
		if _, ok := p.Capabilities[id]; !ok {
			return fmt.Errorf("capability %s does not exist", id)
		}
		p.Capabilities[id] = &Capability{Name: pl.Name, Description: pl.Description}
	case *event.CapabilityRetracted:
		if _, ok := p.Capabilities[id]; !ok {
			return fmt.Errorf("capability %s does not exist", id)
		}
		delete(p.Capabilities, id)

	case *event.ProjectCreated:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		p.Projects[id] = &Project{Name: pl.Name, Metadata: string(pl.Metadata)}
	case *event.ProjectSuperseded:
		if _, ok := p.Projects[id]; !ok {
			return fmt.Errorf("project %s does not exist", id)
		}
		p.Projects[id] = &Project{Name: pl.Name, Metadata: string(pl.Metadata)}
	case *event.ProjectRetracted:
		if _, ok := p.Projects[id]; !ok {
			return fmt.Errorf("project %s does not exist", id)
		}
		delete(p.Projects, id)

	case *event.ThingCreated:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		if pl.Parent != "" {
			if err := p.attachChild(pl.Parent, id); err != nil {
				return err
			}
		}
		p.Things[id] = &Thing{
			Project: pl.Project, Name: pl.Name, Type: pl.Type,
			Parent: pl.Parent, Metadata: string(pl.Metadata),
		}
	case *event.ThingSuperseded:
		th, ok := p.Things[id]
		if !ok {
			return fmt.Errorf("thing %s does not exist", id)
		}
		if th.Parent != pl.Parent {
			if th.Parent != "" {
				p.detachChild(th.Parent, id)
			}
			if pl.Parent != "" {
				if err := p.attachChild(pl.Parent, id); err != nil {
					return err
				}
			}
		}
		th.Name, th.Type, th.Parent, th.Metadata = pl.Name, pl.Type, pl.Parent, string(pl.Metadata)
	case *event.ThingRetracted:
		th, ok := p.Things[id]
		if !ok {
			return fmt.Errorf("thing %s does not exist", id)
		}
		if th.Parent != "" {
			p.detachChild(th.Parent, id)
		}
		delete(p.Things, id)
		// Retraction ends the status bookkeeping too — eagerly, so the
		// boundary refresh never has to sweep Statuses for dead ids (ids
		// are never reused, so nothing can resurrect the entry).
		delete(p.Statuses, id)
	case *event.ThingStateChanged:
		th, ok := p.Things[id]
		if !ok {
			return fmt.Errorf("thing %s does not exist", id)
		}
		th.State = pl.State

	case *event.DependencyAsserted:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		policy := pl.OnAbandoned
		if policy == "" {
			policy = event.OnAbandonedIgnore
		}
		p.Dependencies[id] = &Dependency{From: pl.From, To: pl.To, OnAbandoned: policy}
	case *event.DependencyRetracted:
		if _, ok := p.Dependencies[id]; !ok {
			return fmt.Errorf("dependency %s does not exist", id)
		}
		delete(p.Dependencies, id)

	case *event.RequirementAsserted:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		p.Requirements[id] = &Requirement{
			Thing: pl.Thing, Quantity: pl.Quantity,
			Capabilities: sortedCopy(pl.Capabilities), Resource: pl.Resource,
			Version: ev.Seq,
		}
	case *event.RequirementSuperseded:
		req, ok := p.Requirements[id]
		if !ok {
			return fmt.Errorf("requirement %s does not exist", id)
		}
		req.Quantity = pl.Quantity
		req.Capabilities = sortedCopy(pl.Capabilities)
		req.Resource = pl.Resource
		req.Version = ev.Seq
	case *event.RequirementRetracted:
		if _, ok := p.Requirements[id]; !ok {
			return fmt.Errorf("requirement %s does not exist", id)
		}
		delete(p.Requirements, id)

	case *event.ResourceTypeDefined:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		p.ResourceTypes[id] = &ResourceType{Name: pl.Name, Color: pl.Color, Description: pl.Description,
			Fields: foldFields(pl.Fields)}
	case *event.ResourceTypeSuperseded:
		if _, ok := p.ResourceTypes[id]; !ok {
			return fmt.Errorf("resource type %s does not exist", id)
		}
		p.ResourceTypes[id] = &ResourceType{Name: pl.Name, Color: pl.Color, Description: pl.Description,
			Fields: foldFields(pl.Fields)}
	case *event.ResourceTypeRetracted:
		if _, ok := p.ResourceTypes[id]; !ok {
			return fmt.Errorf("resource type %s does not exist", id)
		}
		delete(p.ResourceTypes, id)

	case *event.ResourceCreated:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		p.Resources[id] = &Resource{
			Name: pl.Name, Kind: pl.Kind, Named: pl.Named, Capacity: pl.Capacity,
			Type: pl.Type, Metadata: string(pl.Metadata), Capabilities: map[string]struct{}{},
			Available: true,
		}
	case *event.ResourceSuperseded:
		rs, ok := p.Resources[id]
		if !ok {
			return fmt.Errorf("resource %s does not exist", id)
		}
		rs.Name, rs.Kind, rs.Named, rs.Capacity, rs.Type, rs.Metadata =
			pl.Name, pl.Kind, pl.Named, pl.Capacity, pl.Type, string(pl.Metadata)
	case *event.ResourceRetracted:
		if _, ok := p.Resources[id]; !ok {
			return fmt.Errorf("resource %s does not exist", id)
		}
		delete(p.Resources, id)
	case *event.ResourceAvailabilityChanged:
		rs, ok := p.Resources[id]
		if !ok {
			return fmt.Errorf("resource %s does not exist", id)
		}
		rs.Available, rs.Note = pl.Available, pl.Note

	case *event.CapabilityGranted:
		rs, ok := p.Resources[id]
		if !ok {
			return fmt.Errorf("resource %s does not exist", id)
		}
		rs.Capabilities[pl.Capability] = struct{}{}
	case *event.CapabilityRevoked:
		rs, ok := p.Resources[id]
		if !ok {
			return fmt.Errorf("resource %s does not exist", id)
		}
		delete(rs.Capabilities, pl.Capability)

	case *event.AllocationOpened:
		if err := p.mustNotExist(id); err != nil {
			return err
		}
		var reqVersion int64
		if req, ok := p.Requirements[pl.Requirement]; ok {
			reqVersion = req.Version
		}
		p.Allocations[id] = &Allocation{
			Thing: pl.Thing, Resource: pl.Resource, Requirement: pl.Requirement,
			Quantity: pl.Quantity, Open: true, OpenedSeq: ev.Seq,
			RequirementVersion: reqVersion,
		}
	case *event.AllocationClosed:
		al, ok := p.Allocations[id]
		if !ok {
			return fmt.Errorf("allocation %s does not exist", id)
		}
		if !al.Open {
			return fmt.Errorf("allocation %s is already closed", id)
		}
		al.Open = false
		al.ClosedSeq = ev.Seq

	default:
		// Every registered type must be handled; a decodable-but-unhandled
		// event would silently fold to nothing.
		return fmt.Errorf("event type %q v%d has no fold logic", ev.Type, ev.V)
	}
	return nil
}

// attachChild adds child to parent's child set. When the parent was a leaf
// this is the promotion moment (§2.1): its current state ceases to apply and
// is cleared — a later demotion must re-enter a state explicitly, so stale
// pre-composite facts are never resurrected.
func (p *Projection) attachChild(parent, child string) error {
	pt, ok := p.Things[parent]
	if !ok {
		return fmt.Errorf("parent thing %s does not exist", parent)
	}
	if len(pt.Children) == 0 {
		pt.Children = map[string]struct{}{}
		pt.State = ""
	}
	pt.Children[child] = struct{}{}
	return nil
}

func (p *Projection) detachChild(parent, child string) {
	if pt, ok := p.Things[parent]; ok {
		delete(pt.Children, child)
		if len(pt.Children) == 0 {
			pt.Children = nil
		}
	}
}

// mustNotExist guards create/define/assert events: ids are stable and never
// reused (Versions retains retracted ids for exactly this check), so a
// duplicate id is a structurally impossible log.
func (p *Projection) mustNotExist(id string) error {
	if _, ok := p.Versions[id]; ok {
		return fmt.Errorf("entity id %s already exists: ids are never reused", id)
	}
	return nil
}

// foldFields renders a payload's declared metadata fields into the model:
// deep-copied (payload slices never alias the projection) and with Kind
// normalized to its default, like a dependency's OnAbandoned.
func foldFields(fs []event.MetadataField) []MetadataField {
	if len(fs) == 0 {
		return nil
	}
	out := make([]MetadataField, len(fs))
	for i, f := range fs {
		kind := f.Kind
		if kind == "" {
			kind = event.FieldKindText
		}
		out[i] = MetadataField{
			Key: f.Key, Label: f.Label, Kind: kind,
			Options:  append([]string(nil), f.Options...),
			Required: f.Required,
		}
	}
	return out
}

func sortedCopy(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	c := append([]string(nil), ss...)
	sort.Strings(c)
	return c
}

// Fold replays events (in seq order) into a fresh projection. Any violation
// aborts with a nil projection. The trailing refresh closes the final
// batch's status boundary (§3.3) — the fold proper only sees boundaries
// announced by a successor event.
func Fold(events []event.Envelope) (*Projection, error) {
	p := NewProjection()
	for _, ev := range events {
		if err := p.Apply(ev); err != nil {
			return nil, err
		}
	}
	p.refreshStatuses(p.LastTS)
	return p, nil
}
