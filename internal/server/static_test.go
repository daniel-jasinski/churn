package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"churn/web"
)

// TestStaticIndex: / serves the app shell with no-store (a stale shell must
// never pin an old bundle name).
func TestStaticIndex(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(http.MethodGet, "/", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET /: content type %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("GET /: Cache-Control %q, want no-store", cc)
	}
	if !strings.Contains(string(body), `<div id="app">`) {
		t.Fatalf("GET /: body is not the app shell:\n%s", body)
	}
}

// TestStaticAssets: every embedded bundle asset is served with the correct
// MIME type and the immutable cache header (names are content-hashed).
func TestStaticAssets(t *testing.T) {
	e := newEnv(t)
	entries, err := fs.ReadDir(web.Dist, "dist/assets")
	if err != nil {
		t.Fatalf("embedded dist/assets: %v", err)
	}
	wantType := map[string]string{
		".js":  "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8",
		".map": "application/json",
	}
	var checked int
	for _, en := range entries {
		name := en.Name()
		ext := name[strings.LastIndex(name, "."):]
		want, ok := wantType[ext]
		if !ok {
			continue
		}
		resp, body := e.do(http.MethodGet, "/assets/"+name, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /assets/%s: status %d", name, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != want {
			t.Errorf("GET /assets/%s: content type %q, want %q", name, ct, want)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("GET /assets/%s: Cache-Control %q, want immutable", name, cc)
		}
		if len(body) == 0 {
			t.Errorf("GET /assets/%s: empty body", name)
		}
		checked++
	}
	if checked < 2 { // at least the js and css bundles
		t.Fatalf("checked only %d assets — is dist/ built?", checked)
	}
}

// TestStaticSPAFallback: unknown non-API paths serve index.html (hash
// routing), while unknown /api paths keep the JSON 404 envelope.
func TestStaticSPAFallback(t *testing.T) {
	e := newEnv(t)

	resp, body := e.do(http.MethodGet, "/some/deep/route", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /some/deep/route: status %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `<div id="app">`) {
		t.Fatalf("GET /some/deep/route: not the SPA fallback")
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("fallback Cache-Control %q, want no-store", cc)
	}

	for _, p := range []string{"/api/v1/definitely-not-a-route", "/api/nope"} {
		resp, body := e.do(http.MethodGet, p, nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: status %d, want 404", p, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("GET %s: content type %q, want application/json", p, ct)
		}
		var env struct {
			Error struct {
				Kind string `json:"kind"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err != nil || env.Error.Kind != "not_found" {
			t.Fatalf("GET %s: not the JSON 404 envelope: %s (err %v)", p, body, err)
		}
	}
}

// TestStaticMissingAsset: an unmatched /assets path is a real 404 (JSON
// envelope), never the SPA shell masquerading as a bundle.
func TestStaticMissingAsset(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(http.MethodGet, "/assets/app-DEADBEEF.js", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset: status %d, want 404: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("missing asset: content type %q, want the JSON envelope", ct)
	}
	if !strings.Contains(string(body), "not_found") {
		t.Fatalf("missing asset: body %s", body)
	}
}

// TestStaticSecurityHeaders: every static response carries nosniff and the
// self-only CSP.
func TestStaticSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/", "/some/spa/route"} {
		resp, _ := e.do(http.MethodGet, p, nil, nil)
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options %q", p, got)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "img-src 'self' data:") {
			t.Errorf("GET %s: CSP %q", p, csp)
		}
	}
}

// TestStaticMethodNotAllowed: mutations against static paths are refused
// with the JSON envelope.
func TestStaticMethodNotAllowed(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(http.MethodPost, "/", map[string]string{"x": "y"}, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: status %d, want 405", resp.StatusCode)
	}
	if !strings.Contains(string(body), "method_not_allowed") {
		t.Fatalf("POST /: body %s", body)
	}
}

// TestStaticHead: HEAD works for the shell (ServeContent path).
func TestStaticHead(t *testing.T) {
	e := newEnv(t)
	req, err := http.NewRequest(http.MethodHead, e.ts.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || len(b) != 0 {
		t.Fatalf("HEAD /: status %d, body %d bytes", resp.StatusCode, len(b))
	}
}
