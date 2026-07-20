package server

import (
	"net/http"

	"churn/internal/domain"
	"churn/internal/event"
	"churn/internal/writer"
)

// kindSpec describes one CRUD-able entity kind: its routes, event types,
// payload constructors (the event payload structs ARE the request DTOs — one
// schema, validated once, no drift), and read-side builders.
type kindSpec struct {
	// name is the singular kind name /batch operations use ("project",
	// "state", …).
	name string
	// path is the route segment under /api/v1 ("projects", "vocab/states").
	path string
	// prefix is the typed id prefix minted for creates.
	prefix string
	// createType/supersedeType/retractType are the event types; an empty
	// supersedeType registers no PATCH route (dependencies).
	createType    string
	supersedeType string
	retractType   string
	newCreate     func() event.Payload
	newSupersede  func() event.Payload
	// retract is the (stateless) retraction payload.
	retract event.Payload
	exists  func(p *domain.Projection, id string) bool
	// get renders the single-entity DTO; list the sorted collection (list
	// receives the request for query-param filters).
	get  func(p *domain.Projection, id string) any
	list func(p *domain.Projection, r *http.Request) any
}

// kinds enumerates the §5.1 CRUD surface: projects, things, resources,
// dependencies, requirements, and the vocabulary (states, types,
// resource types, capabilities) — the same shape via events (§5.3).
func (s *Server) kinds() []kindSpec {
	return []kindSpec{
		{
			name: "project", path: "projects", prefix: event.PrefixProject,
			createType: event.TypeProjectCreated, supersedeType: event.TypeProjectSuperseded,
			retractType: event.TypeProjectRetracted,
			newCreate:   func() event.Payload { return new(event.ProjectCreated) },
			newSupersede: func() event.Payload {
				return new(event.ProjectSuperseded)
			},
			retract: &event.ProjectRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.Projects[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildProjectDTO(p, id) },
			list: func(p *domain.Projection, _ *http.Request) any {
				out := make([]projectDTO, 0, len(p.Projects))
				for _, id := range sortedKeys(p.Projects) {
					out = append(out, buildProjectDTO(p, id))
				}
				return out
			},
		},
		{
			name: "thing", path: "things", prefix: event.PrefixThing,
			createType: event.TypeThingCreated, supersedeType: event.TypeThingSuperseded,
			retractType: event.TypeThingRetracted,
			newCreate:   func() event.Payload { return new(event.ThingCreated) },
			newSupersede: func() event.Payload {
				return new(event.ThingSuperseded)
			},
			retract: &event.ThingRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.Things[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any {
				return buildThingDTO(p, id, p.Derive(id))
			},
			list: func(p *domain.Projection, r *http.Request) any {
				project := r.URL.Query().Get("project")
				derived := p.DeriveAll()
				out := make([]thingDTO, 0, len(p.Things))
				for _, id := range sortedKeys(p.Things) {
					if project != "" && p.Things[id].Project != project {
						continue
					}
					out = append(out, buildThingDTO(p, id, derived[id]))
				}
				return out
			},
		},
		{
			name: "resource", path: "resources", prefix: event.PrefixResource,
			createType: event.TypeResourceCreated, supersedeType: event.TypeResourceSuperseded,
			retractType: event.TypeResourceRetracted,
			newCreate:   func() event.Payload { return new(event.ResourceCreated) },
			newSupersede: func() event.Payload {
				return new(event.ResourceSuperseded)
			},
			retract: &event.ResourceRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.Resources[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildResourceDTO(p, id) },
			list: func(p *domain.Projection, _ *http.Request) any {
				out := make([]resourceDTO, 0, len(p.Resources))
				for _, id := range sortedKeys(p.Resources) {
					out = append(out, buildResourceDTO(p, id))
				}
				return out
			},
		},
		{
			name: "dependency", path: "dependencies", prefix: event.PrefixDependency,
			createType:  event.TypeDependencyAsserted,
			retractType: event.TypeDependencyRetracted,
			newCreate:   func() event.Payload { return new(event.DependencyAsserted) },
			retract:     &event.DependencyRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.Dependencies[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildDependencyDTO(p, id) },
			list: func(p *domain.Projection, _ *http.Request) any {
				out := make([]dependencyDTO, 0, len(p.Dependencies))
				for _, id := range sortedKeys(p.Dependencies) {
					out = append(out, buildDependencyDTO(p, id))
				}
				return out
			},
		},
		{
			name: "requirement", path: "requirements", prefix: event.PrefixRequirement,
			createType: event.TypeRequirementAsserted, supersedeType: event.TypeRequirementSuperseded,
			retractType: event.TypeRequirementRetracted,
			newCreate:   func() event.Payload { return new(event.RequirementAsserted) },
			newSupersede: func() event.Payload {
				return new(event.RequirementSuperseded)
			},
			retract: &event.RequirementRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.Requirements[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildRequirementDTO(p, id) },
			list: func(p *domain.Projection, _ *http.Request) any {
				out := make([]requirementDTO, 0, len(p.Requirements))
				for _, id := range sortedKeys(p.Requirements) {
					out = append(out, buildRequirementDTO(p, id))
				}
				return out
			},
		},
		{
			name: "note", path: "notes", prefix: event.PrefixNote,
			createType: event.TypeNoteAdded, supersedeType: event.TypeNoteSuperseded,
			retractType: event.TypeNoteRetracted,
			newCreate:   func() event.Payload { return new(event.NoteAdded) },
			newSupersede: func() event.Payload {
				return new(event.NoteSuperseded)
			},
			retract: &event.NoteRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.Notes[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildNoteDTO(p, id) },
			list: func(p *domain.Projection, r *http.Request) any {
				thing := r.URL.Query().Get("thing")
				out := make([]noteDTO, 0, len(p.Notes))
				for _, id := range sortedKeys(p.Notes) {
					if thing != "" && p.Notes[id].Thing != thing {
						continue
					}
					out = append(out, buildNoteDTO(p, id))
				}
				return out
			},
		},
		{
			name: "state", path: "vocab/states", prefix: event.PrefixState,
			createType: event.TypeStateDefined, supersedeType: event.TypeStateSuperseded,
			retractType: event.TypeStateRetracted,
			newCreate:   func() event.Payload { return new(event.StateDefined) },
			newSupersede: func() event.Payload {
				return new(event.StateSuperseded)
			},
			retract: &event.StateRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.States[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildStateDTO(p, id) },
			list: func(p *domain.Projection, _ *http.Request) any {
				out := make([]stateDTO, 0, len(p.States))
				for _, id := range sortedKeys(p.States) {
					out = append(out, buildStateDTO(p, id))
				}
				return out
			},
		},
		{
			name: "type", path: "vocab/types", prefix: event.PrefixType,
			createType: event.TypeTypeDefined, supersedeType: event.TypeTypeSuperseded,
			retractType: event.TypeTypeRetracted,
			newCreate:   func() event.Payload { return new(event.TypeDefined) },
			newSupersede: func() event.Payload {
				return new(event.TypeSuperseded)
			},
			retract: &event.TypeRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.Types[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildTypeDTO(p, id) },
			list: func(p *domain.Projection, _ *http.Request) any {
				out := make([]typeDTO, 0, len(p.Types))
				for _, id := range sortedKeys(p.Types) {
					out = append(out, buildTypeDTO(p, id))
				}
				return out
			},
		},
		{
			name: "resource_type", path: "vocab/resource-types", prefix: event.PrefixResourceType,
			createType: event.TypeResourceTypeDefined, supersedeType: event.TypeResourceTypeSuperseded,
			retractType: event.TypeResourceTypeRetracted,
			newCreate:   func() event.Payload { return new(event.ResourceTypeDefined) },
			newSupersede: func() event.Payload {
				return new(event.ResourceTypeSuperseded)
			},
			retract: &event.ResourceTypeRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.ResourceTypes[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildResourceTypeDTO(p, id) },
			list: func(p *domain.Projection, _ *http.Request) any {
				out := make([]resourceTypeDTO, 0, len(p.ResourceTypes))
				for _, id := range sortedKeys(p.ResourceTypes) {
					out = append(out, buildResourceTypeDTO(p, id))
				}
				return out
			},
		},
		{
			name: "capability", path: "vocab/capabilities", prefix: event.PrefixCapability,
			createType: event.TypeCapabilityDefined, supersedeType: event.TypeCapabilitySuperseded,
			retractType: event.TypeCapabilityRetracted,
			newCreate:   func() event.Payload { return new(event.CapabilityDefined) },
			newSupersede: func() event.Payload {
				return new(event.CapabilitySuperseded)
			},
			retract: &event.CapabilityRetracted{},
			exists: func(p *domain.Projection, id string) bool {
				_, ok := p.Capabilities[id]
				return ok
			},
			get: func(p *domain.Projection, id string) any { return buildCapabilityDTO(p, id) },
			list: func(p *domain.Projection, _ *http.Request) any {
				out := make([]capabilityDTO, 0, len(p.Capabilities))
				for _, id := range sortedKeys(p.Capabilities) {
					out = append(out, buildCapabilityDTO(p, id))
				}
				return out
			},
		},
	}
}

// registerKind wires the generic CRUD handlers for one kind.
func (s *Server) registerKind(mux *http.ServeMux, k kindSpec) {
	base := "/api/v1/" + k.path
	mux.HandleFunc("GET "+base, func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, http.StatusOK, k.list(s.w.Projection(), r))
	})
	mux.HandleFunc("GET "+base+"/{id}", func(rw http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p := s.w.Projection()
		if !k.exists(p, id) {
			writeError(rw, errNotFound(id))
			return
		}
		writeJSON(rw, http.StatusOK, k.get(p, id))
	})
	mux.HandleFunc("POST "+base, func(rw http.ResponseWriter, r *http.Request) {
		s.handleCreate(rw, r, k)
	})
	if k.supersedeType != "" {
		mux.HandleFunc("PATCH "+base+"/{id}", func(rw http.ResponseWriter, r *http.Request) {
			s.handleSupersede(rw, r, k)
		})
	}
	mux.HandleFunc("DELETE "+base+"/{id}", func(rw http.ResponseWriter, r *http.Request) {
		s.handleRetract(rw, r, k)
	})
}

