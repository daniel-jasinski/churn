// Package server hosts the HTTP API (DESIGN.md §5.1): the /api/v1 surface
// over one open workspace's writer, store, and projection. Handlers are
// deliberately thin — request translation and calls into domain, analytics,
// and the writer; all domain meaning stays in the fold (the one-brain rule:
// no SQL computes status, no logic is duplicated from the domain).
//
// # Mutation semantics
//
// Every mutation translates to one writer.Submit batch: validated against
// the live projection inside the writer's critical section, committed as one
// SQLite transaction, published atomically. Ids for new entities are minted
// server-side (writer.MintID) and returned; clients never supply ids.
// PATCH is §5.2 full replacement — the request body is the COMPLETE new
// attribute set (the event payload schema), never a partial patch; missing
// fields are rejected by payload validation, unknown fields by strict
// decoding. Optimistic concurrency: single-entity mutations take the
// expected version (the entity's version = seq of the last touching event,
// returned as "version" on every GET) in an If-Match header; /batch takes an
// explicit expected_versions map. A mismatch is a 409 stale_version
// conflict (§5.2 "preconditions are commands, not facts").
//
// The actor is stamped server-side on every Submit from the serve --actor
// option (default: OS username) — clients never supply it. Phase 3 replaces
// this with server-side sessions (§6); the seam is Options.Actor.
//
// # Error envelope and status mapping
//
// Every non-2xx response is the one JSON envelope
//
//	{"error": {"kind": "...", "message": "...", "ids": [...], "details": {...}}}
//
// with domain.Error kinds passed through verbatim. The documented mapping:
//
//	400 bad_request              malformed JSON, unknown fields, invalid
//	                             query params, invalid payload shape
//	404 not_found                unknown id in the URL; as_of before the log
//	404 unknown_entity           domain: target entity does not exist
//	405 method_not_allowed       method mismatch (Allow header set)
//	409 stale_version            expected version precondition failed
//	409 cycle                    expanded leaf graph would become cyclic
//	409 retraction_blocked       inbound references exist
//	409 semantic_immutable       state semantic change while occupied
//	409 capacity                 allocation exceeds free effective capacity
//	                             (the propose→confirm drift conflict)
//	409 infeasible_allocation    resource cannot satisfy the requirement
//	413 payload_too_large        request body over the 4 MiB limit
//	415 unsupported_media_type   mutation body not application/json
//	422 duplicate_id, undefined_reference, containment, composite_state,
//	    composite_requirement, pin_violation, allocation,
//	    allocation_coverage, demotion
//	                             validation rejections that are not conflicts
//	500 internal                 unexpected server error (incl. panics)
//
// On a drifted transition confirm (409), details carries "fresh_proposal" —
// a new proposal (or null if none is feasible now) so the client can
// re-confirm without another round trip.
//
// # Determinism
//
// Every list is stably ordered: entity collections by id ascending;
// analytics lists by their packages' documented keys (ready and
// recommendations by score descending then thing id; contention signatures
// by signature; starvation by current stint, credit, id; history by seq).
package server

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"churn/internal/store"
	"churn/internal/writer"
)

// maxBodyBytes is the request-body size limit on every request (§5-adjacent
// hygiene): 4 MiB comfortably covers the largest legitimate bulk batch.
const maxBodyBytes = 4 << 20

// Options configures a Server.
type Options struct {
	// DataDir is the workspace data directory; settings.json lives here.
	DataDir string
	// Actor is stamped on every write. Empty defaults to "local". Phase 3
	// replaces this with server-side sessions (DESIGN.md §6).
	Actor string
	// Verbose enables request logging (method, path, status, duration).
	Verbose bool
	// LogWriter receives request logs and panic reports; default os.Stderr.
	LogWriter io.Writer
}

// Server serves the HTTP API over one open workspace.
type Server struct {
	w        *writer.Writer
	st       *store.Store
	actor    string
	verbose  bool
	logger   *log.Logger
	settings *settingsFile
	hub      *sseHub

	quitOnce sync.Once
	quit     chan struct{} // closed by Shutdown; ends SSE streams

	// testRoutes registers extra handlers under /api/v1/test/{name} — a
	// test-only seam (panic route, slow route); nil in production.
	testRoutes map[string]http.HandlerFunc
}

