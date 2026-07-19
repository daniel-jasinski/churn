// Event catalog (DESIGN.md §5.2): every domain event type with its v1
// payload schema. Payload Validate methods check shape only — field presence,
// enum membership, id prefixes, local arithmetic rules (a pin's quantity is
// 1, named ⇒ capacity 1). Referential and cross-entity rules live in the
// domain package.
//
// Entity ids use typed prefixes (st_, ty_, cap_, pr_, th_, dep_, req_, rt_,
// rs_, al_) followed by a caller-minted unique suffix (a ULID in production; the
// suffix format is deliberately not validated so readers stay permissive).
// The envelope's entity column carries the id of the entity an event is
// about; payloads never repeat it.

package event

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Typed id prefixes (DESIGN.md §5.2 "everything addressable has a stable id").
const (
	PrefixWorkspace    = "ws_"
	PrefixWriter       = "wr_"
	PrefixState        = "st_"
	PrefixType         = "ty_"
	PrefixCapability   = "cap_"
	PrefixProject      = "pr_"
	PrefixThing        = "th_"
	PrefixDependency   = "dep_"
	PrefixRequirement  = "req_"
	PrefixResourceType = "rt_"
	PrefixResource     = "rs_"
	PrefixAllocation   = "al_"
)

// The closed set of state semantics (DESIGN.md §2.2).
const (
	SemanticPending   = "pending"
	SemanticActive    = "active"
	SemanticPaused    = "paused"
	SemanticSatisfied = "satisfied"
	SemanticAbandoned = "abandoned"
)

// Semantics is the closed set of state semantics, in a fixed order.
var Semantics = []string{
	SemanticPending, SemanticActive, SemanticPaused,
	SemanticSatisfied, SemanticAbandoned,
}

// Dependency on_abandoned policies (DESIGN.md §2.2). The empty string in an
// asserted payload means the default, OnAbandonedIgnore (unblock with a
// warning badge).
const (
	OnAbandonedBlock  = "block"
	OnAbandonedIgnore = "ignore"
)

// Resource kinds (DESIGN.md §2.3). Consumable is reserved for a later phase;
// the schema accepts it so stock support arrives without migration.
const (
	KindReusable   = "reusable"
	KindConsumable = "consumable"
)

// Metadata field kinds (DESIGN.md §5.3): the editor-form widgets a declared
// metadata field may ask for. The empty string in a payload means the
// default, FieldKindText.
const (
	FieldKindText   = "text"
	FieldKindNumber = "number"
	FieldKindDate   = "date"
	FieldKindSelect = "select"
)

// FieldKinds is the closed set of metadata field kinds, in a fixed order.
var FieldKinds = []string{FieldKindText, FieldKindNumber, FieldKindDate, FieldKindSelect}

// Event type names of the domain catalog.
const (
	TypeStateDefined    = "state.defined"
	TypeStateSuperseded = "state.superseded"
	TypeStateRetracted  = "state.retracted"

	TypeTypeDefined    = "type.defined"
	TypeTypeSuperseded = "type.superseded"
	TypeTypeRetracted  = "type.retracted"

	TypeCapabilityDefined    = "capability.defined"
	TypeCapabilitySuperseded = "capability.superseded"
	TypeCapabilityRetracted  = "capability.retracted"

	TypeProjectCreated    = "project.created"
	TypeProjectSuperseded = "project.superseded"
	TypeProjectRetracted  = "project.retracted"

	TypeThingCreated      = "thing.created"
	TypeThingSuperseded   = "thing.superseded"
	TypeThingRetracted    = "thing.retracted"
	TypeThingStateChanged = "thing.state_changed"

	TypeDependencyAsserted  = "dependency.asserted"
	TypeDependencyRetracted = "dependency.retracted"

	TypeRequirementAsserted   = "requirement.asserted"
	TypeRequirementSuperseded = "requirement.superseded"
	TypeRequirementRetracted  = "requirement.retracted"

	TypeResourceTypeDefined    = "resourcetype.defined"
	TypeResourceTypeSuperseded = "resourcetype.superseded"
	TypeResourceTypeRetracted  = "resourcetype.retracted"

	TypeResourceCreated             = "resource.created"
	TypeResourceSuperseded          = "resource.superseded"
	TypeResourceRetracted           = "resource.retracted"
	TypeResourceAvailabilityChanged = "resource.availability_changed"

	TypeCapabilityGranted = "capability.granted"
	TypeCapabilityRevoked = "capability.revoked"

	TypeAllocationOpened = "allocation.opened"
	TypeAllocationClosed = "allocation.closed"
)

