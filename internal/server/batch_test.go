package server

import (
	"strconv"
	"testing"
)

// TestBatchPreviewVsCommit: preview validates without touching the log
// (seq unchanged), commit appends the same operations as one batch.
func TestBatchPreviewVsCommit(t *testing.T) {
	e := newEnv(t)
	f := e.seed()

	ops := []map[string]any{
		{"op": "create", "kind": "project", "data": map[string]any{"name": "Bulk"}},
		{"op": "create", "kind": "thing", "data": map[string]any{
			"project": f.project, "name": "bulk thing", "type": f.typ,
		}},
	}
	before := e.lastSeq()

	prev := e.call("POST", "/api/v1/batch", map[string]any{"mode": "preview", "operations": ops}, 200)
	if prev["committed"] != false || len(prev["results"].([]any)) != 2 {
		t.Fatalf("preview: %v", prev)
	}
	if got := e.lastSeq(); got != before {
		t.Fatalf("preview moved the log: %d → %d", before, got)
	}
	// The would-be ids are minted but nothing exists under them.
	previewID := str(prev["results"].([]any)[0].(map[string]any), "id")
	e.call("GET", "/api/v1/projects/"+previewID, nil, 404)

	com := e.call("POST", "/api/v1/batch", map[string]any{"mode": "commit", "operations": ops}, 200)
	if com["committed"] != true {
		t.Fatalf("commit: %v", com)
	}
	if got := e.lastSeq(); got != before+2 {
		t.Fatalf("commit advanced seq to %d, want %d", got, before+2)
	}
	newProject := str(com["results"].([]any)[0].(map[string]any), "id")
	e.call("GET", "/api/v1/projects/"+newProject, nil, 200)

	// Both events share the returned batch id.
	hist := e.call("GET", "/api/v1/history?batch="+str(com, "batch"), nil, 200)
	if n := len(hist["events"].([]any)); n != 2 {
		t.Fatalf("committed batch has %d events, want 2", n)
	}
}

// TestBatchPreviewRejectsInvalid: preview runs the full domain validation —
// a cycle is refused with the structured 409, and the log stays untouched.
func TestBatchPreviewRejectsInvalid(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	a := e.thing(f, "a")
	b := e.thing(f, "b")
	e.call("POST", "/api/v1/dependencies", map[string]any{"from": a, "to": b}, 201)

	before := e.lastSeq()
	m := e.call("POST", "/api/v1/batch", map[string]any{
		"mode": "preview",
		"operations": []map[string]any{
			{"op": "create", "kind": "dependency", "data": map[string]any{"from": b, "to": a}},
		},
	}, 409)
	if errKind(m) != "cycle" {
		t.Fatalf("preview cycle: %v", m)
	}
	if got := e.lastSeq(); got != before {
		t.Fatalf("failed preview moved the log: %d → %d", before, got)
	}
}

// TestBatchExpectedVersionsConflict: two clients read version v; the first
// write wins, the second batch is rejected 409 stale_version naming the id.
func TestBatchExpectedVersionsConflict(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	th := e.thing(f, "contested")
	v := e.call("GET", "/api/v1/things/"+th, nil, 200)["version"].(float64)

	sup := func(name string) map[string]any {
		return map[string]any{
			"mode":              "commit",
			"expected_versions": map[string]any{th: v},
			"operations": []map[string]any{
				{"op": "supersede", "kind": "thing", "id": th, "data": map[string]any{
					"name": name, "type": f.typ,
				}},
			},
		}
	}
	e.call("POST", "/api/v1/batch", sup("first wins"), 200)
	m := e.call("POST", "/api/v1/batch", sup("second loses"), 409)
	if errKind(m) != "stale_version" {
		t.Fatalf("stale batch: %v", m)
	}
	if ids := errIDs(m); len(ids) != 1 || ids[0] != th {
		t.Fatalf("stale ids %v, want [%s]", ids, th)
	}
	if got := str(e.call("GET", "/api/v1/things/"+th, nil, 200), "name"); got != "first wins" {
		t.Fatalf("thing name %q", got)
	}
}

// TestIfMatchConflict: the same optimistic-concurrency race through the
// single-entity PATCH endpoint's If-Match header.
func TestIfMatchConflict(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	th := e.thing(f, "contested")
	dto := e.call("GET", "/api/v1/things/"+th, nil, 200)
	v := int64(dto["version"].(float64))

	hdr := map[string]string{"If-Match": itoa(v)}
	body := map[string]any{"name": "client one", "type": f.typ}
	resp, _ := e.do("PATCH", "/api/v1/things/"+th, body, hdr)
	if resp.StatusCode != 200 {
		t.Fatalf("first PATCH: %d", resp.StatusCode)
	}
	body["name"] = "client two"
	resp, b := e.do("PATCH", "/api/v1/things/"+th, body, hdr)
	if resp.StatusCode != 409 {
		t.Fatalf("stale PATCH: %d %s", resp.StatusCode, b)
	}
}

// TestBatchOpValidation: bad kinds, ops, and payloads are rejected 400 with
// the op index in the message.
func TestBatchOpValidation(t *testing.T) {
	e := newEnv(t)
	e.seed()
	for _, tc := range []struct {
		name string
		op   map[string]any
	}{
		{"unknown kind", map[string]any{"op": "create", "kind": "widget", "data": map[string]any{}}},
		{"unknown op", map[string]any{"op": "merge", "kind": "project"}},
		{"dependency supersede", map[string]any{"op": "supersede", "kind": "dependency", "id": "dep_x", "data": map[string]any{}}},
		{"create with id", map[string]any{"op": "create", "kind": "project", "id": "pr_x", "data": map[string]any{"name": "n"}}},
		{"missing id", map[string]any{"op": "retract", "kind": "project"}},
		{"invalid payload", map[string]any{"op": "create", "kind": "project", "data": map[string]any{"name": ""}}},
	} {
		m := e.call("POST", "/api/v1/batch", map[string]any{
			"mode": "commit", "operations": []map[string]any{tc.op},
		}, 400)
		if errKind(m) != "bad_request" {
			t.Fatalf("%s: %v", tc.name, m)
		}
	}
	m := e.call("POST", "/api/v1/batch", map[string]any{"mode": "dryrun", "operations": []map[string]any{{}}}, 400)
	if errKind(m) != "bad_request" {
		t.Fatalf("bad mode: %v", m)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
