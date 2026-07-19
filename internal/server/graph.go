package server

import (
	"errors"
	"net/http"
	"time"

	"churn/internal/domain"
	"churn/internal/event"
)

// graphDTO is GET /api/v1/projects/{id}/graph: the full graph of one project
// — things (with derived statuses and badges), declared dependency edges,
// and the expanded leaf adjacency — live, or as of a past cursor (§3.6).
type graphDTO struct {
	Project projectDTO `json:"project"`
	// AsOf reports the snapped cursor; absent on the live projection.
	AsOf *asOfDTO `json:"as_of,omitempty"`
	// Things lists the project's things, sorted by id.
	Things []thingDTO `json:"things"`
	// Dependencies lists declared edges touching the project (either
	// endpoint's thing in it), sorted by id. Endpoints outside the project
	// are not expanded into Things.
	Dependencies []dependencyDTO `json:"dependencies"`
	// Edges is the expanded leaf adjacency (§2.1) restricted to pairs
	// touching the project, sorted by (from, to). Declared is true when a
	// declared dependency directly connects exactly these two leaves;
	// otherwise the edge is inherited from a composite endpoint's expansion.
	Edges []expandedEdgeDTO `json:"edges"`
}

type asOfDTO struct {
	// Requested echoes the query parameter; Seq and TS are the snapped
	// position: the last COMPLETE batch at or before the cursor (§3.6).
	Requested string `json:"requested"`
	Seq       int64  `json:"seq"`
	TS        string `json:"ts"`
}

type expandedEdgeDTO struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Declared bool   `json:"declared"`
}

// getGraph implements GET /api/v1/projects/{id}/graph?as_of=ts|seq.
//
// as_of semantics (§3.6): a numeric value is a seq cursor, anything else a
// timestamp (the writer's fixed-width UTC form, or any RFC 3339 time). The
// cursor snaps DOWN to the last batch fully committed at or before it — a
// seq inside a batch resolves to the previous batch, so no view ever
// exposes a state between two events of one atomic operation. A cursor
// before the first batch is 404 (there was no workspace to view — the
// documented decision, rather than an empty graph). Without as_of the live
// projection answers.
func (s *Server) getGraph(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := s.w.Projection()
	var asOf *asOfDTO

	if cursor := r.URL.Query().Get("as_of"); cursor != "" {
		bySeq, byTS, e := parseAsOf(cursor)
		if e != nil {
			writeError(rw, e)
			return
		}
		past, e := s.foldAsOf(bySeq, byTS)
		if e != nil {
			writeError(rw, e)
			return
		}
		p = past
		asOf = &asOfDTO{Requested: cursor, Seq: p.LastSeq, TS: p.LastTS}
	}

	if _, ok := p.Projects[id]; !ok {
		writeError(rw, errNotFound(id))
		return
	}

	dto := graphDTO{Project: buildProjectDTO(p, id), AsOf: asOf}
	inProject := map[string]bool{}
	derived := p.DeriveAll()
	for _, tid := range sortedKeys(p.Things) {
		if p.Things[tid].Project != id {
			continue
		}
		inProject[tid] = true
		dto.Things = append(dto.Things, buildThingDTO(p, tid, derived[tid]))
	}

	declared := map[[2]string]bool{}
	for _, did := range sortedKeys(p.Dependencies) {
		dep := p.Dependencies[did]
		declared[[2]string{dep.From, dep.To}] = true
		fromTh, okF := p.Things[dep.From]
		toTh, okT := p.Things[dep.To]
		if (okF && fromTh.Project == id) || (okT && toTh.Project == id) {
			dto.Dependencies = append(dto.Dependencies, buildDependencyDTO(p, did))
		}
	}

	adj := p.ExpandedLeafGraph()
	for _, from := range sortedKeys(adj) {
		for _, to := range adj[from] {
			if !inProject[from] && !inProject[to] {
				continue
			}
			dto.Edges = append(dto.Edges, expandedEdgeDTO{
				From: from, To: to, Declared: declared[[2]string{from, to}],
			})
		}
	}

	// Non-nil empty slices: a graph always has these keys.
	if dto.Things == nil {
		dto.Things = []thingDTO{}
	}
	if dto.Dependencies == nil {
		dto.Dependencies = []dependencyDTO{}
	}
	if dto.Edges == nil {
		dto.Edges = []expandedEdgeDTO{}
	}
	writeJSON(rw, http.StatusOK, dto)
}

// parseAsOf splits the as_of parameter into a seq cursor (all digits) or a
// timestamp cursor (normalized to the writer's fixed-width UTC form, so
// string comparison is time comparison).
func parseAsOf(cursor string) (bySeq int64, byTS string, e *apiError) {
	digits := len(cursor) > 0
	for _, c := range cursor {
		if c < '0' || c > '9' {
			digits = false
			break
		}
	}
	if digits {
		var n int64
		for _, c := range cursor {
			n = n*10 + int64(c-'0')
			if n < 0 {
				return 0, "", errBadRequest("as_of seq %q overflows", cursor)
			}
		}
		if n == 0 {
			return 0, "", errBadRequest("as_of seq must be >= 1")
		}
		return n, "", nil
	}
	for _, layout := range []string{event.TSFormat, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, cursor); err == nil {
			return 0, t.UTC().Format(event.TSFormat), nil
		}
	}
	return 0, "", errBadRequest("as_of %q: want a seq or an RFC 3339 timestamp", cursor)
}

// foldAsOf replays the log up to the snapped cursor: whole batches only,
// including a batch iff its LAST event is at or before the cursor — §3.6's
// "last batch committed at or before". The scan runs on the store's read
// pool (a consistent WAL snapshot) and stops at the first excluded batch.
func (s *Server) foldAsOf(bySeq int64, byTS string) (*domain.Projection, *apiError) {
	var included, pending []event.Envelope
	errStop := errors.New("stop")
	flush := func() bool {
		if len(pending) == 0 {
			return true
		}
		if bySeq > 0 && pending[len(pending)-1].Seq > bySeq {
			return false
		}
		included = append(included, pending...)
		pending = pending[:0]
		return true
	}
	err := s.st.Scan(func(ev event.Envelope) error {
		// All events of a batch share the commit ts, so a too-late ts ends
		// inclusion at a batch boundary by construction.
		if byTS != "" && ev.TS > byTS {
			return errStop
		}
		if len(pending) > 0 && ev.Batch != pending[0].Batch {
			if !flush() {
				return errStop
			}
		}
		if bySeq > 0 && ev.Seq > bySeq {
			pending = pending[:0] // cursor lands inside this batch: exclude it
			return errStop
		}
		pending = append(pending, ev)
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return nil, &apiError{status: http.StatusInternalServerError, kind: "internal", message: err.Error()}
	}
	flush()
	if len(included) == 0 {
		return nil, &apiError{status: http.StatusNotFound, kind: "not_found",
			message: "as_of predates the first batch of the log"}
	}
	p, ferr := domain.Fold(included)
	if ferr != nil {
		return nil, &apiError{status: http.StatusInternalServerError, kind: "internal", message: ferr.Error()}
	}
	return p, nil
}