func init() {
	reg := func(typ string, prefix string, dec func() Payload) {
		registry[key{typ, 1}] = entry{dec: dec, entityPrefix: prefix}
	}
	reg(TypeStateDefined, PrefixState, func() Payload { return new(StateDefined) })
	reg(TypeStateSuperseded, PrefixState, func() Payload { return new(StateSuperseded) })
	reg(TypeStateRetracted, PrefixState, func() Payload { return new(StateRetracted) })
	reg(TypeTypeDefined, PrefixType, func() Payload { return new(TypeDefined) })
	reg(TypeTypeSuperseded, PrefixType, func() Payload { return new(TypeSuperseded) })
	reg(TypeTypeRetracted, PrefixType, func() Payload { return new(TypeRetracted) })
	reg(TypeCapabilityDefined, PrefixCapability, func() Payload { return new(CapabilityDefined) })
	reg(TypeCapabilitySuperseded, PrefixCapability, func() Payload { return new(CapabilitySuperseded) })
	reg(TypeCapabilityRetracted, PrefixCapability, func() Payload { return new(CapabilityRetracted) })
	reg(TypeProjectCreated, PrefixProject, func() Payload { return new(ProjectCreated) })
	reg(TypeProjectSuperseded, PrefixProject, func() Payload { return new(ProjectSuperseded) })
	reg(TypeProjectRetracted, PrefixProject, func() Payload { return new(ProjectRetracted) })
	reg(TypeThingCreated, PrefixThing, func() Payload { return new(ThingCreated) })
	reg(TypeThingSuperseded, PrefixThing, func() Payload { return new(ThingSuperseded) })
	reg(TypeThingRetracted, PrefixThing, func() Payload { return new(ThingRetracted) })
	reg(TypeThingStateChanged, PrefixThing, func() Payload { return new(ThingStateChanged) })
	reg(TypeDependencyAsserted, PrefixDependency, func() Payload { return new(DependencyAsserted) })
	reg(TypeDependencyRetracted, PrefixDependency, func() Payload { return new(DependencyRetracted) })
	reg(TypeRequirementAsserted, PrefixRequirement, func() Payload { return new(RequirementAsserted) })
	reg(TypeRequirementSuperseded, PrefixRequirement, func() Payload { return new(RequirementSuperseded) })
	reg(TypeRequirementRetracted, PrefixRequirement, func() Payload { return new(RequirementRetracted) })
	reg(TypeResourceTypeDefined, PrefixResourceType, func() Payload { return new(ResourceTypeDefined) })
	reg(TypeResourceTypeSuperseded, PrefixResourceType, func() Payload { return new(ResourceTypeSuperseded) })
	reg(TypeResourceTypeRetracted, PrefixResourceType, func() Payload { return new(ResourceTypeRetracted) })
	reg(TypeResourceCreated, PrefixResource, func() Payload { return new(ResourceCreated) })
	reg(TypeResourceSuperseded, PrefixResource, func() Payload { return new(ResourceSuperseded) })
	reg(TypeResourceRetracted, PrefixResource, func() Payload { return new(ResourceRetracted) })
	reg(TypeResourceAvailabilityChanged, PrefixResource, func() Payload { return new(ResourceAvailabilityChanged) })
	reg(TypeCapabilityGranted, PrefixResource, func() Payload { return new(CapabilityGranted) })
	reg(TypeCapabilityRevoked, PrefixResource, func() Payload { return new(CapabilityRevoked) })
	reg(TypeAllocationOpened, PrefixAllocation, func() Payload { return new(AllocationOpened) })
	reg(TypeAllocationClosed, PrefixAllocation, func() Payload { return new(AllocationClosed) })
}

