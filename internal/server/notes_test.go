package server

import "testing"

// TestNotesCRUD drives the note surface: create against a thing (author +
// created stamps set server-side), list filtered by thing, edit (edited
// stamps appear, author preserved), and delete.
func TestNotesCRUD(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	a := e.thing(f, "build")
	b := e.thing(f, "ship")

	// Create two notes on a, one on b.
	n1 := e.call("POST", "/api/v1/notes", map[string]any{"thing": a, "body": "first"}, 201)
	id1 := str(n1, "id")
	if str(n1, "thing") != a || str(n1, "body") != "first" {
		t.Fatalf("note DTO: %v", n1)
	}
	if str(n1, "author") != "tester" {
		t.Fatalf("author = %q, want the server actor", str(n1, "author"))
	}
	if str(n1, "created_ts") == "" || n1["created_seq"] == nil {
		t.Fatalf("created stamps missing: %v", n1)
	}
	if _, ok := n1["edited_ts"]; ok {
		t.Fatalf("fresh note must not carry edited_ts: %v", n1)
	}
	e.call("POST", "/api/v1/notes", map[string]any{"thing": a, "body": "second"}, 201)
	e.call("POST", "/api/v1/notes", map[string]any{"thing": b, "body": "other"}, 201)

	// Filter by thing.
	onA := e.callList("GET", "/api/v1/notes?thing="+a, 200)
	if len(onA) != 2 {
		t.Fatalf("notes on a = %d, want 2", len(onA))
	}
	onB := e.callList("GET", "/api/v1/notes?thing="+b, 200)
	if len(onB) != 1 || str(onB[0], "body") != "other" {
		t.Fatalf("notes on b = %v", onB)
	}
	if all := e.callList("GET", "/api/v1/notes", 200); len(all) != 3 {
		t.Fatalf("all notes = %d, want 3", len(all))
	}

	// Edit note 1: body changes, edited stamps appear, author unchanged.
	ed := e.call("PATCH", "/api/v1/notes/"+id1, map[string]any{"body": "first (edited)"}, 200)
	if str(ed, "body") != "first (edited)" {
		t.Fatalf("edit body: %v", ed)
	}
	if str(ed, "author") != "tester" {
		t.Fatalf("author changed on edit: %v", ed)
	}
	if str(ed, "edited_ts") == "" {
		t.Fatalf("edited note must carry edited_ts: %v", ed)
	}

	// Delete note 1.
	e.call("DELETE", "/api/v1/notes/"+id1, nil, 200)
	if got := e.callList("GET", "/api/v1/notes?thing="+a, 200); len(got) != 1 {
		t.Fatalf("after delete, notes on a = %d, want 1", len(got))
	}
}

// TestNoteOnMissingThing: a note referencing a non-existent thing is a 422
// undefined_reference (declared-before-use, like every other reference).
func TestNoteOnMissingThing(t *testing.T) {
	e := newEnv(t)
	e.seed()
	m := e.call("POST", "/api/v1/notes", map[string]any{"thing": "th_nope", "body": "x"}, 422)
	if errKind(m) != "undefined_reference" {
		t.Fatalf("kind = %q, want undefined_reference", errKind(m))
	}
}

// TestThingRetractionBlockedByNote: the API surfaces the domain rule that a
// note blocks its thing's retraction, naming the note id.
func TestThingRetractionBlockedByNote(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	a := e.thing(f, "build")
	note := str(e.call("POST", "/api/v1/notes", map[string]any{"thing": a, "body": "hold"}, 201), "id")

	m := e.call("DELETE", "/api/v1/things/"+a, nil, 409)
	if errKind(m) != "retraction_blocked" {
		t.Fatalf("kind = %q, want retraction_blocked", errKind(m))
	}
	if ids := errIDs(m); len(ids) != 1 || ids[0] != note {
		t.Fatalf("blocking ids = %v, want [%s]", ids, note)
	}

	// Remove the note, then the thing deletes cleanly.
	e.call("DELETE", "/api/v1/notes/"+note, nil, 200)
	e.call("DELETE", "/api/v1/things/"+a, nil, 200)
}

// TestNoteEmptyBodyRejected: an empty body is a 400 bad_request from payload
// validation.
func TestNoteEmptyBodyRejected(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	a := e.thing(f, "build")
	m := e.call("POST", "/api/v1/notes", map[string]any{"thing": a, "body": ""}, 400)
	if errKind(m) != "bad_request" {
		t.Fatalf("kind = %q, want bad_request", errKind(m))
	}
}
