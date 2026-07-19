// Static frontend serving (DESIGN.md §5, PLAN.md M6): the embedded web/dist
// bundle behind every non-/api path.
//
//	/                  → index.html          Cache-Control: no-store
//	/assets/<hashed>   → js/css/map bundle   Cache-Control: immutable (names
//	                                         carry a content hash)
//	/assets/<missing>  → 404 JSON envelope   (an asset URL is never a SPA
//	                                         route; falling back to HTML
//	                                         would poison bundle fetches)
//	other existing file → served, no-store
//	anything else      → index.html          (SPA hash-routing fallback;
//	                                         /api/* never reaches here — the
//	                                         Handler routes it to the mux,
//	                                         keeping the JSON 404/405)
//
// Every static response carries X-Content-Type-Options: nosniff and a
// self-only Content-Security-Policy (data: images allowed for the inline
// favicon; all styling is CSSOM/external, so no 'unsafe-inline').

package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"churn/web"
)

// staticTypes maps bundle extensions to explicit content types — Windows'
// registry-backed mime.TypeByExtension is not trusted to be deterministic.
var staticTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".map":  "application/json",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".png":  "image/png",
	".json": "application/json",
	".txt":  "text/plain; charset=utf-8",
}

// staticHandler serves the embedded frontend. Handler() routes only
// non-/api paths here; /api stays on the mux so unknown API routes keep
// their JSON 404/405 envelope and never hit the SPA fallback.
func (s *Server) staticHandler() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic("server: embedded web/dist missing: " + err.Error())
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'self'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			rw.Header().Set("Allow", "GET, HEAD")
			writeError(rw, &apiError{status: http.StatusMethodNotAllowed,
				kind: "method_not_allowed", message: "static content is read-only"})
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "." && name != ".buildstamp" {
			if b, err := fs.ReadFile(dist, name); err == nil {
				if strings.HasPrefix(name, "assets/") {
					// Hashed names: a changed bundle is a changed URL.
					rw.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					rw.Header().Set("Cache-Control", "no-store")
				}
				serveBytes(rw, r, name, b)
				return
			}
		}
		if strings.HasPrefix(name, "assets/") {
			// A missing asset is a real 404 (JSON envelope, like every other
			// non-2xx here) — never the SPA shell masquerading as a bundle.
			writeError(rw, &apiError{status: http.StatusNotFound, kind: "not_found",
				message: "no such asset: /" + name})
			return
		}

		// SPA fallback: any other path is the app shell.
		b, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			writeError(rw, &apiError{status: http.StatusInternalServerError,
				kind: "internal", message: "embedded index.html missing"})
			return
		}
		rw.Header().Set("Cache-Control", "no-store")
		serveBytes(rw, r, "index.html", b)
	})
}

// serveBytes writes b with the explicit content type for name's extension.
// http.ServeContent handles HEAD, ranges, and If-* preconditions.
func serveBytes(rw http.ResponseWriter, r *http.Request, name string, b []byte) {
	ct := staticTypes[strings.ToLower(path.Ext(name))]
	if ct == "" {
		ct = "application/octet-stream"
	}
	rw.Header().Set("Content-Type", ct)
	http.ServeContent(rw, r, "", time.Time{}, bytes.NewReader(b))
}
