package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"churn/internal/domain"
	"churn/internal/event"
	"churn/internal/writer"
)

// transitionRequest is the body of POST /things/{id}/transition.
//
// Propose: {"state": "st_…"} — if the target state has active semantics
// (and the thing is not already active), the response is a PROPOSAL, not a
// commit: the concrete allocation set (domain.ProposeAllocations) plus an
// opaque token binding thing, state, assignment, and the projection seq it
// was computed at.
//
// Confirm: {"state": "st_…", "confirm": true, "proposal": "<token>"} —
// submits transition + allocations as ONE batch. The writer re-validates in
// its critical section; if the world drifted (another actor took the last
// unit), the commit returns 409 with details.fresh_proposal (a new token, or
// null when nothing is feasible now) so the client can re-confirm.
//
// Transitions to non-active states commit directly; leaving active
// semantics closes all open allocations in the same batch. Active→active
// transitions commit directly with allocations untouched.
type transitionRequest struct {
	State    string `json:"state"`
	Confirm  bool   `json:"confirm,omitempty"`
	Proposal string `json:"proposal,omitempty"`
}

// proposalClaim is the decoded content of a proposal token. The token is
// stateless: base64(JSON) of exactly these fields — the server keeps no
// proposal registry, and the writer's critical-section re-validation (not
// the token's seq) is the drift guard. BasedOnSeq is disclosure, not a
// precondition: a confirm against a moved-on-but-still-feasible world
// commits fine (§5: conflicts are for drift that invalidates, not for any
// drift).
type proposalClaim struct {
	Thing       string          `json:"thing"`
	State       string          `json:"state"`
	BasedOnSeq  int64           `json:"based_on_seq"`
	Allocations []allocationRow `json:"allocations"`
}

// allocationRow is one line of a proposed assignment.
type allocationRow struct {
	Requirement string `json:"requirement"`
	Resource    string `json:"resource"`
	Quantity    int    `json:"quantity"`
}

// proposalDTO is the wire form of a proposal response.
type proposalDTO struct {
	Token       string          `json:"token"`
	Thing       string          `json:"thing"`
	State       string          `json:"state"`
	BasedOnSeq  int64           `json:"based_on_seq"`
	Allocations []allocationRow `json:"allocations"`
}

func encodeProposal(c proposalClaim) (proposalDTO, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return proposalDTO{}, err
	}
	return proposalDTO{
		Token: base64.RawURLEncoding.EncodeToString(b),
		Thing: c.Thing, State: c.State, BasedOnSeq: c.BasedOnSeq,
		Allocations: c.Allocations,
	}, nil
}

func decodeProposal(token string) (proposalClaim, *apiError) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return proposalClaim{}, errBadRequest("proposal token is not valid base64")
	}
	var c proposalClaim
	if err := json.Unmarshal(b, &c); err != nil {
		return proposalClaim{}, errBadRequest("proposal token does not decode")
	}
	return c, nil
}

func toRows(props []domain.ProposedAllocation) []allocationRow {
	rows := make([]allocationRow, len(props))
	for i, pr := range props {
		rows[i] = allocationRow{Requirement: pr.Requirement, Resource: pr.Resource, Quantity: pr.Quantity}
	}
	return rows
}

