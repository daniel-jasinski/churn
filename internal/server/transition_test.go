package server

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestConfirmDrift builds the exact §5 drift scenario: two things want the
// same capability, two single-unit resources exist. Client 1 proposes (gets
// R1 under the ascending-id tie-break), client 2 proposes AND confirms
// (taking R1), then client 1 confirms its now-stale proposal → 409 with a
// FRESH proposal (R2) in details, which then confirms successfully.
func TestConfirmDrift(t *testing.T) {
	e := newEnv(t)
	f := e.seed()

	a1 := e.thing(f, "one")
	a2 := e.thing(f, "two")
	e.requirement(f, a1, 1)
	e.requirement(f, a2, 1)
	r1 := e.resource(f, "r-alpha", 1, false)
	r2 := e.resource(f, "r-beta", 1, false)
	if r1 >= r2 {
		// ULIDs ascend with mint order; the tie-break test below relies on it.
		t.Fatalf("resource ids not ascending: %s, %s", r1, r2)
	}

	// Client 1 proposes for a1 → R1 (first eligible wins).
	p1 := e.call("POST", "/api/v1/things/"+a1+"/transition", map[string]any{"state": f.active}, 200)
	prop1 := p1["proposal"].(map[string]any)
	if got := str(prop1["allocations"].([]any)[0].(map[string]any), "resource"); got != r1 {
		t.Fatalf("client 1 proposed %s, want %s", got, r1)
	}

	// Client 2 proposes and confirms for a2 — taking R1.
	p2 := e.call("POST", "/api/v1/things/"+a2+"/transition", map[string]any{"state": f.active}, 200)
	prop2 := p2["proposal"].(map[string]any)
	if got := str(prop2["allocations"].([]any)[0].(map[string]any), "resource"); got != r1 {
		t.Fatalf("client 2 proposed %s, want %s", got, r1)
	}
	e.call("POST", "/api/v1/things/"+a2+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(prop2, "token"),
	}, 200)

	// Client 1 confirms the stale proposal → 409 (capacity drift) with a
	// fresh proposal naming R2.
	conflict := e.call("POST", "/api/v1/things/"+a1+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(prop1, "token"),
	}, 409)
	if errKind(conflict) != "capacity" {
		t.Fatalf("drift kind %q, want capacity; %v", errKind(conflict), conflict)
	}
	details := conflict["error"].(map[string]any)["details"].(map[string]any)
	fresh, ok := details["fresh_proposal"].(map[string]any)
	if !ok {
		t.Fatalf("no fresh proposal in %v", details)
	}
	if got := str(fresh["allocations"].([]any)[0].(map[string]any), "resource"); got != r2 {
		t.Fatalf("fresh proposal resource %s, want %s", got, r2)
	}

	// Re-confirm with the fresh token succeeds.
	done := e.call("POST", "/api/v1/things/"+a1+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(fresh, "token"),
	}, 200)
	if done["committed"] != true {
		t.Fatalf("re-confirm: %v", done)
	}
}

// TestConfirmDriftInfeasible: when the last unit is gone and no alternative
// exists, the drift 409 carries fresh_proposal: null.
func TestConfirmDriftInfeasible(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	a1 := e.thing(f, "one")
	a2 := e.thing(f, "two")
	e.requirement(f, a1, 1)
	e.requirement(f, a2, 1)
	e.resource(f, "only", 1, false)

	p1 := e.call("POST", "/api/v1/things/"+a1+"/transition", map[string]any{"state": f.active}, 200)
	p2 := e.call("POST", "/api/v1/things/"+a2+"/transition", map[string]any{"state": f.active}, 200)
	e.call("POST", "/api/v1/things/"+a2+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(p2["proposal"].(map[string]any), "token"),
	}, 200)

	conflict := e.call("POST", "/api/v1/things/"+a1+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(p1["proposal"].(map[string]any), "token"),
	}, 409)
	details := conflict["error"].(map[string]any)["details"].(map[string]any)
	if details["fresh_proposal"] != nil {
		t.Fatalf("fresh_proposal = %v, want null", details["fresh_proposal"])
	}

	// A fresh propose leg now reports infeasible directly.
	m := e.call("POST", "/api/v1/things/"+a1+"/transition", map[string]any{"state": f.active}, 409)
	if errKind(m) != "infeasible_allocation" {
		t.Fatalf("propose on empty pool: %v", m)
	}
}