// checkID validates a required entity-id field: non-empty, correct typed
// prefix, non-empty suffix.
func checkID(field, id, prefix string) error {
	if id == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if !strings.HasPrefix(id, prefix) || len(id) == len(prefix) {
		return fmt.Errorf("%s %q must have prefix %q and a non-empty suffix", field, id, prefix)
	}
	return nil
}

// checkOptID validates an optional entity-id field: empty, or a valid id.
func checkOptID(field, id, prefix string) error {
	if id == "" {
		return nil
	}
	return checkID(field, id, prefix)
}

func validSemantic(s string) bool {
	switch s {
	case SemanticPending, SemanticActive, SemanticPaused, SemanticSatisfied, SemanticAbandoned:
		return true
	}
	return false
}

// ── vocabulary: states ──

// StateDefined declares a named state bound to one semantic (v1).
type StateDefined struct {
	Name        string `json:"name"`
	Semantic    string `json:"semantic"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

// Validate implements the payload contract.
func (p *StateDefined) Validate() error { return validateStateShape(p.Name, p.Semantic) }

// StateSuperseded is the full replacement of a state's mutable attribute set
// (v1). Semantic immutability while occupied is a domain rule.
type StateSuperseded struct {
	Name        string `json:"name"`
	Semantic    string `json:"semantic"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

// Validate implements the payload contract.
func (p *StateSuperseded) Validate() error { return validateStateShape(p.Name, p.Semantic) }

func validateStateShape(name, semantic string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if !validSemantic(semantic) {
		return fmt.Errorf("semantic %q is not one of %v", semantic, Semantics)
	}
	return nil
}

// StateRetracted tombstones a state (v1).
type StateRetracted struct{}

// Validate implements the payload contract.
func (p *StateRetracted) Validate() error { return nil }

// ── vocabulary: thing types ──

// MetadataField is one declared metadata field shape on a thing type or
// resource type (DESIGN.md §5.3): a UI affordance that drives editing forms.
// The engine computes nothing off metadata and the log stays permissive —
// instance metadata is never validated against declarations; Required is a
// form hint, not schema enforcement.
type MetadataField struct {
	// Key is the metadata key the field edits; required, unique within the
	// declaring type.
	Key string `json:"key"`
	// Label is the optional display label (the key shows otherwise).
	Label string `json:"label,omitempty"`
	// Kind selects the editor widget; empty means FieldKindText.
	Kind string `json:"kind,omitempty"`
	// Options are the choices of a select field — non-empty strings, legal
	// and required exactly when Kind is FieldKindSelect.
	Options []string `json:"options,omitempty"`
	// Required marks the field as expected by the form — a hint only.
	Required bool `json:"required,omitempty"`
}

// validateFields shape-checks a declared metadata field list: non-empty
// unique keys, a known kind, and options present iff the kind is select.
func validateFields(fields []MetadataField) error {
	seen := make(map[string]bool, len(fields))
	for i, f := range fields {
		if f.Key == "" {
			return fmt.Errorf("fields[%d]: key must not be empty", i)
		}
		if seen[f.Key] {
			return fmt.Errorf("fields[%d]: key %q declared twice", i, f.Key)
		}
		seen[f.Key] = true
		switch f.Kind {
		case "", FieldKindText, FieldKindNumber, FieldKindDate:
			if len(f.Options) > 0 {
				return fmt.Errorf("fields[%d] (%s): options are only legal with kind %q", i, f.Key, FieldKindSelect)
			}
		case FieldKindSelect:
			if len(f.Options) == 0 {
				return fmt.Errorf("fields[%d] (%s): kind %q requires options", i, f.Key, FieldKindSelect)
			}
			for j, o := range f.Options {
				if o == "" {
					return fmt.Errorf("fields[%d] (%s): options[%d] must not be empty", i, f.Key, j)
				}
			}
		default:
			return fmt.Errorf("fields[%d] (%s): kind %q is not one of %v", i, f.Key, f.Kind, FieldKinds)
		}
	}
	return nil
}

// TypeDefined declares a thing type (v1). Fields is the optional declared
// metadata field list — additive, so v stays 1 and older events simply lack
// it.
type TypeDefined struct {
	Name        string          `json:"name"`
	Color       string          `json:"color,omitempty"`
	Description string          `json:"description,omitempty"`
	Fields      []MetadataField `json:"fields,omitempty"`
}

// Validate implements the payload contract.
func (p *TypeDefined) Validate() error {
	if err := requireName(p.Name); err != nil {
		return err
	}
	return validateFields(p.Fields)
}

// TypeSuperseded is the full replacement of a thing type's attributes,
// declared metadata fields included (v1).
type TypeSuperseded struct {
	Name        string          `json:"name"`
	Color       string          `json:"color,omitempty"`
	Description string          `json:"description,omitempty"`
	Fields      []MetadataField `json:"fields,omitempty"`
}

// Validate implements the payload contract.
func (p *TypeSuperseded) Validate() error {
	if err := requireName(p.Name); err != nil {
		return err
	}
	return validateFields(p.Fields)
}

// TypeRetracted tombstones a thing type (v1).
type TypeRetracted struct{}

// Validate implements the payload contract.
func (p *TypeRetracted) Validate() error { return nil }

// ── vocabulary: capabilities ──

// CapabilityDefined declares a capability tag (v1).
type CapabilityDefined struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Validate implements the payload contract.
func (p *CapabilityDefined) Validate() error { return requireName(p.Name) }

// CapabilitySuperseded is the full replacement of a capability's attributes (v1).
type CapabilitySuperseded struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Validate implements the payload contract.
func (p *CapabilitySuperseded) Validate() error { return requireName(p.Name) }

// CapabilityRetracted tombstones a capability (v1).
type CapabilityRetracted struct{}

// Validate implements the payload contract.
func (p *CapabilityRetracted) Validate() error { return nil }

func requireName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	return nil
}