// New returns a Server for w and st. It installs the writer's commit hook to
// feed the SSE stream; the hook is owned by this server from then on.
func New(w *writer.Writer, st *store.Store, opts Options) *Server {
	if opts.Actor == "" {
		opts.Actor = "local"
	}
	if opts.LogWriter == nil {
		opts.LogWriter = os.Stderr
	}
	s := &Server{
		w:        w,
		st:       st,
		actor:    opts.Actor,
		verbose:  opts.Verbose,
		logger:   log.New(opts.LogWriter, "churn: ", log.LstdFlags),
		settings: newSettingsFile(opts.DataDir),
		hub:      newSSEHub(),
		quit:     make(chan struct{}),
	}
	w.SetCommitHook(s.hub.notify)
	return s
}

// Shutdown ends the server's long-lived streams (SSE) so an enclosing
// http.Server.Shutdown can drain the remaining in-flight requests. Safe to
// call more than once.
func (s *Server) Shutdown() {
	s.quitOnce.Do(func() { close(s.quit) })
}

// Handler returns the root HTTP handler: the full /api/v1 surface wrapped in
// the hygiene middleware (panic recovery, request logging, body size limit,
// JSON error envelopes for mux-generated 404/405).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/workspace", s.getWorkspace)

	// Entity CRUD (§5.1). Dependencies have no PATCH: the event catalog has
	// no dependency.superseded — an edge is retracted and re-asserted.
	for _, k := range s.kinds() {
		s.registerKind(mux, k)
	}
	mux.HandleFunc("PATCH /api/v1/dependencies/{id}", func(rw http.ResponseWriter, r *http.Request) {
		writeError(rw, &apiError{
			status: http.StatusMethodNotAllowed, kind: "method_not_allowed",
			message: "dependencies have no supersession (§5.2 catalog): retract and re-assert instead",
		})
	})

	// Resource sub-facts: availability toggle and capability grants — events
	// of the §5.2 catalog with no other §5.1 home.
	mux.HandleFunc("POST /api/v1/resources/{id}/availability", s.postAvailability)
	mux.HandleFunc("POST /api/v1/resources/{id}/capabilities", s.postGrant)
	mux.HandleFunc("DELETE /api/v1/resources/{id}/capabilities/{cap}", s.deleteGrant)

	// Transitions (§2.5 propose→confirm) and the atomic re-propose.
	mux.HandleFunc("POST /api/v1/things/{id}/transition", s.postTransition)
	mux.HandleFunc("POST /api/v1/things/{id}/repropose", s.postRepropose)

	// Bulk substrate.
	mux.HandleFunc("POST /api/v1/batch", s.postBatch)

	// Graph and analytics.
	mux.HandleFunc("GET /api/v1/projects/{id}/graph", s.getGraph)
	mux.HandleFunc("GET /api/v1/analytics/ready", s.getReady)
	mux.HandleFunc("GET /api/v1/analytics/bottlenecks", s.getBottlenecks)
	mux.HandleFunc("GET /api/v1/analytics/recommendations", s.getRecommendations)
	mux.HandleFunc("GET /api/v1/analytics/resource-board", s.getResourceBoard)

	// History (the audit trail) and workspace settings.
	mux.HandleFunc("GET /api/v1/history", s.getHistory)
	mux.HandleFunc("GET /api/v1/settings", s.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.putSettings)

	// SSE commit notifications (phase-3 feature landed early: the writer's
	// commit hook is the natural seam; M6's UI stays trivially fresh).
	mux.HandleFunc("GET /api/v1/events/stream", s.getEventStream)

	for name, h := range s.testRoutes {
		mux.HandleFunc("/api/v1/test/"+name, h)
	}

	var h http.Handler = mux
	h = s.limitBody(h)
	h = jsonErrors(h)
	if s.verbose {
		h = s.logRequests(h)
	}
	h = s.recoverPanics(h)
	return h
}

// health reports the workspace identity and log position — enough for a
// liveness probe and for a human to confirm they hit the right workspace.
func (s *Server) health(rw http.ResponseWriter, _ *http.Request) {
	p := s.w.Projection()
	writeJSON(rw, http.StatusOK, struct {
		Status      string `json:"status"`
		WorkspaceID string `json:"workspace_id"`
		Origin      string `json:"origin"`
		LastSeq     int64  `json:"last_seq"`
	}{"ok", p.WorkspaceID, p.Origin, p.LastSeq})
}

