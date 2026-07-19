package server

import (
	"encoding/json"
	"fmt"
	"net/http"

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
// Operations reference existing entities by id; there is no placeholder
// syntax for referencing ids minted earlier in the same batch (preview the
// creates first, then compose). A transition op into an active-semantic
// state is rejected by validation unless the batch itself carries a covering
// allocation set — the interactive propose→confirm endpoint is the intended
// path for those.
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
//	kind: project | thing | resource | dependency | requirement | state | type | capability
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

	cmds := make([]writer.Command, 0, len(req.Operations))
	results := make([]batchOpResult, 0, len(req.Operations))
	for i, op := range req.Operations {
		cmd, e := s.translateOp(byName, op)
		if e != nil {
			e.message = fmt.Sprintf("operation %d: %s", i, e.message)
			writeError(rw, e)
			return
		}
		cmds = append(cmds, cmd)
		results = append(results, batchOpResult{ID: cmd.Entity})
	}

	if req.Mode == "preview" {
		if e := s.previewBatch(cmds, req.ExpectedVersions); e != nil {
			writeError(rw, e)
			return
		}
		writeJSON(rw, http.StatusOK, batchResponse{Mode: "preview", Committed: false, Results: results})
		return
	}

	evs, err := s.w.Submit(s.actor, cmds, req.ExpectedVersions)
	if err != nil {
		writeError(rw, mapError(err))
		return
	}
	writeJSON(rw, http.StatusOK, batchResponse{
		Mode: "commit", Committed: true, Results: results,
		Seq: evs[len(evs)-1].Seq, Batch: evs[0].Batch,
	})
}

// translateOp turns one batch op into a writer command, minting ids for
// creates and validating the payload shape.
func (s *Server) translateOp(byName map[string]kindSpec, op batchOp) (writer.Command, *apiError) {
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
		return validatePayload(pl)
	}
	needID := func() *apiError {
		if op.ID == "" {
			return errBadRequest("%s %s requires an id", op.Op, op.Kind)
		}
		return nil
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
