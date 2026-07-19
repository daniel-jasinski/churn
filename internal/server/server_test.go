package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"churn/internal/event"
	"churn/internal/store"
	"churn/internal/writer"
)

// stepClock hands out strictly increasing times, one second apart — every
// batch gets a distinct, known-order commit timestamp, which the as_of tests
// rely on.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

// env is one test server over a fresh workspace.
type env struct {
	t   *testing.T
	s   *Server
	ts  *httptest.Server
	w   *writer.Writer
	st  *store.Store
	dir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &stepClock{t: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)}
	w, err := writer.Open(st, writer.Options{Now: clock.now})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	s := New(w, st, Options{DataDir: dir, Actor: "tester"})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		s.Shutdown()
		w.Close()
		st.Close()
	})
	return &env{t: t, s: s, ts: ts, w: w, st: st, dir: dir}
}

// do issues one request; body values are marshaled to JSON.
func (e *env) do(method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp, b
}

// call issues a request, asserts the status, and decodes the response.
func (e *env) call(method, path string, body any, wantStatus int) map[string]any {
	e.t.Helper()
	resp, b := e.do(method, path, body, nil)
	if resp.StatusCode != wantStatus {
		e.t.Fatalf("%s %s: status %d, want %d; body %s", method, path, resp.StatusCode, wantStatus, b)
	}
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		e.t.Fatalf("%s %s: decoding %s: %v", method, path, b, err)
	}
	return m
}