// mint mints one typed entity id via the writer.
func (s *Server) mint(prefix string) (string, *apiError) {
	id, err := s.w.MintID(prefix)
	if err != nil {
		return "", &apiError{status: http.StatusInternalServerError, kind: "internal", message: err.Error()}
	}
	return id, nil
}

// handleCreate mints an id, appends the create event, and returns 201 with
// the created entity (id + version at minimum).
func (s *Server) handleCreate(rw http.ResponseWriter, r *http.Request, k kindSpec) {
	pl := k.newCreate()
	if e := decodeJSON(r, pl); e != nil {
		writeError(rw, e)
		return
	}
	if e := validatePayload(pl); e != nil {
		writeError(rw, e)
		return
	}
	id, e := s.mint(k.prefix)
	if e != nil {
		writeError(rw, e)
		return
	}
	if _, err := s.w.Submit(s.actor, []writer.Command{
		{Type: k.createType, V: 1, Entity: id, Payload: pl},
	}, nil); err != nil {
		writeError(rw, mapError(err))
		return
	}
	s.respondEntity(rw, http.StatusCreated, k, id)
}

// handleSupersede appends the full-replacement supersession (§5.2), guarded
// by the optional If-Match expected version.
func (s *Server) handleSupersede(rw http.ResponseWriter, r *http.Request, k kindSpec) {
	id := r.PathValue("id")
	if !k.exists(s.w.Projection(), id) {
		writeError(rw, errNotFound(id))
		return
	}
	pl := k.newSupersede()
	if e := decodeJSON(r, pl); e != nil {
		writeError(rw, e)
		return
	}
	if e := validatePayload(pl); e != nil {
		writeError(rw, e)
		return
	}
	expected, e := expectedFromIfMatch(r, id)
	if e != nil {
		writeError(rw, e)
		return
	}
	if _, err := s.w.Submit(s.actor, []writer.Command{
		{Type: k.supersedeType, V: 1, Entity: id, Payload: pl},
	}, expected); err != nil {
		writeError(rw, mapError(err))
		return
	}
	s.respondEntity(rw, http.StatusOK, k, id)
}

