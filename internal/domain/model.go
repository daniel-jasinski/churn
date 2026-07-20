package domain

import (
	"sort"

	"churn/internal/event"
)

// State is a user-defined named state bound to one semantic (DESIGN.md §2.2).
type State struct {
	Name        string
	Semantic    string
	Color       string
	Description string
}

// MetadataField is one declared metadata field shape on a thing type or
// resource type (§5.3): a UI affordance that drives editing forms. The fold
// stores it verbatim (Kind normalized to its default); nothing in the engine
// ever reads instance metadata against it — the log stays permissive, and
// Required is a form hint, not schema enforcement.
type MetadataField struct {
	Key   string
	Label string
	// Kind is always normalized ("text", "number", "date" or "select"; the
	// fold applies the default).
	Kind string
	// Options are the select choices; empty unless Kind is "select".
	Options  []string
	Required bool
}

// ThingType is a user-defined thing type (§5.3). Fields are its declared
// metadata field shapes.
type ThingType struct {
	Name        string
	Color       string
	Description string
	Fields      []MetadataField
}

// ResourceType is a user-defined resource type (§5.3) — pure categorization
// for boards and reports; the engine attaches no meaning to it. Fields are
// its declared metadata field shapes.
type ResourceType struct {
	Name        string
	Color       string
	Description string
	Fields      []MetadataField
}

// Capability is a user-defined capability tag (§5.3).
type Capability struct {
	Name        string
	Description string
}

// Project is a container for things.
type Project struct {
	Name string
	// Metadata is the canonicalized JSON metadata document; "" if absent.
	Metadata string
}

// Thing is a node in the dependency graph, scoped to a project (§2.1).
// A thing with children is a composite: it carries no requirements, no own
// state (State is cleared on promotion and must be re-entered explicitly
// after demotion), and is never worked directly.
type Thing struct {
	Project string
	Name    string
	Type    string
	// Parent is the containment parent; "" for a root.
	Parent string
	// Metadata is the canonicalized JSON metadata document; "" if absent.
	Metadata string
	// Children is the set of direct containment children.
	Children map[string]struct{}
	// State is the current state id; "" if the thing never transitioned (a
	// never-started thing counts as pending-semantic) or is a composite.
	State string
}

// Dependency is an edge: From depends on To (§2.1). OnAbandoned is always
// normalized ("block" or "ignore"; the fold applies the default).
type Dependency struct {
	From        string
	To          string
	OnAbandoned string
}

// Requirement declares that a leaf thing needs Quantity resource units
// carrying all Capabilities, or the one pinned Resource (§2.4). Requirements
// are versioned: Version is the seq of the event that asserted the current
// attribute set, compared against Allocation.RequirementVersion for the
// "allocations out of step" badge (§2.5).
type Requirement struct {
	Thing    string
	Quantity int
	// Capabilities is sorted; empty iff pinned.
	Capabilities []string
	// Resource is the pin; "" iff capability-based.
	Resource string
	// Version is the seq of the assert/supersede that produced this version.
	Version int64
}

// Pinned reports whether the requirement pins a specific named resource.
func (r *Requirement) Pinned() bool { return r.Resource != "" }

// Resource is a workspace-global entity work is done with (§2.3).
type Resource struct {
	Name  string
	Kind  string
	Named bool
	// Capacity is the nominal capacity (>= 1; exactly 1 when Named).
	Capacity int
	// Type is the optional resource type id (rt_); "" if untyped. Display
	// categorization only — matching stays capability-based (§2.4).
	Type string
	// Metadata is the canonicalized JSON metadata document; "" if absent.
	Metadata string
	// Capabilities is the set of granted capability ids.
	Capabilities map[string]struct{}
	// Available is the availability toggle; unavailable counts as capacity 0.
	Available bool
	// Note annotates the current availability ("maintenance", "on leave").
	Note string
}

// EffectiveCapacity is the capacity the resource currently offers:
// 0 while unavailable, Capacity otherwise.
func (r *Resource) EffectiveCapacity() int {
	if !r.Available {
		return 0
	}
	return r.Capacity
}

// Allocation links a thing to a resource satisfying one of the thing's
// requirements (§2.5). Closed allocations are kept forever — they are the
// work history per resource and per thing.
type Allocation struct {
	Thing       string
	Resource    string
	Requirement string
	Quantity    int
	// Open is false once the allocation is closed.
	Open bool
	// OpenedSeq / ClosedSeq are the seqs of the opening and closing events
	// (ClosedSeq is 0 while open).
	OpenedSeq int64
	ClosedSeq int64
	// RequirementVersion is Requirement.Version at open time — the input to
	// the "allocations out of step with requirements" comparison (§2.5).
	RequirementVersion int64
}