// callList is call for endpoints returning a JSON array.
func (e *env) callList(method, path string, wantStatus int) []map[string]any {
	e.t.Helper()
	resp, b := e.do(method, path, nil, nil)
	if resp.StatusCode != wantStatus {
		e.t.Fatalf("%s %s: status %d, want %d; body %s", method, path, resp.StatusCode, wantStatus, b)
	}
	var l []map[string]any
	if err := json.Unmarshal(b, &l); err != nil {
		e.t.Fatalf("%s %s: decoding %s: %v", method, path, b, err)
	}
	return l
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// errKind extracts error.kind from an envelope.
func errKind(m map[string]any) string {
	if em, ok := m["error"].(map[string]any); ok {
		return str(em, "kind")
	}
	return ""
}

// errIDs extracts error.ids.
func errIDs(m map[string]any) []string {
	em, _ := m["error"].(map[string]any)
	raw, _ := em["ids"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// stateBySemantic finds a seeded default state id by semantic.
func (e *env) stateBySemantic(sem string) string {
	e.t.Helper()
	for _, st := range e.callList("GET", "/api/v1/vocab/states", 200) {
		if str(st, "semantic") == sem {
			return str(st, "id")
		}
	}
	e.t.Fatalf("no state with semantic %q", sem)
	return ""
}

// fixture is the common workspace: a project, a type, a capability, and the
// default states.
type fixture struct {
	project, typ, cap             string
	pending, active, done, paused string
}

func (e *env) seed() fixture {
	e.t.Helper()
	f := fixture{
		pending: e.stateBySemantic("pending"),
		active:  e.stateBySemantic("active"),
		done:    e.stateBySemantic("satisfied"),
		paused:  e.stateBySemantic("paused"),
	}
	f.project = str(e.call("POST", "/api/v1/projects", map[string]any{"name": "Alpha"}, 201), "id")
	f.typ = str(e.call("POST", "/api/v1/vocab/types", map[string]any{"name": "task"}, 201), "id")
	f.cap = str(e.call("POST", "/api/v1/vocab/capabilities", map[string]any{"name": "review"}, 201), "id")
	return f
}

// thing creates a leaf thing in the fixture project.
func (e *env) thing(f fixture, name string) string {
	e.t.Helper()
	return str(e.call("POST", "/api/v1/things", map[string]any{
		"project": f.project, "name": name, "type": f.typ,
	}, 201), "id")
}

// resource creates a resource carrying the fixture capability.
func (e *env) resource(f fixture, name string, capacity int, named bool) string {
	e.t.Helper()
	id := str(e.call("POST", "/api/v1/resources", map[string]any{
		"name": name, "kind": "reusable", "named": named, "capacity": capacity,
	}, 201), "id")
	e.call("POST", "/api/v1/resources/"+id+"/capabilities", map[string]any{"capability": f.cap}, 200)
	return id
}

// requirement asserts a capability requirement on a thing.
func (e *env) requirement(f fixture, thing string, quantity int) string {
	e.t.Helper()
	return str(e.call("POST", "/api/v1/requirements", map[string]any{
		"thing": thing, "quantity": quantity, "capabilities": []string{f.cap},
	}, 201), "id")
}

// lastSeq reads the workspace log position.
func (e *env) lastSeq() int64 {
	e.t.Helper()
	m := e.call("GET", "/api/v1/workspace", nil, 200)
	return int64(m["last_seq"].(float64))
}

// TestEndToEndFlow drives the whole §5.1 surface once: vocab → project →
// things → dependency → requirement → resource → transition propose/confirm
// → completion, checking derived statuses along the way.
func TestEndToEndFlow(t *testing.T) {
	e := newEnv(t)
	f := e.seed()

	a := e.thing(f, "build")
	b := e.thing(f, "ship")
	dep := e.call("POST", "/api/v1/dependencies", map[string]any{"from": b, "to": a}, 201)
	if str(dep, "from") != b || dep["satisfied"] != false {
		t.Fatalf("dependency DTO: %v", dep)
	}
	req := e.requirement(f, a, 1)
	rs := e.resource(f, "alice", 1, true)

	// Derived statuses: A ready, B blocked.
	if got := str(e.call("GET", "/api/v1/things/"+a, nil, 200), "status"); got != "ready" {
		t.Fatalf("thing A status %q, want ready", got)
	}
	if got := str(e.call("GET", "/api/v1/things/"+b, nil, 200), "status"); got != "blocked" {
		t.Fatalf("thing B status %q, want blocked", got)
	}

	// Transition A into the active state: propose leg.
	prop := e.call("POST", "/api/v1/things/"+a+"/transition", map[string]any{"state": f.active}, 200)
	if prop["committed"] != false {
		t.Fatalf("propose leg committed: %v", prop)
	}
	proposal := prop["proposal"].(map[string]any)
	allocs := proposal["allocations"].([]any)
	if len(allocs) != 1 {
		t.Fatalf("proposal allocations: %v", allocs)
	}
	row := allocs[0].(map[string]any)
	if str(row, "requirement") != req || str(row, "resource") != rs {
		t.Fatalf("proposed row %v, want requirement %s on resource %s", row, req, rs)
	}

	// Confirm leg: transition + allocation as one batch.
	conf := e.call("POST", "/api/v1/things/"+a+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(proposal, "token"),
	}, 200)
	if conf["committed"] != true || len(conf["opened"].([]any)) != 1 {
		t.Fatalf("confirm: %v", conf)
	}
	if got := str(e.call("GET", "/api/v1/things/"+a, nil, 200), "status"); got != "working" {
		t.Fatalf("thing A status %q, want working", got)
	}
	rsDTO := e.call("GET", "/api/v1/resources/"+rs, nil, 200)
	if rsDTO["free"].(float64) != 0 || rsDTO["allocated"].(float64) != 1 {
		t.Fatalf("resource after confirm: %v", rsDTO)
	}

	// The confirm batch is atomic: transition and allocation share a batch.
	batch := str(conf, "batch")
	hist := e.call("GET", "/api/v1/history?batch="+batch, nil, 200)
	events := hist["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("confirm batch has %d events, want 2", len(events))
	}

	// Finish A: direct commit, auto-closing the allocation in the same batch.
	fin := e.call("POST", "/api/v1/things/"+a+"/transition", map[string]any{"state": f.done}, 200)
	if fin["committed"] != true || len(fin["closed"].([]any)) != 1 {
		t.Fatalf("finish: %v", fin)
	}
	if got := str(e.call("GET", "/api/v1/things/"+b, nil, 200), "status"); got != "ready" {
		t.Fatalf("thing B status %q, want ready after A done", got)
	}
	rsDTO = e.call("GET", "/api/v1/resources/"+rs, nil, 200)
	if rsDTO["free"].(float64) != 1 {
		t.Fatalf("resource not freed: %v", rsDTO)
	}

	// Analytics answer over the same projection.
	ready := e.callList("GET", "/api/v1/analytics/ready", 200)
	if len(ready) != 1 || str(ready[0], "thing") != b {
		t.Fatalf("ready list: %v", ready)
	}
	if got := e.callList("GET", "/api/v1/analytics/ready?project="+f.project+"&type="+f.typ, 200); len(got) != 1 {
		t.Fatalf("filtered ready list: %v", got)
	}
	board := e.callList("GET", "/api/v1/analytics/resource-board", 200)
	if len(board) != 1 {
		t.Fatalf("resource board: %v", board)
	}
	e.call("GET", "/api/v1/analytics/bottlenecks", nil, 200)
	e.call("GET", "/api/v1/analytics/recommendations", nil, 200)

	// Graph (live).
	g := e.call("GET", "/api/v1/projects/"+f.project+"/graph", nil, 200)
	if len(g["things"].([]any)) != 2 || len(g["edges"].([]any)) != 1 {
		t.Fatalf("graph: %v", g)
	}
	edge := g["edges"].([]any)[0].(map[string]any)
	if str(edge, "from") != b || str(edge, "to") != a || edge["declared"] != true {
		t.Fatalf("edge: %v", edge)
	}
}

// TestCRUDLifecycle exercises PATCH (full replacement), DELETE, 404s, and
// version reporting on a couple of kinds.
func TestCRUDLifecycle(t *testing.T) {
	e := newEnv(t)
	f := e.seed()

	// PATCH is full replacement: name only is the complete project set.
	pr := e.call("PATCH", "/api/v1/projects/"+f.project, map[string]any{"name": "Beta"}, 200)
	if str(pr, "name") != "Beta" {
		t.Fatalf("supersede: %v", pr)
	}

	// Unknown fields are rejected (strict decoding — no true patches).
	resp, body := e.do("PATCH", "/api/v1/projects/"+f.project, map[string]any{"name": "x", "bogus": 1}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("unknown field: %d %s", resp.StatusCode, body)
	}

	// Missing required fields are rejected (payload validation).
	resp, _ = e.do("PATCH", "/api/v1/projects/"+f.project, map[string]any{}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("empty supersede accepted: %d", resp.StatusCode)
	}

	// GET/PATCH/DELETE of unknown ids are 404 with the envelope.
	for _, m := range []string{"GET", "DELETE"} {
		resp, b := e.do(m, "/api/v1/projects/pr_missing", nil, nil)
		if resp.StatusCode != 404 {
			t.Fatalf("%s missing: %d %s", m, resp.StatusCode, b)
		}
		var env map[string]any
		if err := json.Unmarshal(b, &env); err != nil || errKind(env) != "not_found" {
			t.Fatalf("%s missing envelope: %s", m, b)
		}
	}

	// DELETE blocked while referenced → 409 retraction_blocked, ids listed.
	th := e.thing(f, "solo")
	m := e.call("DELETE", "/api/v1/projects/"+f.project, nil, 409)
	if errKind(m) != "retraction_blocked" {
		t.Fatalf("delete referenced project: %v", m)
	}
	if ids := errIDs(m); len(ids) != 1 || ids[0] != th {
		t.Fatalf("retraction_blocked ids: %v, want [%s]", errIDs(m), th)
	}

	// DELETE the thing, then the project.
	e.call("DELETE", "/api/v1/things/"+th, nil, 200)
	e.call("DELETE", "/api/v1/projects/"+f.project, nil, 200)
	if got := e.callList("GET", "/api/v1/projects", 200); len(got) != 0 {
		t.Fatalf("projects after delete: %v", got)
	}

	// Dependencies have no PATCH.
	resp, b := e.do("PATCH", "/api/v1/dependencies/dep_x", map[string]any{}, nil)
	if resp.StatusCode != 405 {
		t.Fatalf("PATCH dependency: %d %s", resp.StatusCode, b)
	}
	var env2 map[string]any
	if err := json.Unmarshal(b, &env2); err != nil || errKind(env2) != "method_not_allowed" {
		t.Fatalf("PATCH dependency envelope: %s", b)
	}
}

// TestListDeterminism asserts documented orderings: entity lists ascend by
// id, and responses carry the JSON content type.
func TestListDeterminism(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	for i := 0; i < 5; i++ {
		e.thing(f, fmt.Sprintf("thing-%d", i))
	}
	resp, b := e.do("GET", "/api/v1/things", nil, nil)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	var list []map[string]any
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 {
		t.Fatalf("things: %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if str(list[i-1], "id") >= str(list[i], "id") {
			t.Fatalf("things list not ascending by id at %d", i)
		}
	}
	states := e.callList("GET", "/api/v1/vocab/states", 200)
	for i := 1; i < len(states); i++ {
		if str(states[i-1], "id") >= str(states[i], "id") {
			t.Fatalf("states list not ascending by id at %d", i)
		}
	}
}

// TestWorkspaceAndHealth sanity-checks the identity endpoints.
func TestWorkspaceAndHealth(t *testing.T) {
	e := newEnv(t)
	h := e.call("GET", "/api/v1/health", nil, 200)
	if str(h, "status") != "ok" || !strings.HasPrefix(str(h, "workspace_id"), event.PrefixWorkspace) {
		t.Fatalf("health: %v", h)
	}
	w := e.call("GET", "/api/v1/workspace", nil, 200)
	counts := w["counts"].(map[string]any)
	if counts["states"].(float64) != 5 {
		t.Fatalf("workspace counts: %v", counts)
	}
	if w["last_seq"].(float64) < 6 {
		t.Fatalf("workspace last_seq: %v", w)
	}
}
