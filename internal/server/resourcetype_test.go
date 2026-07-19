package server

import (
	"strings"
	"testing"
)

// TestResourceTypeVocabCRUD drives /api/v1/vocab/resource-types and the
// optional type reference on resources: CRUD, declared-before-use (422
// undefined_reference), blocked retraction (409 retraction_blocked with the
// referencing resource ids), and the /batch placeholder flow.
func TestResourceTypeVocabCRUD(t *testing.T) {
	e := newEnv(t)

	// Create.
	rt := e.call("POST", "/api/v1/vocab/resource-types",
		map[string]any{"name": "person", "color": "#123", "description": "humans"}, 201)
	id := str(rt, "id")
	if !strings.HasPrefix(id, "rt_") {
		t.Fatalf("resource type id %q, want rt_ prefix", id)
	}
	if str(rt, "name") != "person" || str(rt, "color") != "#123" {
		t.Fatalf("created DTO: %v", rt)
	}

	// List and get.
	if l := e.callList("GET", "/api/v1/vocab/resource-types", 200); len(l) != 1 || str(l[0], "id") != id {
		t.Fatalf("list: %v", l)
	}
	e.call("GET", "/api/v1/vocab/resource-types/"+id, nil, 200)

	// Missing name is a 400 payload-shape rejection.
	e.call("POST", "/api/v1/vocab/resource-types", map[string]any{"color": "#fff"}, 400)

	// Supersede (full replacement) is free.
	up := e.call("PATCH", "/api/v1/vocab/resource-types/"+id, map[string]any{"name": "human"}, 200)
	if str(up, "name") != "human" || str(up, "color") != "" {
		t.Fatalf("superseded DTO: %v", up)
	}

	// A resource carrying the type; the DTO echoes it.
	rs := e.call("POST", "/api/v1/resources",
		map[string]any{"name": "Anna", "kind": "reusable", "named": true, "capacity": 1, "type": id}, 201)
	rsID := str(rs, "id")
	if str(rs, "type") != id {
		t.Fatalf("resource DTO type = %q, want %s", str(rs, "type"), id)
	}

	// Declared-before-use: an undefined type is 422 undefined_reference.
	m := e.call("POST", "/api/v1/resources",
		map[string]any{"name": "X", "kind": "reusable", "capacity": 1, "type": "rt_ghost"}, 422)
	if errKind(m) != "undefined_reference" {
		t.Fatalf("undefined type envelope: %v", m)
	}

	// Retraction while referenced: 409 retraction_blocked naming the resource.
	m = e.call("DELETE", "/api/v1/vocab/resource-types/"+id, nil, 409)
	if errKind(m) != "retraction_blocked" {
		t.Fatalf("blocked retraction envelope: %v", m)
	}
	if ids := errIDs(m); len(ids) != 1 || ids[0] != rsID {
		t.Fatalf("blocked retraction ids = %v, want [%s]", ids, rsID)
	}

	// Supersede the resource without the type, then retraction succeeds.
	e.call("PATCH", "/api/v1/resources/"+rsID,
		map[string]any{"name": "Anna", "kind": "reusable", "named": true, "capacity": 1}, 200)
	e.call("DELETE", "/api/v1/vocab/resource-types/"+id, nil, 200)
	e.call("GET", "/api/v1/vocab/resource-types/"+id, nil, 404)

	// Workspace counts carry the registry size.
	e.call("POST", "/api/v1/vocab/resource-types", map[string]any{"name": "room"}, 201)
	w := e.call("GET", "/api/v1/workspace", nil, 200)
	counts := w["counts"].(map[string]any)
	if got := counts["resource_types"].(float64); got != 1 {
		t.Fatalf("counts.resource_types = %v, want 1", got)
	}

	// /batch: create a resource type and a resource referencing it via the
	// "$0" placeholder in one atomic batch.
	b := e.call("POST", "/api/v1/batch", map[string]any{
		"mode": "commit",
		"operations": []map[string]any{
			{"op": "create", "kind": "resource_type", "data": map[string]any{"name": "license"}},
			{"op": "create", "kind": "resource", "data": map[string]any{
				"name": "IDE seat", "kind": "reusable", "capacity": 3, "type": "$0"}},
		},
	}, 200)
	ph := b["placeholders"].(map[string]any)
	rtBatch := ph["$0"].(string)
	if !strings.HasPrefix(rtBatch, "rt_") {
		t.Fatalf("batch-minted resource type id %q", rtBatch)
	}
	results := b["results"].([]any)
	rsBatch := str(results[1].(map[string]any), "id")
	got := e.call("GET", "/api/v1/resources/"+rsBatch, nil, 200)
	if str(got, "type") != rtBatch {
		t.Fatalf("batch-created resource type = %q, want %s", str(got, "type"), rtBatch)
	}
}