// ── projects ──

// ProjectCreated creates a project (v1).
type ProjectCreated struct {
	Name string `json:"name"`
	// Metadata is arbitrary JSON, schema-free by design; may be absent.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Validate implements the payload contract.
func (p *ProjectCreated) Validate() error { return requireName(p.Name) }

// ProjectSuperseded is the full replacement of a project's mutable attributes (v1).
type ProjectSuperseded struct {
	Name     string          `json:"name"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Validate implements the payload contract.
func (p *ProjectSuperseded) Validate() error { return requireName(p.Name) }

// ProjectRetracted tombstones a project (v1).
type ProjectRetracted struct{}

// Validate implements the payload contract.
func (p *ProjectRetracted) Validate() error { return nil }

// ── things ──

// ThingCreated creates a thing in a project (v1). The project is immutable
// for the thing's lifetime; parent is optional containment.
type ThingCreated struct {
	Project  string          `json:"project"`
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Parent   string          `json:"parent,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Validate implements the payload contract.
func (p *ThingCreated) Validate() error {
	if err := checkID("project", p.Project, PrefixProject); err != nil {
		return err
	}
	if err := requireName(p.Name); err != nil {
		return err
	}
	if err := checkID("type", p.Type, PrefixType); err != nil {
		return err
	}
	return checkOptID("parent", p.Parent, PrefixThing)
}

// Refs implements Referencer.
func (p *ThingCreated) Refs() []Ref {
	rs := []Ref{{p.Project, "project"}, {p.Type, "type"}}
	if p.Parent != "" {
		rs = append(rs, Ref{p.Parent, "parent"})
	}
	return rs
}

// ThingSuperseded is the full replacement of a thing's mutable attribute set
// (name, type, parent, metadata) — the project is immutable (v1).
type ThingSuperseded struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Parent   string          `json:"parent,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Validate implements the payload contract.
func (p *ThingSuperseded) Validate() error {
	if err := requireName(p.Name); err != nil {
		return err
	}
	if err := checkID("type", p.Type, PrefixType); err != nil {
		return err
	}
	return checkOptID("parent", p.Parent, PrefixThing)
}

// Refs implements Referencer.
func (p *ThingSuperseded) Refs() []Ref {
	rs := []Ref{{p.Type, "type"}}
	if p.Parent != "" {
		rs = append(rs, Ref{p.Parent, "parent"})
	}
	return rs
}

// ThingRetracted tombstones a thing (v1).
type ThingRetracted struct{}

// Validate implements the payload contract.
func (p *ThingRetracted) Validate() error { return nil }

// ThingStateChanged moves a leaf thing into a defined state (v1).
type ThingStateChanged struct {
	State string `json:"state"`
}

// Validate implements the payload contract.
func (p *ThingStateChanged) Validate() error {
	return checkID("state", p.State, PrefixState)
}

// Refs implements Referencer.
func (p *ThingStateChanged) Refs() []Ref { return []Ref{{p.State, "state"}} }

// ── dependencies ──

// DependencyAsserted creates a dependency edge: From depends on To (v1).
// OnAbandoned is the per-edge policy for abandoned dependencies; empty means
// the default, OnAbandonedIgnore (unblock with a warning badge, §2.2).
type DependencyAsserted struct {
	From        string `json:"from"`
	To          string `json:"to"`
	OnAbandoned string `json:"on_abandoned,omitempty"`
}

// Validate implements the payload contract.
func (p *DependencyAsserted) Validate() error {
	if err := checkID("from", p.From, PrefixThing); err != nil {
		return err
	}
	if err := checkID("to", p.To, PrefixThing); err != nil {
		return err
	}
	switch p.OnAbandoned {
	case "", OnAbandonedBlock, OnAbandonedIgnore:
		return nil
	}
	return fmt.Errorf("on_abandoned %q must be %q or %q", p.OnAbandoned, OnAbandonedBlock, OnAbandonedIgnore)
}

// Refs implements Referencer.
func (p *DependencyAsserted) Refs() []Ref {
	return []Ref{{p.From, "from"}, {p.To, "to"}}
}

// DependencyRetracted tombstones a dependency edge (v1).
type DependencyRetracted struct{}

// Validate implements the payload contract.
func (p *DependencyRetracted) Validate() error { return nil }

// ── requirements ──

// RequirementAsserted declares a resource requirement of a leaf thing (v1):
// Quantity units of either any resource carrying all Capabilities, or the one
// pinned Resource (exactly one of the two forms; a pin's quantity is 1).
type RequirementAsserted struct {
	Thing        string   `json:"thing"`
	Quantity     int      `json:"quantity"`
	Capabilities []string `json:"capabilities,omitempty"`
	Resource     string   `json:"resource,omitempty"`
}

// Validate implements the payload contract.
func (p *RequirementAsserted) Validate() error {
	if err := checkID("thing", p.Thing, PrefixThing); err != nil {
		return err
	}
	return validateRequirementShape(p.Quantity, p.Capabilities, p.Resource)
}

// Refs implements Referencer.
func (p *RequirementAsserted) Refs() []Ref {
	rs := []Ref{{p.Thing, "thing"}}
	for _, c := range p.Capabilities {
		rs = append(rs, Ref{c, "capability"})
	}
	if p.Resource != "" {
		rs = append(rs, Ref{p.Resource, "pin"})
	}
	return rs
}

// RequirementSuperseded is the full replacement of a requirement's mutable
// attribute set (quantity, capabilities | pin) — the owning thing is
// immutable (v1).
type RequirementSuperseded struct {
	Quantity     int      `json:"quantity"`
	Capabilities []string `json:"capabilities,omitempty"`
	Resource     string   `json:"resource,omitempty"`
}

// Validate implements the payload contract.
func (p *RequirementSuperseded) Validate() error {
	return validateRequirementShape(p.Quantity, p.Capabilities, p.Resource)
}

// Refs implements Referencer.
func (p *RequirementSuperseded) Refs() []Ref {
	var rs []Ref
	for _, c := range p.Capabilities {
		rs = append(rs, Ref{c, "capability"})
	}
	if p.Resource != "" {
		rs = append(rs, Ref{p.Resource, "pin"})
	}
	return rs
}

func validateRequirementShape(quantity int, capabilities []string, resource string) error {
	pinned := resource != ""
	if pinned == (len(capabilities) > 0) {
		return fmt.Errorf("exactly one of capabilities and resource must be set")
	}
	if pinned {
		if err := checkID("resource", resource, PrefixResource); err != nil {
			return err
		}
		if quantity != 1 {
			return fmt.Errorf("a pinned requirement's quantity must be 1, got %d", quantity)
		}
		return nil
	}
	seen := make(map[string]bool, len(capabilities))
	for _, c := range capabilities {
		if err := checkID("capability", c, PrefixCapability); err != nil {
			return err
		}
		if seen[c] {
			return fmt.Errorf("capability %q listed twice", c)
		}
		seen[c] = true
	}
	if quantity < 1 {
		return fmt.Errorf("quantity must be >= 1, got %d", quantity)
	}
	return nil
}

// RequirementRetracted tombstones a requirement (v1).
type RequirementRetracted struct{}

// Validate implements the payload contract.
func (p *RequirementRetracted) Validate() error { return nil }

// ── vocabulary: resource types ──

// ResourceTypeDefined declares a resource type (v1). Fields is the optional
// declared metadata field list — additive, so v stays 1 and older events
// simply lack it.
type ResourceTypeDefined struct {
	Name        string          `json:"name"`
	Color       string          `json:"color,omitempty"`
	Description string          `json:"description,omitempty"`
	Fields      []MetadataField `json:"fields,omitempty"`
}

// Validate implements the payload contract.
func (p *ResourceTypeDefined) Validate() error {
	if err := requireName(p.Name); err != nil {
		return err
	}
	return validateFields(p.Fields)
}

// ResourceTypeSuperseded is the full replacement of a resource type's
// attributes, declared metadata fields included (v1).
type ResourceTypeSuperseded struct {
	Name        string          `json:"name"`
	Color       string          `json:"color,omitempty"`
	Description string          `json:"description,omitempty"`
	Fields      []MetadataField `json:"fields,omitempty"`
}

// Validate implements the payload contract.
func (p *ResourceTypeSuperseded) Validate() error {
	if err := requireName(p.Name); err != nil {
		return err
	}
	return validateFields(p.Fields)
}

// ResourceTypeRetracted tombstones a resource type (v1).
type ResourceTypeRetracted struct{}

// Validate implements the payload contract.
func (p *ResourceTypeRetracted) Validate() error { return nil }

// ── resources ──

// ResourceCreated creates a workspace-global resource (v1). Capabilities are
// granted by separate capability.granted events; availability defaults to
// true and changes via resource.availability_changed. Type is an optional
// resource type reference (categorization only — the engine attaches no
// meaning to it); the field is additive, so v stays 1 and older events
// simply lack it.
type ResourceCreated struct {
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Named    bool            `json:"named"`
	Capacity int             `json:"capacity"`
	Type     string          `json:"type,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Validate implements the payload contract.
func (p *ResourceCreated) Validate() error {
	return validateResourceShape(p.Name, p.Kind, p.Named, p.Capacity, p.Type)
}

// Refs implements Referencer.
func (p *ResourceCreated) Refs() []Ref {
	if p.Type == "" {
		return nil
	}
	return []Ref{{p.Type, "type"}}
}

// ResourceSuperseded is the full replacement of a resource's mutable
// attribute set (v1). Capability grants and availability are separate facts
// and survive supersession. Type is the same optional, additive resource
// type reference as on ResourceCreated.
type ResourceSuperseded struct {
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Named    bool            `json:"named"`
	Capacity int             `json:"capacity"`
	Type     string          `json:"type,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Validate implements the payload contract.
func (p *ResourceSuperseded) Validate() error {
	return validateResourceShape(p.Name, p.Kind, p.Named, p.Capacity, p.Type)
}

// Refs implements Referencer.
func (p *ResourceSuperseded) Refs() []Ref {
	if p.Type == "" {
		return nil
	}
	return []Ref{{p.Type, "type"}}
}

func validateResourceShape(name, kind string, named bool, capacity int, typ string) error {
	if err := requireName(name); err != nil {
		return err
	}
	if kind != KindReusable && kind != KindConsumable {
		return fmt.Errorf("kind %q must be %q or %q", kind, KindReusable, KindConsumable)
	}
	if capacity < 1 {
		return fmt.Errorf("capacity must be >= 1, got %d", capacity)
	}
	if named && capacity != 1 {
		return fmt.Errorf("a named resource's capacity must be 1, got %d", capacity)
	}
	return checkOptID("type", typ, PrefixResourceType)
}

// ResourceRetracted tombstones a resource (v1).
type ResourceRetracted struct{}

// Validate implements the payload contract.
func (p *ResourceRetracted) Validate() error { return nil }

// ResourceAvailabilityChanged toggles a resource's availability (v1).
// Unavailable resources count as capacity 0; open allocations are never
// force-closed (§2.5 "reality wins").
type ResourceAvailabilityChanged struct {
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`
}

// Validate implements the payload contract.
func (p *ResourceAvailabilityChanged) Validate() error { return nil }

// ── capability grants ──

// CapabilityGranted adds a capability to the resource in the envelope's
// entity column (v1).
type CapabilityGranted struct {
	Capability string `json:"capability"`
}

// Validate implements the payload contract.
func (p *CapabilityGranted) Validate() error {
	return checkID("capability", p.Capability, PrefixCapability)
}

// Refs implements Referencer.
func (p *CapabilityGranted) Refs() []Ref { return []Ref{{p.Capability, "capability"}} }

// CapabilityRevoked removes a capability from the resource in the envelope's
// entity column (v1).
type CapabilityRevoked struct {
	Capability string `json:"capability"`
}

// Validate implements the payload contract.
func (p *CapabilityRevoked) Validate() error {
	return checkID("capability", p.Capability, PrefixCapability)
}

// Refs implements Referencer.
func (p *CapabilityRevoked) Refs() []Ref { return []Ref{{p.Capability, "capability"}} }

// ── allocations ──

// AllocationOpened opens an allocation: Quantity units of Resource satisfying
// Requirement of Thing (v1). The matching's result is recorded, not just its
// net effect (§2.5).
type AllocationOpened struct {
	Thing       string `json:"thing"`
	Resource    string `json:"resource"`
	Quantity    int    `json:"quantity"`
	Requirement string `json:"requirement"`
}

// Validate implements the payload contract.
func (p *AllocationOpened) Validate() error {
	if err := checkID("thing", p.Thing, PrefixThing); err != nil {
		return err
	}
	if err := checkID("resource", p.Resource, PrefixResource); err != nil {
		return err
	}
	if err := checkID("requirement", p.Requirement, PrefixRequirement); err != nil {
		return err
	}
	if p.Quantity < 1 {
		return fmt.Errorf("quantity must be >= 1, got %d", p.Quantity)
	}
	return nil
}

// Refs implements Referencer.
func (p *AllocationOpened) Refs() []Ref {
	return []Ref{{p.Thing, "thing"}, {p.Resource, "resource"}, {p.Requirement, "requirement"}}
}

// AllocationClosed closes an open allocation (v1).
type AllocationClosed struct{}

// Validate implements the payload contract.
func (p *AllocationClosed) Validate() error { return nil }
