// Package server hosts the HTTP API (DESIGN.md §5.1). M4 ships the server
// skeleton and the health endpoint only; M5 registers the full /api/v1
// surface (and the embedded frontend) on the same handler.
package server

import (
	"encoding/json"
	"net/http"

	"churn/internal/writer"
)

// Server serves the HTTP API over one open workspace's writer.
type Server struct {
	w *writer.Writer
}

// New returns a Server for w.
func New(w *writer.Writer) *Server {
	return &Server{w: w}
}

// Handler returns the root HTTP handler. M5 plugs the /api/v1 entity,
// analytics, and history routes in here; M4 exposes only the health
// endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	return mux
}

// health reports the workspace identity and log position — enough for a
// liveness probe and for a human to confirm they hit the right workspace.
func (s *Server) health(rw http.ResponseWriter, _ *http.Request) {
	p := s.w.Projection()
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(struct {
		Status      string `json:"status"`
		WorkspaceID string `json:"workspace_id"`
		Origin      string `json:"origin"`
		LastSeq     int64  `json:"last_seq"`
	}{"ok", p.WorkspaceID, p.Origin, p.LastSeq})
}