// Note is a free-text annotation (a comment) attached to a thing. Author,
// CreatedTS and CreatedSeq come from the adding event's envelope; EditedTS and
// EditedSeq track the last supersession ("" / 0 while never edited). Notes are
// plain facts — the engine derives nothing from them.
type Note struct {
	Thing      string
	Body       string
	Author     string
	CreatedTS  string
	CreatedSeq int64
	EditedTS   string
	EditedSeq  int64
}

// IsComposite reports whether the thing with the given id has children.
// Unknown ids are not composite.
func (p *Projection) IsComposite(id string) bool {
	th, ok := p.Things[id]
	return ok && len(th.Children) > 0
}

// SemanticOf returns the state semantic of a thing: the semantic of its
// current state, or pending for a thing that never transitioned. Composites
// have no semantic of their own; callers must not ask (rollup is computed,
// M3). For robustness a composite (cleared State) also reads pending.
func (p *Projection) SemanticOf(th *Thing) string {
	if th.State == "" {
		return event.SemanticPending
	}
	if st, ok := p.States[th.State]; ok {
		return st.Semantic
	}
	// Unreachable in a validated log: state retraction is blocked while
	// occupied. Read as pending rather than invent a semantic.
	return event.SemanticPending
}

// Leaves returns the sorted leaf things of the subtree rooted at id (id
// itself if it is a leaf). Unknown ids yield nil.
func (p *Projection) Leaves(id string) []string {
	th, ok := p.Things[id]
	if !ok {
		return nil
	}
	if len(th.Children) == 0 {
		return []string{id}
	}
	var out []string
	for _, c := range sortedKeys(th.Children) {
		out = append(out, p.Leaves(c)...)
	}
	sort.Strings(out)
	return out
}

// NotesOf returns the ids of notes attached to a thing, sorted. Note ids are
// ULIDs, so ascending id order is chronological (oldest first).
func (p *Projection) NotesOf(thing string) []string {
	var out []string
	for id, nt := range p.Notes {
		if nt.Thing == thing {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// OpenAllocationsOf returns the ids of open allocations of a thing, sorted.
func (p *Projection) OpenAllocationsOf(thing string) []string {
	var out []string
	for id, al := range p.Allocations {
		if al.Open && al.Thing == thing {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// AllocatedQuantity returns the total quantity of open allocations on a
// resource.
func (p *Projection) AllocatedQuantity(resource string) int {
	total := 0
	for _, al := range p.Allocations {
		if al.Open && al.Resource == resource {
			total += al.Quantity
		}
	}
	return total
}

// Version returns the entity's version: the seq of the last event whose
// entity column was this id, or 0 if the id was never touched.
func (p *Projection) Version(id string) int64 {
	return p.Versions[id]
}

// sortedKeys returns the keys of a string-keyed map in sorted order —
// the deterministic-iteration helper used anywhere order can leak.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// clone helpers — one per entity type, deep-copying every reference field.

func cloneMap[V any](m map[string]V, cloneV func(V) V) map[string]V {
	if m == nil {
		return nil
	}
	c := make(map[string]V, len(m))
	for k, v := range m {
		c[k] = cloneV(v)
	}
	return c
}

func cloneSet(s map[string]struct{}) map[string]struct{} {
	if s == nil {
		return nil
	}
	c := make(map[string]struct{}, len(s))
	for k := range s {
		c[k] = struct{}{}
	}
	return c
}

func cloneFields(fs []MetadataField) []MetadataField {
	if fs == nil {
		return nil
	}
	c := make([]MetadataField, len(fs))
	for i, f := range fs {
		c[i] = f
		c[i].Options = append([]string(nil), f.Options...)
	}
	return c
}

func (t *ThingType) clone() *ThingType {
	c := *t
	c.Fields = cloneFields(t.Fields)
	return &c
}

func (r *ResourceType) clone() *ResourceType {
	c := *r
	c.Fields = cloneFields(r.Fields)
	return &c
}

func (t *Thing) clone() *Thing {
	c := *t
	c.Children = cloneSet(t.Children)
	return &c
}

func (r *Requirement) clone() *Requirement {
	c := *r
	c.Capabilities = append([]string(nil), r.Capabilities...)
	return &c
}

func (r *Resource) clone() *Resource {
	c := *r
	c.Capabilities = cloneSet(r.Capabilities)
	return &c
}

func clonePtr[T any](p *T) *T {
	c := *p
	return &c
}
