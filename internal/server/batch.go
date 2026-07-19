package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"churn/internal/canonjson"
	"churn/internal/domain"
	"churn/internal/event"
	"churn/internal/writer"
)

// batchRequest is the body of POST /api/v1/batch — the substrate for all
// bulk UI operations (§5.1): any set of mutations as ONE atomic event batch.
//
// mode "preview" validates the whole batch against the current projection —
// domain.ValidateBatch on a clone, exactly the writer's own validation — and
// returns per-op results with the ids the ops WOULD produce, committing
// nothing (the log and its seq are untouched; a later commit mints fresh
// ids). mode "commit" submits the batch through the writer.
//
// expected_versions is the §5.2 precondition map (entity id → last seen
// version) for entities the batch writes; a mismatch is a 409 stale_version
// conflict naming the stale ids.
//
// Operations reference existing entities by id, or by PLACEHOLDER: the
// string "$N" (N = zero-based index into operations) names the id minted by
// the create op at that index. Placeholders are resolved in the decoded,
// understood fields only — op "id" targets and the id-bearing payload
// fields (project, type, parent, state, from, to, thing, resource/pin,
// capabilities) — never by textual replacement in raw JSON, so metadata and
// names can contain "$0" literally. A placeholder must point BACKWARD at an
// earlier create op; forward or self references, indexes out of range,
// references to non-create ops, and malformed forms are 400 bad_request.
// The response carries the full placeholder→minted-id mapping (preview and
// commit alike; preview's minted ids are discarded — a later commit mints
// fresh ones).
//
// A transition op into an active-semantic state is rejected by validation
// unless the batch itself carries a covering allocation set — the
// interactive propose→confirm endpoint is the intended path for those.
//
// Note the §2.1 demotion rule is ORDER-SENSITIVE: a batch that removes a
// composite's last child must place the thing's explicit transition into a
// pending state AFTER the child-removing op.
type batchRequest struct {
	Mode             string           `json:"mode"`
	ExpectedVersions map[string]int64 `json:"expected_versions,omitempty"`
	Operations       []batchOp        `json:"operations"`
}