// TestProposeTokenValidation: a confirm must carry a token bound to the
// same thing and state.
func TestProposeTokenValidation(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	a1 := e.thing(f, "one")
	a2 := e.thing(f, "two")
	e.requirement(f, a1, 1)
	e.resource(f, "r", 1, false)

	p1 := e.call("POST", "/api/v1/things/"+a1+"/transition", map[string]any{"state": f.active}, 200)
	token := str(p1["proposal"].(map[string]any), "token")

	m := e.call("POST", "/api/v1/things/"+a2+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": token,
	}, 400)
	if errKind(m) != "bad_request" {
		t.Fatalf("cross-thing token: %v", m)
	}
	m = e.call("POST", "/api/v1/things/"+a1+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": "not-a-token!!!",
	}, 400)
	if errKind(m) != "bad_request" {
		t.Fatalf("garbage token: %v", m)
	}
}

// TestConfirmTamperedToken: a token whose allocation rows were tampered
// (zero quantity, garbage entity ids) is a client shape error — 400 with
// the structured envelope, never an internal writer error leak.
func TestConfirmTamperedToken(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	th := e.thing(f, "target")
	e.requirement(f, th, 1)
	e.resource(f, "r", 1, false)

	freshToken := func() (string, map[string]any) {
		t.Helper()
		p := e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{"state": f.active}, 200)
		prop := p["proposal"].(map[string]any)
		tok := str(prop, "token")
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatal(err)
		}
		var claim map[string]any
		if err := json.Unmarshal(raw, &claim); err != nil {
			t.Fatal(err)
		}
		return tok, claim
	}
	reencode := func(claim map[string]any) string {
		t.Helper()
		b, err := json.Marshal(claim)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}

	tamper := []struct {
		name string
		edit func(row map[string]any)
	}{
		{"quantity zero", func(row map[string]any) { row["quantity"] = 0 }},
		{"quantity negative", func(row map[string]any) { row["quantity"] = -5 }},
		{"garbage resource id", func(row map[string]any) { row["resource"] = "not-a-resource" }},
		{"garbage requirement id", func(row map[string]any) { row["requirement"] = "junk" }},
	}
	for _, tc := range tamper {
		_, claim := freshToken()
		row := claim["allocations"].([]any)[0].(map[string]any)
		tc.edit(row)
		resp, body := e.do("POST", "/api/v1/things/"+th+"/transition", map[string]any{
			"state": f.active, "confirm": true, "proposal": reencode(claim),
		}, nil)
		if resp.StatusCode != 400 {
			t.Fatalf("%s: status %d, want 400; body %s", tc.name, resp.StatusCode, body)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil || errKind(m) != "bad_request" {
			t.Fatalf("%s: envelope %s", tc.name, body)
		}
		if strings.Contains(string(body), "writer:") {
			t.Fatalf("%s: internal writer error leaked: %s", tc.name, body)
		}
	}

	// The untampered token still confirms — tampering attempts changed
	// nothing.
	tok, _ := freshToken()
	done := e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": tok,
	}, 200)
	if done["committed"] != true {
		t.Fatalf("clean confirm after tamper attempts: %v", done)
	}
}

