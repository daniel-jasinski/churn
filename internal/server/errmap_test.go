package server

import (
	"errors"
	"net/http"
	"testing"

	"churn/internal/domain"
)

// TestDomainErrorMapping pins the documented kind → HTTP status table for
// every domain error kind.
func TestDomainErrorMapping(t *testing.T) {
	table := map[string]int{
		domain.KindStaleVersion:         409,
		domain.KindCycle:                409,
		domain.KindRetractionBlocked:    409,
		domain.KindSemanticImmutable:    409,
		domain.KindCapacity:             409,
		domain.KindInfeasible:           409,
		domain.KindUnknownEntity:        404,
		domain.KindDuplicateID:          422,
		domain.KindUndefinedReference:   422,
		domain.KindContainment:          422,
		domain.KindCompositeState:       422,
		domain.KindCompositeRequirement: 422,
		domain.KindPinViolation:         422,
		domain.KindAllocation:           422,
		domain.KindAllocationCoverage:   422,
		domain.KindDemotion:             422,
	}
	for kind, want := range table {
		err := &domain.Error{Kind: kind, Message: "m", IDs: []string{"x"}}
		ae := mapError(err)
		if ae.status != want {
			t.Errorf("kind %s → %d, want %d", kind, ae.status, want)
		}
		if ae.kind != kind {
			t.Errorf("kind %s not passed through (got %s)", kind, ae.kind)
		}
		if len(ae.ids) != 1 || ae.ids[0] != "x" {
			t.Errorf("kind %s ids not passed through: %v", kind, ae.ids)
		}
	}
	// Wrapped domain errors unwrap.
	wrapped := mapError(wrap(&domain.Error{Kind: domain.KindCycle, Message: "m"}))
	if wrapped.status != 409 || wrapped.kind != domain.KindCycle {
		t.Errorf("wrapped domain error: %+v", wrapped)
	}
	// Unknown future kinds default to 422.
	if got := mapError(&domain.Error{Kind: "novel_kind", Message: "m"}); got.status != 422 {
		t.Errorf("unknown kind → %d, want 422", got.status)
	}
	// Plain errors are internal 500s — a domain rejection is never reduced
	// to a bare Go error string with a misleading status.
	if got := mapError(errors.New("boom")); got.status != http.StatusInternalServerError || got.kind != "internal" {
		t.Errorf("plain error: %+v", got)
	}
}

func wrap(err error) error { return &wrapped{err} }

type wrapped struct{ err error }

func (w *wrapped) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

// TestErrorEnvelopesEndToEnd samples the mapping through real requests:
// each rejection arrives as the one envelope with the documented status.
func TestErrorEnvelopesEndToEnd(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	a := e.thing(f, "a")
	b := e.thing(f, "b")
	e.call("POST", "/api/v1/dependencies", map[string]any{"from": a, "to": b}, 201)

	// cycle → 409, offending expanded cycle in ids.
	m := e.call("POST", "/api/v1/dependencies", map[string]any{"from": b, "to": a}, 409)
	if errKind(m) != "cycle" || len(errIDs(m)) == 0 {
		t.Fatalf("cycle envelope: %v", m)
	}

	// undefined_reference → 422 (well-formed but unknown type id).
	m = e.call("POST", "/api/v1/things", map[string]any{
		"project": f.project, "name": "x", "type": "ty_nonexistent",
	}, 422)
	if errKind(m) != "undefined_reference" {
		t.Fatalf("undefined reference envelope: %v", m)
	}

	// composite_state → 422: transitioning a composite.
	child := str(e.call("POST", "/api/v1/things", map[string]any{
		"project": f.project, "name": "child", "type": f.typ, "parent": a,
	}, 201), "id")
	_ = child
	m = e.call("POST", "/api/v1/things/"+a+"/transition", map[string]any{"state": f.done}, 422)
	if errKind(m) != "composite_state" {
		t.Fatalf("composite transition envelope: %v", m)
	}

	// semantic_immutable → 409: changing an occupied state's semantic.
	e.call("POST", "/api/v1/things/"+b+"/transition", map[string]any{"state": f.done}, 200)
	m = e.call("PATCH", "/api/v1/vocab/states/"+f.done, map[string]any{
		"name": "done", "semantic": "abandoned",
	}, 409)
	if errKind(m) != "semantic_immutable" {
		t.Fatalf("semantic change envelope: %v", m)
	}

	// pin_violation → 422: pinning an unnamed resource.
	rs := e.resource(f, "pool", 3, false)
	m = e.call("POST", "/api/v1/requirements", map[string]any{
		"thing": b, "quantity": 1, "resource": rs,
	}, 422)
	if errKind(m) != "pin_violation" {
		t.Fatalf("pin envelope: %v", m)
	}
}