// handleRetract appends the retraction tombstone, guarded by the optional
// If-Match expected version. The domain rejects it while inbound references
// exist (409 retraction_blocked, referencing ids enumerated).
func (s *Server) handleRetract(rw http.ResponseWriter, r *http.Request, k kindSpec) {
	id := r.PathValue("id")
	if !k.exists(s.w.Projection(), id) {
		writeError(rw, errNotFound(id))
		return
	}
	expected, e := expectedFromIfMatch(r, id)
	if e != nil {
		writeError(rw, e)
		return
	}
	evs, err := s.w.Submit(s.actor, []writer.Command{
		{Type: k.retractType, V: 1, Entity: id, Payload: k.retract},
	}, expected)
	if err != nil {
		writeError(rw, mapError(err))
		return
	}
	writeJSON(rw, http.StatusOK, struct {
		ID        string `json:"id"`
		Retracted bool   `json:"retracted"`
		Seq       int64  `json:"seq"`
	}{id, true, evs[len(evs)-1].Seq})
}

// respondEntity writes the entity's DTO from the now-published projection —
// or, if a concurrent batch already removed it, the minimal id envelope.
func (s *Server) respondEntity(rw http.ResponseWriter, status int, k kindSpec, id string) {
	p := s.w.Projection()
	if k.exists(p, id) {
		writeJSON(rw, status, k.get(p, id))
		return
	}
	writeJSON(rw, status, struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}{id, p.Version(id)})
}