// TestRepropose drives the §2.5 out-of-step flow: supersede a requirement
// under an active thing (badge appears), then one repropose call closes the
// stale allocation and opens the replacement atomically.
func TestRepropose(t *testing.T) {
	e := newEnv(t)
	f := e.seed()

	// A second capability that only resource B carries.
	cap2 := str(e.call("POST", "/api/v1/vocab/capabilities", map[string]any{"name": "sign-off"}, 201), "id")
	th := e.thing(f, "work")
	req := e.requirement(f, th, 1)
	rA := e.resource(f, "res-a", 1, false)
	rB := e.resource(f, "res-b", 1, false)
	e.call("POST", "/api/v1/resources/"+rB+"/capabilities", map[string]any{"capability": cap2}, 200)

	// Start work: the tie-break assigns rA.
	p := e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{"state": f.active}, 200)
	prop := p["proposal"].(map[string]any)
	if got := str(prop["allocations"].([]any)[0].(map[string]any), "resource"); got != rA {
		t.Fatalf("initial assignment on %s, want %s", got, rA)
	}
	e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(prop, "token"),
	}, 200)

	// Supersede the requirement mid-active: now needs cap AND cap2 — legal,
	// but the allocations drift out of step (§2.5).
	e.call("PATCH", "/api/v1/requirements/"+req, map[string]any{
		"quantity": 1, "capabilities": []string{f.cap, cap2},
	}, 200)
	badges := e.call("GET", "/api/v1/things/"+th, nil, 200)["badges"].(map[string]any)
	if badges["allocations_out_of_step"] != true {
		t.Fatalf("out-of-step badge not set: %v", badges)
	}

	// One click: atomic close+reopen. Only rB satisfies the new AND-set.
	rp := e.call("POST", "/api/v1/things/"+th+"/repropose", nil, 200)
	if rp["committed"] != true || len(rp["closed"].([]any)) != 1 || len(rp["opened"].([]any)) != 1 {
		t.Fatalf("repropose: %v", rp)
	}

	// Atomicity: close and open share one batch.
	hist := e.call("GET", "/api/v1/history?batch="+str(rp, "batch"), nil, 200)
	if n := len(hist["events"].([]any)); n != 2 {
		t.Fatalf("repropose batch has %d events, want 2", n)
	}

	// Badge cleared; thing still working; rB now holds the unit.
	after := e.call("GET", "/api/v1/things/"+th, nil, 200)
	if str(after, "status") != "working" {
		t.Fatalf("thing left active during repropose: %v", after)
	}
	if after["badges"].(map[string]any)["allocations_out_of_step"] != false {
		t.Fatalf("badge not cleared: %v", after)
	}
	if got := e.call("GET", "/api/v1/resources/"+rB, nil, 200); got["allocated"].(float64) != 1 {
		t.Fatalf("rB not allocated after repropose: %v", got)
	}
	if got := e.call("GET", "/api/v1/resources/"+rA, nil, 200); got["allocated"].(float64) != 0 {
		t.Fatalf("rA not freed after repropose: %v", got)
	}
}

// TestReproposeRequiresActive: repropose on a non-active thing is refused.
func TestReproposeRequiresActive(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	th := e.thing(f, "idle")
	m := e.call("POST", "/api/v1/things/"+th+"/repropose", nil, 422)
	if errKind(m) != "allocation" {
		t.Fatalf("repropose idle: %v", m)
	}
}

// TestTransitionPauseAndResume: leaving active semantics auto-closes the
// allocations in the same batch; held things report resumable_now.
func TestTransitionPauseAndResume(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	th := e.thing(f, "pausable")
	e.requirement(f, th, 1)
	e.resource(f, "r", 1, false)

	p := e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{"state": f.active}, 200)
	e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{
		"state": f.active, "confirm": true, "proposal": str(p["proposal"].(map[string]any), "token"),
	}, 200)

	paused := e.call("POST", "/api/v1/things/"+th+"/transition", map[string]any{"state": f.paused}, 200)
	if len(paused["closed"].([]any)) != 1 {
		t.Fatalf("pause did not close allocations: %v", paused)
	}
	dto := e.call("GET", "/api/v1/things/"+th, nil, 200)
	if str(dto, "status") != "held" || dto["resumable_now"] != true {
		t.Fatalf("held thing: %v", dto)
	}
}
