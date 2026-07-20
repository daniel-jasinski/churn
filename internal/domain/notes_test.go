package domain

import (
	"reflect"
	"testing"

	"churn/internal/event"
)

// TestNoteFoldCapturesEnvelope: note.added records the envelope's actor and
// ts; note.superseded replaces the body and stamps the edit, keeping the
// original author and creation stamps; note.retracted removes it.
func TestNoteFoldCapturesEnvelope(t *testing.T) {
	const t1 = "2026-07-19T10:00:01.000Z"
	log := []event.Envelope{
		initEv(1, t0),
		envE(2, t0, event.TypeStateDefined, "wr_1", "st_p", `{"name":"todo","semantic":"pending"}`),
		envE(3, t0, event.TypeTypeDefined, "wr_1", "ty_t", `{"name":"task"}`),
		envE(4, t0, event.TypeProjectCreated, "wr_1", "pr_p", `{"name":"Alpha"}`),
		envE(5, t0, event.TypeThingCreated, "wr_1", "th_x", `{"name":"X","project":"pr_p","type":"ty_t"}`),
		noteEv(6, t0, "author-a", "nt_1", "th_x", "first"),
	}
	p, err := Fold(log)
	if err != nil {
		t.Fatal(err)
	}
	nt := p.Notes["nt_1"]
	if nt == nil {
		t.Fatal("note nt_1 not folded")
	}
	if nt.Thing != "th_x" || nt.Body != "first" || nt.Author != "author-a" {
		t.Fatalf("note = %+v", nt)
	}
	if nt.CreatedTS != t0 || nt.CreatedSeq != 6 {
		t.Fatalf("created stamps = %q/%d, want %q/6", nt.CreatedTS, nt.CreatedSeq, t0)
	}
	if nt.EditedTS != "" || nt.EditedSeq != 0 {
		t.Fatalf("unedited note carries edit stamps: %+v", nt)
	}
	// An edit by a different actor updates the body and edit stamps, but the
	// author of record stays the creator.
	p2, err := Fold(append(log, envEA(7, t1, event.TypeNoteSuperseded, "wr_1", "author-b", "nt_1", `{"body":"second"}`)))
	if err != nil {
		t.Fatal(err)
	}
	nt = p2.Notes["nt_1"]
	if nt.Body != "second" {
		t.Fatalf("body not superseded: %q", nt.Body)
	}
	if nt.Author != "author-a" {
		t.Fatalf("author changed on edit: %q, want the creator", nt.Author)
	}
	if nt.EditedTS != t1 || nt.EditedSeq != 7 {
		t.Fatalf("edit stamps = %q/%d, want %q/7", nt.EditedTS, nt.EditedSeq, t1)
	}
	// Retraction removes the note but keeps its id reserved.
	p3, err := Fold(append(log, envE(7, t1, event.TypeNoteRetracted, "wr_1", "nt_1", `{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p3.Notes["nt_1"]; ok {
		t.Fatal("retracted note still present")
	}
	if p3.Version("nt_1") != 7 {
		t.Fatalf("retracted note id must stay in Versions, got %d", p3.Version("nt_1"))
	}
}

// TestNotesOfOrdering: NotesOf returns a thing's notes in ascending id order,
// filtered to that thing.
func TestNotesOfOrdering(t *testing.T) {
	log := []event.Envelope{
		initEv(1, t0),
		envE(2, t0, event.TypeStateDefined, "wr_1", "st_p", `{"name":"todo","semantic":"pending"}`),
		envE(3, t0, event.TypeTypeDefined, "wr_1", "ty_t", `{"name":"task"}`),
		envE(4, t0, event.TypeProjectCreated, "wr_1", "pr_p", `{"name":"Alpha"}`),
		envE(5, t0, event.TypeThingCreated, "wr_1", "th_x", `{"name":"X","project":"pr_p","type":"ty_t"}`),
		envE(6, t0, event.TypeThingCreated, "wr_1", "th_y", `{"name":"Y","project":"pr_p","type":"ty_t"}`),
		noteEv(7, t0, "a", "nt_2", "th_x", "b"),
		noteEv(8, t0, "a", "nt_1", "th_x", "a"),
		noteEv(9, t0, "a", "nt_9", "th_y", "other"),
	}
	p, err := Fold(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.NotesOf("th_x"); len(got) != 2 || got[0] != "nt_1" || got[1] != "nt_2" {
		t.Fatalf("NotesOf(th_x) = %v, want [nt_1 nt_2] (sorted, filtered)", got)
	}
	if got := p.NotesOf("th_y"); len(got) != 1 || got[0] != "nt_9" {
		t.Fatalf("NotesOf(th_y) = %v", got)
	}
}

// TestNoteValidation drives the cross-entity rules: a note needs an existing
// thing; supersede/retract need an existing note.
func TestNoteValidation(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))

	// Note on a missing thing is an undefined reference.
	b.reject(KindUndefinedReference, []string{"th_gone"},
		cmd3{event.TypeNoteAdded, "nt_1", `{"thing":"th_gone","body":"x"}`})

	// Valid note.
	b.must(cmd3{event.TypeNoteAdded, "nt_1", `{"thing":"th_a","body":"hello"}`})

	// Supersede / retract of an unknown note are unknown-entity errors.
	b.reject(KindUnknownEntity, []string{"nt_gone"},
		cmd3{event.TypeNoteSuperseded, "nt_gone", `{"body":"x"}`})
	b.reject(KindUnknownEntity, []string{"nt_gone"},
		cmd3{event.TypeNoteRetracted, "nt_gone", `{}`})

	// Editing the real note is fine.
	b.must(cmd3{event.TypeNoteSuperseded, "nt_1", `{"body":"edited"}`})
}

// TestThingRetractionBlockedByNote: a note keeps its thing from being
// retracted (a plain fact reference, like a dependency or requirement);
// removing the note first unblocks the retraction.
func TestThingRetractionBlockedByNote(t *testing.T) {
	b := newWS(t)
	b.must(thing("th_a", "A"))
	b.must(cmd3{event.TypeNoteAdded, "nt_1", `{"thing":"th_a","body":"hold on"}`})

	b.reject(KindRetractionBlocked, []string{"nt_1"},
		cmd3{event.TypeThingRetracted, "th_a", `{}`})

	// Remove the note, then the thing retracts cleanly.
	b.must(cmd3{event.TypeNoteRetracted, "nt_1", `{}`})
	b.must(cmd3{event.TypeThingRetracted, "th_a", `{}`})
}

// TestNoteFoldValidateParity: a note-bearing log folds identically whether
// replayed straight (Fold) or committed through ValidateBatch — the parity the
// golden fixtures assert for every other entity, exercised here for notes
// (whose events the golden .jsonl fixtures do not contain).
func TestNoteFoldValidateParity(t *testing.T) {
	evs := []event.Envelope{
		initEv(1, t0),
		envE(2, t0, event.TypeStateDefined, "wr_1", "st_p", `{"name":"todo","semantic":"pending"}`),
		envE(3, t0, event.TypeTypeDefined, "wr_1", "ty_t", `{"name":"task"}`),
		envE(4, t0, event.TypeProjectCreated, "wr_1", "pr_p", `{"name":"Alpha"}`),
		envE(5, t0, event.TypeThingCreated, "wr_1", "th_x", `{"name":"X","project":"pr_p","type":"ty_t"}`),
		noteEv(6, t0, "ana", "nt_1", "th_x", "first"),
		envEA(7, t0, event.TypeNoteSuperseded, "wr_1", "bob", "nt_1", `{"body":"edited"}`),
		noteEv(8, t0, "cyd", "nt_2", "th_x", "second"),
	}
	folded, err := Fold(evs)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	// Every event shares batch "b1" (envE/noteEv), so it is one ValidateBatch.
	validated, err := ValidateBatch(NewProjection(), evs, nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !reflect.DeepEqual(folded, validated) {
		t.Fatal("fold and validated replay disagree on a note-bearing log")
	}
}

// noteEv builds a note.added envelope with the given actor.
func noteEv(seq int64, ts, actor, id, thing, body string) event.Envelope {
	return envEA(seq, ts, event.TypeNoteAdded, "wr_1", actor, id,
		`{"thing":"`+thing+`","body":"`+body+`"}`)
}