// transitionResult is the response of a committed transition or re-propose.
type transitionResult struct {
	Committed bool   `json:"committed"`
	Thing     string `json:"thing"`
	State     string `json:"state,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	Batch     string `json:"batch,omitempty"`
	// Proposal is set (with Committed false) on the propose leg.
	Proposal *proposalDTO `json:"proposal,omitempty"`
	// Opened / Closed list the allocation ids the batch opened and closed.
	Opened []string `json:"opened,omitempty"`
	Closed []string `json:"closed,omitempty"`
}

// postTransition implements POST /api/v1/things/{id}/transition (§2.5,
// §5.1): propose→confirm into active states, direct commit otherwise.
func (s *Server) postTransition(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req transitionRequest
	if e := decodeJSON(r, &req); e != nil {
		writeError(rw, e)
		return
	}
	p := s.w.Projection()
	th, ok := p.Things[id]
	if !ok {
		writeError(rw, errNotFound(id))
		return
	}
	if len(th.Children) > 0 {
		writeError(rw, &apiError{status: http.StatusUnprocessableEntity,
			kind: domain.KindCompositeState, ids: []string{id},
			message: "thing " + id + " is a composite: its state is a computed rollup, not a fact"})
		return
	}
	st, ok := p.States[req.State]
	if !ok {
		writeError(rw, &apiError{status: http.StatusUnprocessableEntity,
			kind: domain.KindUndefinedReference, ids: []string{req.State},
			message: "state " + req.State + " is not defined"})
		return
	}
	expected, e := expectedFromIfMatch(r, id)
	if e != nil {
		writeError(rw, e)
		return
	}

	curActive := len(th.Children) == 0 && p.SemanticOf(th) == event.SemanticActive
	targetActive := st.Semantic == event.SemanticActive

	if targetActive && !curActive && !req.Confirm {
		// Propose leg: compute the concrete assignment, commit nothing.
		props, feasible := p.ProposeAllocations(id)
		if !feasible {
			writeError(rw, &apiError{status: http.StatusConflict,
				kind: domain.KindInfeasible, ids: []string{id},
				message: "no feasible allocation of the thing's requirements onto free units exists right now"})
			return
		}
		dto, err := encodeProposal(proposalClaim{
			Thing: id, State: req.State, BasedOnSeq: p.LastSeq, Allocations: toRows(props),
		})
		if err != nil {
			writeError(rw, mapError(err))
			return
		}
		writeJSON(rw, http.StatusOK, transitionResult{Committed: false, Thing: id, State: req.State, Proposal: &dto})
		return
	}

	var cmds []writer.Command
	var opened, closed []string
	if targetActive && !curActive {
		// Confirm leg: transition + the proposal's allocations as ONE batch.
		claim, e := decodeProposal(req.Proposal)
		if e != nil {
			writeError(rw, e)
			return
		}
		if claim.Thing != id || claim.State != req.State {
			writeError(rw, errBadRequest("proposal token binds thing %s state %s, request names thing %s state %s",
				claim.Thing, claim.State, id, req.State))
			return
		}
		cmds = append(cmds, writer.Command{
			Type: event.TypeThingStateChanged, V: 1, Entity: id,
			Payload: &event.ThingStateChanged{State: req.State},
		})
		for _, row := range claim.Allocations {
			// The rows come from the CLIENT-HELD token: validate their shape
			// like any other request payload, so a tampered token is a 400,
			// never an internal writer error.
			pl := &event.AllocationOpened{
				Thing: id, Resource: row.Resource, Quantity: row.Quantity, Requirement: row.Requirement,
			}
			if e := validatePayload(pl); e != nil {
				writeError(rw, e)
				return
			}
			alID, e := s.mint(event.PrefixAllocation)
			if e != nil {
				writeError(rw, e)
				return
			}
			opened = append(opened, alID)
			cmds = append(cmds, writer.Command{
				Type: event.TypeAllocationOpened, V: 1, Entity: alID, Payload: pl,
			})
		}
	} else {
		// Direct commit: non-active target (closing all open allocations in
		// the same batch when leaving active semantics), or active→active
		// with allocations untouched.
		if curActive && !targetActive {
			for _, alID := range p.OpenAllocationsOf(id) {
				pl := &event.AllocationClosed{}
				if e := validatePayload(pl); e != nil { // symmetry with the open set
					writeError(rw, e)
					return
				}
				closed = append(closed, alID)
				cmds = append(cmds, writer.Command{
					Type: event.TypeAllocationClosed, V: 1, Entity: alID, Payload: pl,
				})
			}
		}
		cmds = append(cmds, writer.Command{
			Type: event.TypeThingStateChanged, V: 1, Entity: id,
			Payload: &event.ThingStateChanged{State: req.State},
		})
	}

	evs, err := s.w.Submit(s.actor, cmds, expected)
	if err != nil {
		s.writeTransitionConflict(rw, err, id, req.State)
		return
	}
	writeJSON(rw, http.StatusOK, transitionResult{
		Committed: true, Thing: id, State: req.State,
		Seq: evs[len(evs)-1].Seq, Batch: evs[0].Batch,
		Opened: opened, Closed: closed,
	})
}

// writeTransitionConflict maps a rejected transition/confirm batch. For the
// drift kinds — capacity, infeasible_allocation, allocation_coverage (a
// requirement changed between propose and confirm), stale_version — it
// answers 409 and attaches details.fresh_proposal: a NEW proposal computed
// against the current projection (null when nothing is feasible), so the
// client can re-confirm directly.
func (s *Server) writeTransitionConflict(rw http.ResponseWriter, err error, thing, state string) {
	ae := mapError(err)
	var de *domain.Error
	if errors.As(err, &de) {
		switch de.Kind {
		case domain.KindCapacity, domain.KindInfeasible, domain.KindAllocationCoverage, domain.KindStaleVersion:
			ae.status = http.StatusConflict
			ae.details = map[string]any{"fresh_proposal": nil}
			cur := s.w.Projection()
			if props, feasible := cur.ProposeAllocations(thing); feasible {
				if dto, encErr := encodeProposal(proposalClaim{
					Thing: thing, State: state, BasedOnSeq: cur.LastSeq, Allocations: toRows(props),
				}); encErr == nil {
					ae.details["fresh_proposal"] = dto
				}
			}
		}
	}
	writeError(rw, ae)
}

// postRepropose implements POST /api/v1/things/{id}/repropose: the §2.5
// one-click reconciliation for the allocations-out-of-step badge — ONE batch
// closes the thing's open allocations and opens a fresh feasible assignment
// of its CURRENT requirements, while the thing stays active throughout. The
// assignment is computed server-side at request time; the writer's
// critical-section re-validation guards the remaining window, so a drifted
// re-propose returns a conflict and the client simply retries.
func (s *Server) postRepropose(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Body != nil && r.ContentLength > 0 {
		var empty struct{}
		if e := decodeJSON(r, &empty); e != nil {
			writeError(rw, e)
			return
		}
	}
	p := s.w.Projection()
	th, ok := p.Things[id]
	if !ok {
		writeError(rw, errNotFound(id))
		return
	}
	if len(th.Children) > 0 || p.SemanticOf(th) != event.SemanticActive {
		writeError(rw, &apiError{status: http.StatusUnprocessableEntity,
			kind: domain.KindAllocation, ids: []string{id},
			message: "re-propose applies to an active leaf thing"})
		return
	}
	expected, e := expectedFromIfMatch(r, id)
	if e != nil {
		writeError(rw, e)
		return
	}

	props, feasible := p.ReproposeAllocations(id)
	if !feasible {
		writeError(rw, &apiError{status: http.StatusConflict,
			kind: domain.KindInfeasible, ids: []string{id},
			message: "no feasible replacement assignment exists for the thing's current requirements"})
		return
	}

	var cmds []writer.Command
	var opened, closed []string
	for _, alID := range p.OpenAllocationsOf(id) {
		closed = append(closed, alID)
		cmds = append(cmds, writer.Command{
			Type: event.TypeAllocationClosed, V: 1, Entity: alID,
			Payload: &event.AllocationClosed{},
		})
	}
	for _, pr := range props {
		alID, e := s.mint(event.PrefixAllocation)
		if e != nil {
			writeError(rw, e)
			return
		}
		opened = append(opened, alID)
		cmds = append(cmds, writer.Command{
			Type: event.TypeAllocationOpened, V: 1, Entity: alID,
			Payload: &event.AllocationOpened{
				Thing: id, Resource: pr.Resource, Quantity: pr.Quantity, Requirement: pr.Requirement,
			},
		})
	}
	if len(cmds) == 0 {
		// No open allocations and no requirements: nothing to reconcile.
		writeJSON(rw, http.StatusOK, transitionResult{Committed: false, Thing: id})
		return
	}
	evs, err := s.w.Submit(s.actor, cmds, expected)
	if err != nil {
		writeError(rw, mapError(err))
		return
	}
	writeJSON(rw, http.StatusOK, transitionResult{
		Committed: true, Thing: id,
		Seq: evs[len(evs)-1].Seq, Batch: evs[0].Batch,
		Opened: opened, Closed: closed,
	})
}