// batchOp is one operation of a batch.
//
//	op: create | supersede | retract | transition | availability | grant | revoke
//	kind: project | thing | resource | dependency | requirement | state | type | resource_type | capability
//	id:   target entity id (required except for create)
//	data: the event payload for the (kind, op) — the same schema the
//	      single-entity endpoints take
//
// transition, availability, grant, and revoke apply to kind "thing",
// "resource", "resource", "resource" respectively.
type batchOp struct {
	Op   string          `json:"op"`
	Kind string          `json:"kind"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// batchOpResult reports one op's outcome: the id it targeted or minted.
type batchOpResult struct {
	ID string `json:"id"`
}

// batchResponse is the reply of POST /api/v1/batch.
type batchResponse struct {
	Mode      string          `json:"mode"`
	Committed bool            `json:"committed"`
	Results   []batchOpResult `json:"results"`
	// Placeholders maps each create op's "$N" placeholder to the id it
	// minted (present whenever the batch contains creates; preview ids are
	// discarded — a later commit mints fresh ones).
	Placeholders map[string]string `json:"placeholders,omitempty"`
	// Seq and Batch identify the committed batch (commit mode only).
	Seq   int64  `json:"seq,omitempty"`
	Batch string `json:"batch,omitempty"`
}

// postBatch implements POST /api/v1/batch (preview → commit, §5).
func (s *Server) postBatch(rw http.ResponseWriter, r *http.Request) {
	var req batchRequest
	if e := decodeJSON(r, &req); e != nil {
		writeError(rw, e)
		return
	}
	if req.Mode != "preview" && req.Mode != "commit" {
		writeError(rw, errBadRequest(`mode must be "preview" or "commit", got %q`, req.Mode))
		return
	}
	if len(req.Operations) == 0 {
		writeError(rw, errBadRequest("operations must not be empty"))
		return
	}

	byName := map[string]kindSpec{}
	for _, k := range s.kinds() {
		byName[k.name] = k
	}

	// minted[i] is the id minted by create op i ("" for other ops) — the
	// substitution table for "$N" placeholder references (see batchRequest).
	minted := make([]string, len(req.Operations))
	resolverAt := func(opIdx int) refResolver {
		return func(ref string) (string, *apiError) {
			if !strings.HasPrefix(ref, "$") {
				return ref, nil
			}
			n, err := strconv.Atoi(ref[1:])
			if err != nil || n < 0 || n >= len(req.Operations) {
				return "", errBadRequest("placeholder %q does not index an operation (0..%d)", ref, len(req.Operations)-1)
			}
			if n >= opIdx {
				return "", errBadRequest("placeholder %q must reference an EARLIER create op (forward and self references are rejected)", ref)
			}
			if minted[n] == "" {
				return "", errBadRequest("placeholder %q references operation %d, which is not a create", ref, n)
			}
			return minted[n], nil
		}
	}

	cmds := make([]writer.Command, 0, len(req.Operations))
	results := make([]batchOpResult, 0, len(req.Operations))
	placeholders := map[string]string{}
	for i, op := range req.Operations {
		cmd, e := s.translateOp(byName, op, resolverAt(i))
		if e != nil {
			e.message = fmt.Sprintf("operation %d: %s", i, e.message)
			writeError(rw, e)
			return
		}
		if op.Op == "create" {
			minted[i] = cmd.Entity
			placeholders["$"+strconv.Itoa(i)] = cmd.Entity
		}
		cmds = append(cmds, cmd)
		results = append(results, batchOpResult{ID: cmd.Entity})
	}
	if len(placeholders) == 0 {
		placeholders = nil
	}

	if req.Mode == "preview" {
		if e := s.previewBatch(cmds, req.ExpectedVersions); e != nil {
			writeError(rw, e)
			return
		}
		writeJSON(rw, http.StatusOK, batchResponse{
			Mode: "preview", Committed: false, Results: results, Placeholders: placeholders,
		})
		return
	}

	evs, err := s.w.Submit(s.actor, cmds, req.ExpectedVersions)
	if err != nil {
		writeError(rw, mapError(err))
		return
	}
	writeJSON(rw, http.StatusOK, batchResponse{
		Mode: "commit", Committed: true, Results: results, Placeholders: placeholders,
		Seq: evs[len(evs)-1].Seq, Batch: evs[0].Batch,
	})
}

// refResolver resolves one id-or-placeholder reference (see batchRequest).
type refResolver func(ref string) (string, *apiError)

// resolvePayloadRefs substitutes placeholder references in the id-bearing
// fields of a decoded batch payload — the fields the server understands,
// never raw JSON (metadata and names keep "$0" literally). Payload types
// without entity references (vocab defines, project creates, availability)
// need no case.
func resolvePayloadRefs(pl event.Payload, resolve refResolver) *apiError {
	fields := func(fs ...*string) *apiError {
		for _, f := range fs {
			v, e := resolve(*f)
			if e != nil {
				return e
			}
			*f = v
		}
		return nil
	}
	slice := func(ss []string) *apiError {
		for i := range ss {
			v, e := resolve(ss[i])
			if e != nil {
				return e
			}
			ss[i] = v
		}
		return nil
	}
	switch p := pl.(type) {
	case *event.ThingCreated:
		return fields(&p.Project, &p.Type, &p.Parent)
	case *event.ThingSuperseded:
		return fields(&p.Type, &p.Parent)
	case *event.ThingStateChanged:
		return fields(&p.State)
	case *event.DependencyAsserted:
		return fields(&p.From, &p.To)
	case *event.RequirementAsserted:
		if e := fields(&p.Thing, &p.Resource); e != nil {
			return e
		}
		return slice(p.Capabilities)
	case *event.RequirementSuperseded:
		if e := fields(&p.Resource); e != nil {
			return e
		}
		return slice(p.Capabilities)
	case *event.ResourceCreated:
		return fields(&p.Type)
	case *event.ResourceSuperseded:
		return fields(&p.Type)
	case *event.CapabilityGranted:
		return fields(&p.Capability)
	case *event.CapabilityRevoked:
		return fields(&p.Capability)
	}
	return nil
}

// translateOp turns one batch op into a writer command, minting ids for
// creates, resolving "$N" placeholder references (op targets and payload id
// fields), and validating the payload shape.
func (s *Server) translateOp(byName map[string]kindSpec, op batchOp, resolve refResolver) (writer.Command, *apiError) {
	var zero writer.Command
	k, ok := byName[op.Kind]
	if !ok {
		return zero, errBadRequest("unknown kind %q", op.Kind)
	}

	decode := func(pl event.Payload) *apiError {
		data := op.Data
		if len(data) == 0 {
			data = []byte("{}")
		}
		if err := json.Unmarshal(data, pl); err != nil {
			return errBadRequest("decoding data: %v", err)
		}
		if e := resolvePayloadRefs(pl, resolve); e != nil {
			return e
		}
		return validatePayload(pl)
	}
	needID := func() *apiError {
		if op.ID == "" {
			return errBadRequest("%s %s requires an id", op.Op, op.Kind)
		}
		var e *apiError
		op.ID, e = resolve(op.ID)
		return e
	}

	switch op.Op {
	case "create":
		if op.ID != "" {
			return zero, errBadRequest("create mints the id server-side; do not supply one")
		}
		pl := k.newCreate()
		if e := decode(pl); e != nil {
			return zero, e
		}
		id, e := s.mint(k.prefix)
		if e != nil {
			return zero, e
		}
		return writer.Command{Type: k.createType, V: 1, Entity: id, Payload: pl}, nil

	case "supersede":
		if k.supersedeType == "" {
			return zero, errBadRequest("%s has no supersession: retract and re-create", op.Kind)
		}
		if e := needID(); e != nil {
			return zero, e
		}
		pl := k.newSupersede()
		if e := decode(pl); e != nil {
			return zero, e
		}
		return writer.Command{Type: k.supersedeType, V: 1, Entity: op.ID, Payload: pl}, nil

	case "retract":
		if e := needID(); e != nil {
			return zero, e
		}
		return writer.Command{Type: k.retractType, V: 1, Entity: op.ID, Payload: k.retract}, nil

	case "transition":
		if op.Kind != "thing" {
			return zero, errBadRequest("transition applies to kind thing, not %q", op.Kind)
		}
		if e := needID(); e != nil {
			return zero, e
		}
		pl := new(event.ThingStateChanged)
		if e := decode(pl); e != nil {
			return zero, e
		}
		return writer.Command{Type: event.TypeThingStateChanged, V: 1, Entity: op.ID, Payload: pl}, nil

	case "availability":
		if op.Kind != "resource" {
			return zero, errBadRequest("availability applies to kind resource, not %q", op.Kind)
		}
		if e := needID(); e != nil {
			return zero, e
		}
		pl := new(event.ResourceAvailabilityChanged)
		if e := decode(pl); e != nil {
			return zero, e
		}
		return writer.Command{Type: event.TypeResourceAvailabilityChanged, V: 1, Entity: op.ID, Payload: pl}, nil

	case "grant", "revoke":
		if op.Kind != "resource" {
			return zero, errBadRequest("%s applies to kind resource, not %q", op.Op, op.Kind)
		}
		if e := needID(); e != nil {
			return zero, e
		}
		if op.Op == "grant" {
			pl := new(event.CapabilityGranted)
			if e := decode(pl); e != nil {
				return zero, e
			}
			return writer.Command{Type: event.TypeCapabilityGranted, V: 1, Entity: op.ID, Payload: pl}, nil
		}
		pl := new(event.CapabilityRevoked)
		if e := decode(pl); e != nil {
			return zero, e
		}
		return writer.Command{Type: event.TypeCapabilityRevoked, V: 1, Entity: op.ID, Payload: pl}, nil

	default:
		return zero, errBadRequest("unknown op %q", op.Op)
	}
}

// previewBatch runs the exact validation a commit would run —
// domain.ValidateBatch over synthetic envelopes appended after the current
// projection — and discards the candidate. Nothing touches the log.
func (s *Server) previewBatch(cmds []writer.Command, expected map[string]int64) *apiError {
	p := s.w.Projection()
	evs := make([]event.Envelope, len(cmds))
	for i, c := range cmds {
		data, err := canonjson.Encode(c.Payload)
		if err != nil {
			return &apiError{status: http.StatusInternalServerError, kind: "internal",
				message: fmt.Sprintf("encoding %s payload: %v", c.Type, err)}
		}
		evs[i] = event.Envelope{
			Seq:    p.LastSeq + 1 + int64(i),
			ID:     fmt.Sprintf("preview_%d", i),
			Origin: p.Origin,
			Batch:  "preview",
			TS:     p.LastTS,
			Actor:  s.actor,
			Type:   c.Type,
			V:      c.V,
			Entity: c.Entity,
			Data:   data,
		}
	}
	if _, err := domain.ValidateBatch(p, evs, expected); err != nil {
		return mapError(err)
	}
	return nil
}