// ── resource sub-facts ──

// postAvailability toggles a resource's availability (never blocked: §2.5
// reality wins; warnings follow).
func (s *Server) postAvailability(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.w.Projection().Resources[id]; !ok {
		writeError(rw, errNotFound(id))
		return
	}
	var pl event.ResourceAvailabilityChanged
	if e := decodeJSON(r, &pl); e != nil {
		writeError(rw, e)
		return
	}
	expected, e := expectedFromIfMatch(r, id)
	if e != nil {
		writeError(rw, e)
		return
	}
	if _, err := s.w.Submit(s.actor, []writer.Command{
		{Type: event.TypeResourceAvailabilityChanged, V: 1, Entity: id, Payload: &pl},
	}, expected); err != nil {
		writeError(rw, mapError(err))
		return
	}
	writeJSON(rw, http.StatusOK, buildResourceDTO(s.w.Projection(), id))
}

// postGrant grants a capability to a resource.
func (s *Server) postGrant(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.w.Projection().Resources[id]; !ok {
		writeError(rw, errNotFound(id))
		return
	}
	var pl event.CapabilityGranted
	if e := decodeJSON(r, &pl); e != nil {
		writeError(rw, e)
		return
	}
	if e := validatePayload(&pl); e != nil {
		writeError(rw, e)
		return
	}
	if _, err := s.w.Submit(s.actor, []writer.Command{
		{Type: event.TypeCapabilityGranted, V: 1, Entity: id, Payload: &pl},
	}, nil); err != nil {
		writeError(rw, mapError(err))
		return
	}
	writeJSON(rw, http.StatusOK, buildResourceDTO(s.w.Projection(), id))
}

// deleteGrant revokes a capability from a resource.
func (s *Server) deleteGrant(rw http.ResponseWriter, r *http.Request) {
	id, cap := r.PathValue("id"), r.PathValue("cap")
	if _, ok := s.w.Projection().Resources[id]; !ok {
		writeError(rw, errNotFound(id))
		return
	}
	pl := event.CapabilityRevoked{Capability: cap}
	if e := validatePayload(&pl); e != nil {
		writeError(rw, e)
		return
	}
	if _, err := s.w.Submit(s.actor, []writer.Command{
		{Type: event.TypeCapabilityRevoked, V: 1, Entity: id, Payload: &pl},
	}, nil); err != nil {
		writeError(rw, mapError(err))
		return
	}
	writeJSON(rw, http.StatusOK, buildResourceDTO(s.w.Projection(), id))
}