// getWorkspace reports workspace identity, log position, and entity counts.
func (s *Server) getWorkspace(rw http.ResponseWriter, _ *http.Request) {
	p := s.w.Projection()
	openAllocs := 0
	for _, al := range p.Allocations {
		if al.Open {
			openAllocs++
		}
	}
	writeJSON(rw, http.StatusOK, workspaceDTO{
		WorkspaceID: p.WorkspaceID,
		Origin:      p.Origin,
		LastSeq:     p.LastSeq,
		LastTS:      p.LastTS,
		Counts: workspaceCounts{
			Projects:          len(p.Projects),
			Things:            len(p.Things),
			Resources:         len(p.Resources),
			Dependencies:      len(p.Dependencies),
			Requirements:      len(p.Requirements),
			States:            len(p.States),
			Types:             len(p.Types),
			Capabilities:      len(p.Capabilities),
			OpenAllocations:   openAllocs,
			ClosedAllocations: len(p.Allocations) - openAllocs,
		},
	})
}

// ── middleware ──

// recoverPanics converts a handler panic into the structured 500 envelope
// (unless a response is already underway, in which case the connection is
// all that can be sacrificed) and logs the panic.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ww := &statusWriter{ResponseWriter: rw}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.logger.Printf("panic serving %s %s: %v", r.Method, r.URL.Path, v)
				if !ww.wrote {
					writeError(ww, &apiError{
						status: http.StatusInternalServerError,
						kind:   "internal", message: "internal server error",
					})
				}
			}
		}()
		next.ServeHTTP(ww, r)
	})
}

// logRequests logs method, path, status, and duration for every request.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ww, ok := rw.(*statusWriter)
		if !ok {
			ww = &statusWriter{ResponseWriter: rw}
		}
		start := time.Now()
		next.ServeHTTP(ww, r)
		status := ww.status
		if status == 0 {
			status = http.StatusOK
		}
		s.logger.Printf("%s %s %d %s", r.Method, r.URL.Path, status, time.Since(start).Round(time.Microsecond))
	})
}

// limitBody caps every request body at maxBodyBytes; overruns surface as
// *http.MaxBytesError from decodeJSON and map to 413.
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(rw, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(rw, r)
	})
}

// statusWriter records the first status written, and whether anything was.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying flusher (SSE needs it through the
// middleware stack).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// jsonErrors rewrites non-JSON error responses generated below the handlers
// (the mux's plain-text 404/405) into the structured envelope, so that ALL
// non-2xx responses share one shape. Handler-written errors pass through
// untouched (they already carry application/json).
func jsonErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&jsonErrorWriter{rw: rw}, r)
	})
}

type jsonErrorWriter struct {
	rw         http.ResponseWriter
	substitute bool // a non-JSON error status was intercepted; body swallowed
	wrote      bool
}

func (w *jsonErrorWriter) Header() http.Header { return w.rw.Header() }

func (w *jsonErrorWriter) WriteHeader(code int) {
	if w.wrote {
		w.rw.WriteHeader(code)
		return
	}
	w.wrote = true
	ct := w.rw.Header().Get("Content-Type")
	if code >= 400 && ct != "application/json" && ct != "text/event-stream" {
		w.substitute = true
		kind := "http_error"
		switch code {
		case http.StatusNotFound:
			kind = "not_found"
		case http.StatusMethodNotAllowed:
			kind = "method_not_allowed"
		case http.StatusRequestEntityTooLarge:
			kind = "payload_too_large"
		}
		w.rw.Header().Set("Content-Type", "application/json")
		w.rw.WriteHeader(code)
		fmt.Fprintf(w.rw, `{"error":{"kind":%q,"message":%q}}`+"\n", kind, http.StatusText(code))
		return
	}
	w.rw.WriteHeader(code)
}

func (w *jsonErrorWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.substitute {
		return len(b), nil // drop the plain-text default body
	}
	return w.rw.Write(b)
}

// Flush forwards to the underlying flusher (SSE).
func (w *jsonErrorWriter) Flush() {
	if f, ok := w.rw.(http.Flusher); ok {
		f.Flush()
	}
}