// TestVocabMetadataFieldDeclarations drives declared metadata fields (§5.3)
// on both vocab kinds that carry them: CRUD round-trip with kind
// normalization, full-replacement supersession dropping them, 400 on shape
// violations — and the permissive boundary: instance metadata is never
// checked against the declarations.
func TestVocabMetadataFieldDeclarations(t *testing.T) {
	e := newEnv(t)
	f := e.seed()

	fields := []map[string]any{
		{"key": "prio", "label": "Priority", "kind": "select", "options": []string{"low", "high"}, "required": true},
		{"key": "ref"}, // kind defaults to text
	}
	ty := e.call("POST", "/api/v1/vocab/types", map[string]any{"name": "ticket", "fields": fields}, 201)
	tyID := str(ty, "id")
	got := ty["fields"].([]any)
	if len(got) != 2 {
		t.Fatalf("created type fields: %v", got)
	}
	first, second := got[0].(map[string]any), got[1].(map[string]any)
	if str(first, "key") != "prio" || str(first, "kind") != "select" ||
		first["required"] != true || len(first["options"].([]any)) != 2 {
		t.Fatalf("fields[0]: %v", first)
	}
	if str(second, "key") != "ref" || str(second, "kind") != "text" {
		t.Fatalf("fields[1]: %v, want kind normalized to text", second)
	}

	// Resource types accept the same declarations.
	rt := e.call("POST", "/api/v1/vocab/resource-types",
		map[string]any{"name": "machine", "fields": []map[string]any{{"key": "room", "kind": "date"}}}, 201)
	if fl := rt["fields"].([]any); len(fl) != 1 || str(fl[0].(map[string]any), "kind") != "date" {
		t.Fatalf("resource type fields: %v", rt)
	}

	// Shape violations are 400s: duplicate keys, bad kind, options without
	// select, select without options.
	for name, bad := range map[string]any{
		"dup keys":            []map[string]any{{"key": "a"}, {"key": "a"}},
		"bad kind":            []map[string]any{{"key": "a", "kind": "toggle"}},
		"options sans select": []map[string]any{{"key": "a", "kind": "text", "options": []string{"x"}}},
		"select sans options": []map[string]any{{"key": "a", "kind": "select"}},
	} {
		resp, b := e.do("POST", "/api/v1/vocab/types", map[string]any{"name": "bad", "fields": bad}, nil)
		if resp.StatusCode != 400 {
			t.Fatalf("%s: status %d %s, want 400", name, resp.StatusCode, b)
		}
	}

	// Declarations drive forms only: a thing whose metadata ignores them —
	// missing required prio, wrong-kind ref — is accepted (the log stays
	// permissive, §5.3).
	th := e.call("POST", "/api/v1/things", map[string]any{
		"project": f.project, "name": "T", "type": tyID,
		"metadata": map[string]any{"ref": 42, "stray": true},
	}, 201)
	if th["metadata"] == nil {
		t.Fatalf("thing DTO: %v, want metadata echoed", th)
	}

	// Supersession is full replacement: PATCH without fields drops them.
	up := e.call("PATCH", "/api/v1/vocab/types/"+tyID, map[string]any{"name": "ticket"}, 200)
	if _, has := up["fields"]; has {
		t.Fatalf("superseded type: %v, want fields dropped (full replacement)", up)
	}
}
