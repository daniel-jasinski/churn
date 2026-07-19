package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestOversizedBody: a body over the 4 MiB cap is 413 with the envelope.
func TestOversizedBody(t *testing.T) {
	e := newEnv(t)
	big := `{"name":"` + strings.Repeat("x", maxBodyBytes+16) + `"}`
	req, err := http.NewRequest("POST", e.ts.URL+"/api/v1/projects", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d %s", resp.StatusCode, b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || errKind(m) != "payload_too_large" {
		t.Fatalf("oversized envelope: %s", b)
	}
}

// TestWrongContentType: a mutation without application/json is 415.
func TestWrongContentType(t *testing.T) {
	e := newEnv(t)
	for _, ct := range []string{"text/plain", "application/xml", ""} {
		req, err := http.NewRequest("POST", e.ts.URL+"/api/v1/projects", strings.NewReader(`{"name":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("content type %q: %d %s", ct, resp.StatusCode, b)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil || errKind(m) != "unsupported_media_type" {
			t.Fatalf("415 envelope for %q: %s", ct, b)
		}
	}
	// application/json with a charset parameter is accepted.
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/projects",
		strings.NewReader(`{"name":"charset ok"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("charset variant refused: %d", resp.StatusCode)
	}
}

// TestPanicRecovery: a panicking handler yields the structured 500 envelope,
// and the server keeps serving.
func TestPanicRecovery(t *testing.T) {
	dirEnv := newEnv(t)
	dirEnv.ts.Close()
	s := New(dirEnv.w, dirEnv.st, Options{DataDir: dirEnv.dir, Actor: "tester", LogWriter: io.Discard})
	s.testRoutes = map[string]http.HandlerFunc{
		"panic": func(http.ResponseWriter, *http.Request) { panic("kaboom") },
	}
	ts := httptestServer(t, s.Handler())

	resp, err := http.Get(ts + "/api/v1/test/panic")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("panic route: %d %s", resp.StatusCode, b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || errKind(m) != "internal" {
		t.Fatalf("panic envelope: %s", b)
	}
	// Still alive.
	resp, err = http.Get(ts + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("server dead after panic: %d", resp.StatusCode)
	}
}

// TestMuxErrorsAreEnvelopes: the mux's own 404 and 405 responses are
// rewritten into the JSON envelope (with Allow preserved on 405).
func TestMuxErrorsAreEnvelopes(t *testing.T) {
	e := newEnv(t)
	resp, b := e.do("GET", "/api/v1/nope", nil, nil)
	if resp.StatusCode != 404 || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("mux 404: %d %q %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || errKind(m) != "not_found" {
		t.Fatalf("mux 404 envelope: %s", b)
	}

	resp, b = e.do("DELETE", "/api/v1/batch", nil, nil)
	if resp.StatusCode != 405 || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("mux 405: %d %s", resp.StatusCode, b)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "POST") {
		t.Fatalf("405 Allow header: %q", allow)
	}
	if err := json.Unmarshal(b, &m); err != nil || errKind(m) != "method_not_allowed" {
		t.Fatalf("mux 405 envelope: %s", b)
	}
}

// TestGracefulShutdown: Shutdown drains an in-flight (slow) request to a
// clean 200 before returning.
func TestGracefulShutdown(t *testing.T) {
	e := newEnv(t)
	e.ts.Close()
	s := New(e.w, e.st, Options{DataDir: e.dir, Actor: "tester", LogWriter: io.Discard})
	release := make(chan struct{})
	entered := make(chan struct{})
	s.testRoutes = map[string]http.HandlerFunc{
		"slow": func(rw http.ResponseWriter, _ *http.Request) {
			close(entered)
			<-release
			rw.Header().Set("Content-Type", "application/json")
			rw.Write([]byte(`{"ok":true}`))
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler()}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	type result struct {
		status int
		body   []byte
		err    error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/api/v1/test/slow")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- result{status: resp.StatusCode, body: b}
	}()

	<-entered // the request is in flight
	shutDone := make(chan error, 1)
	go func() {
		s.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutDone <- srv.Shutdown(ctx)
	}()

	// Shutdown must WAIT for the in-flight request, not kill it.
	select {
	case <-shutDone:
		t.Fatal("Shutdown returned while a request was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	r := <-got
	if r.err != nil || r.status != 200 || !bytes.Contains(r.body, []byte("ok")) {
		t.Fatalf("in-flight request: %+v", r)
	}
	if err := <-shutDone; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	<-served // http.ErrServerClosed
}

// httptestServer starts a plain http.Server for handlers that need a real
// listener, cleaned up with the test.
func httptestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return "http://" + ln.Addr().String()
}
