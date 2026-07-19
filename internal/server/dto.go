package server

import (
	"encoding/json"
	"sort"

	"churn/internal/analytics"
	"churn/internal/domain"
	"churn/internal/match"
)

// DTOs — the JSON shapes of the read side. Every list is sorted by id
// unless documented otherwise; "version" is the §5.2 entity version (seq of
// the last touching event), the value clients echo back via If-Match or
// expected_versions.

type projectDTO struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Version  int64           `json:"version"`
}

type badgesDTO struct {
	AbandonedDependency     bool `json:"abandoned_dependency"`
	FinishedUnsatisfiedDeps bool `json:"finished_unsatisfied_deps"`
	OverAllocated           bool `json:"over_allocated"`
	AllocationsOutOfStep    bool `json:"allocations_out_of_step"`
}

type progressDTO struct {
	Satisfied    int    `json:"satisfied"`
	Total        int    `json:"total"`
	HasAbandoned bool   `json:"has_abandoned"`
	Display      string `json:"display"`
}

type thingDTO struct {
	ID       string          `json:"id"`
	Project  string          `json:"project"`
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Parent   string          `json:"parent,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// State is the current state id; empty for composites and never-started
	// leaves.
	State     string   `json:"state,omitempty"`
	Composite bool     `json:"composite"`
	Children  []string `json:"children,omitempty"`
	// Status is the derived §2.2/§2.1 status; badges and resumable_now
	// per §2.2, progress (composites only) per §3.5.
	Status       string       `json:"status"`
	HasAbandoned bool         `json:"has_abandoned"`
	ResumableNow bool         `json:"resumable_now"`
	Badges       badgesDTO    `json:"badges"`
	Progress     *progressDTO `json:"progress,omitempty"`
	Version      int64        `json:"version"`
}

type dependencyDTO struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	OnAbandoned string `json:"on_abandoned"`
	// Satisfied is the current §2.1 edge verdict; AbandonedTolerated is true
	// when it is satisfied only because the ignore policy tolerates abandoned
	// target leaves.
	Satisfied          bool  `json:"satisfied"`
	AbandonedTolerated bool  `json:"abandoned_tolerated"`
	Version            int64 `json:"version"`
}

type requirementDTO struct {
	ID           string   `json:"id"`
	Thing        string   `json:"thing"`
	Quantity     int      `json:"quantity"`
	Capabilities []string `json:"capabilities,omitempty"`
	Resource     string   `json:"resource,omitempty"`
	Version      int64    `json:"version"`
}

type resourceDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Named    bool   `json:"named"`
	Capacity int    `json:"capacity"`
	// Type is the optional resource type id (rt_); omitted when untyped.
	Type         string          `json:"type,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Available    bool            `json:"available"`
	Note         string          `json:"note,omitempty"`
	// EffectiveCapacity is 0 while unavailable; Free is
	// max(0, effective − allocated) (§2.5 clamps).
	EffectiveCapacity int   `json:"effective_capacity"`
	Allocated         int   `json:"allocated"`
	Free              int   `json:"free"`
	OverAllocated     bool  `json:"over_allocated"`
	Version           int64 `json:"version"`
}

type stateDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Semantic    string `json:"semantic"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
	Version     int64  `json:"version"`
}

// metadataFieldDTO is one declared metadata field shape (§5.3) — a form
// affordance, never enforced against instance metadata. Kind is always the
// normalized value ("text", "number", "date", "select").
type metadataFieldDTO struct {
	Key      string   `json:"key"`
	Label    string   `json:"label,omitempty"`
	Kind     string   `json:"kind"`
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
}

func buildMetadataFieldDTOs(fs []domain.MetadataField) []metadataFieldDTO {
	if len(fs) == 0 {
		return nil
	}
	out := make([]metadataFieldDTO, len(fs))
	for i, f := range fs {
		out[i] = metadataFieldDTO{Key: f.Key, Label: f.Label, Kind: f.Kind,
			Options: append([]string(nil), f.Options...), Required: f.Required}
	}
	return out
}

type typeDTO struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Color       string             `json:"color,omitempty"`
	Description string             `json:"description,omitempty"`
	Fields      []metadataFieldDTO `json:"fields,omitempty"`
	Version     int64              `json:"version"`
}

type resourceTypeDTO struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Color       string             `json:"color,omitempty"`
	Description string             `json:"description,omitempty"`
	Fields      []metadataFieldDTO `json:"fields,omitempty"`
	Version     int64              `json:"version"`
}

type capabilityDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     int64  `json:"version"`
}

type workspaceCounts struct {
	Projects          int `json:"projects"`
	Things            int `json:"things"`
	Resources         int `json:"resources"`
	Dependencies      int `json:"dependencies"`
	Requirements      int `json:"requirements"`
	States            int `json:"states"`
	Types             int `json:"types"`
	ResourceTypes     int `json:"resource_types"`
	Capabilities      int `json:"capabilities"`
	OpenAllocations   int `json:"open_allocations"`
	ClosedAllocations int `json:"closed_allocations"`
}

type workspaceDTO struct {
	WorkspaceID string          `json:"workspace_id"`
	Origin      string          `json:"origin"`
	LastSeq     int64           `json:"last_seq"`
	LastTS      string          `json:"last_ts"`
	Counts      workspaceCounts `json:"counts"`
}

// rawMeta renders the stored canonical metadata string as raw JSON (nil when
// absent, so omitempty drops the field).
func rawMeta(m string) json.RawMessage {
	if m == "" {
		return nil
	}
	return json.RawMessage(m)
}

// ── builders ──

func buildProjectDTO(p *domain.Projection, id string) projectDTO {
	pr := p.Projects[id]
	return projectDTO{ID: id, Name: pr.Name, Metadata: rawMeta(pr.Metadata), Version: p.Version(id)}
}

// buildThingDTO renders one thing with its derived view d (callers computing
// many things pass entries of one DeriveAll sweep).
func buildThingDTO(p *domain.Projection, id string, d domain.Derived) thingDTO {
	th := p.Things[id]
	dto := thingDTO{
		ID: id, Project: th.Project, Name: th.Name, Type: th.Type,
		Parent: th.Parent, Metadata: rawMeta(th.Metadata), State: th.State,
		Composite:    len(th.Children) > 0,
		Status:       string(d.Status),
		HasAbandoned: d.HasAbandoned,
		ResumableNow: d.ResumableNow,
		Badges: badgesDTO{
			AbandonedDependency:     d.Badges.AbandonedDependency,
			FinishedUnsatisfiedDeps: d.Badges.FinishedUnsatisfiedDeps,
			OverAllocated:           d.Badges.OverAllocated,
			AllocationsOutOfStep:    d.Badges.AllocationsOutOfStep,
		},
		Version: p.Version(id),
	}
	if dto.Composite {
		for c := range th.Children {
			dto.Children = append(dto.Children, c)
		}
		sort.Strings(dto.Children)
		pr := analytics.ProgressOf(p, id)
		dto.Progress = &progressDTO{
			Satisfied: pr.Satisfied, Total: pr.Total,
			HasAbandoned: pr.HasAbandoned, Display: pr.Display,
		}
	}
	return dto
}

func buildDependencyDTO(p *domain.Projection, id string) dependencyDTO {
	dep := p.Dependencies[id]
	ok, warn := p.DepSatisfied(dep, nil)
	return dependencyDTO{
		ID: id, From: dep.From, To: dep.To, OnAbandoned: dep.OnAbandoned,
		Satisfied: ok, AbandonedTolerated: warn, Version: p.Version(id),
	}
}

func buildRequirementDTO(p *domain.Projection, id string) requirementDTO {
	req := p.Requirements[id]
	return requirementDTO{
		ID: id, Thing: req.Thing, Quantity: req.Quantity,
		Capabilities: append([]string(nil), req.Capabilities...),
		Resource:     req.Resource, Version: p.Version(id),
	}
}

func buildResourceDTO(p *domain.Projection, id string) resourceDTO {
	rs := p.Resources[id]
	caps := make([]string, 0, len(rs.Capabilities))
	for c := range rs.Capabilities {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	allocated := p.AllocatedQuantity(id)
	free := rs.EffectiveCapacity() - allocated
	if free < 0 {
		free = 0
	}
	return resourceDTO{
		ID: id, Name: rs.Name, Kind: rs.Kind, Named: rs.Named,
		Capacity: rs.Capacity, Type: rs.Type, Metadata: rawMeta(rs.Metadata),
		Capabilities: caps, Available: rs.Available, Note: rs.Note,
		EffectiveCapacity: rs.EffectiveCapacity(), Allocated: allocated,
		Free: free, OverAllocated: allocated > rs.EffectiveCapacity(),
		Version: p.Version(id),
	}
}

func buildStateDTO(p *domain.Projection, id string) stateDTO {
	st := p.States[id]
	return stateDTO{ID: id, Name: st.Name, Semantic: st.Semantic,
		Color: st.Color, Description: st.Description, Version: p.Version(id)}
}

func buildTypeDTO(p *domain.Projection, id string) typeDTO {
	ty := p.Types[id]
	return typeDTO{ID: id, Name: ty.Name, Color: ty.Color,
		Description: ty.Description, Fields: buildMetadataFieldDTOs(ty.Fields),
		Version: p.Version(id)}
}

func buildResourceTypeDTO(p *domain.Projection, id string) resourceTypeDTO {
	rt := p.ResourceTypes[id]
	return resourceTypeDTO{ID: id, Name: rt.Name, Color: rt.Color,
		Description: rt.Description, Fields: buildMetadataFieldDTOs(rt.Fields),
		Version: p.Version(id)}
}

func buildCapabilityDTO(p *domain.Projection, id string) capabilityDTO {
	c := p.Capabilities[id]
	return capabilityDTO{ID: id, Name: c.Name, Description: c.Description, Version: p.Version(id)}
}

// matchReqDTO is the wire form of a match.Requirement (analytics payloads).
type matchReqDTO struct {
	ID           string   `json:"id"`
	Quantity     int      `json:"quantity"`
	Capabilities []string `json:"capabilities,omitempty"`
	Pin          string   `json:"pin,omitempty"`
}

func buildMatchReqDTOs(reqs []match.Requirement) []matchReqDTO {
	out := make([]matchReqDTO, len(reqs))
	for i, r := range reqs {
		out[i] = matchReqDTO{ID: r.ID, Quantity: r.Quantity,
			Capabilities: append([]string(nil), r.Capabilities...), Pin: r.Pin}
	}
	return out
}

// sortedKeys returns the keys of a string-keyed map, ascending — the
// deterministic-iteration helper for list endpoints.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
