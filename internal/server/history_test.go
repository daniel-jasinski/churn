package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"churn/internal/interchange"
)

// histSeqs runs a history query and returns the seq column.
func (e *env) histSeqs(query string) []int64 {
	e.t.Helper()
	m := e.call("GET", "/api/v1/history"+query, nil, 200)
	var out []int64
	for _, raw := range m["events"].([]any) {
		out = append(out, int64(raw.(map[string]any)["seq"].(float64)))
	}
	return out
}

// TestHistoryFilters probes the SQL-level filtering: entity (envelope column
// AND event_refs hits), type, actor, batch, seq bounds, and limit.
func TestHistoryFilters(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	a := e.thing(f, "a")
	b := e.thing(f, "b")
	depDTO := e.call("POST", "/api/v1/dependencies", map[string]any{"from": b, "to": a}, 201)
	dep := str(depDTO, "id")

	// entity=a: thing.created (envelope entity) AND dependency.asserted
	// (event_refs role "to") must both hit.
	m := e.call("GET", "/api/v1/history?entity="+a, nil, 200)
	types := map[string]bool{}
	for _, raw := range m["events"].([]any) {
		ev := raw.(map[string]any)
		types[str(ev, "type")] = true
	}
	if !types["thing.created"] || !types["dependency.asserted"] {
		t.Fatalf("entity filter missed refs: %v", types)
	}
	if len(m["events"].([]any)) != 2 {
		t.Fatalf("entity=a events: %v", m["events"])
	}

	// entity=dep: the dependency's own event only.
	if got := e.histSeqs("?entity=" + dep); len(got) != 1 {
		t.Fatalf("entity=dep: %v", got)
	}

	// type filter.
	if got := e.histSeqs("?type=thing.created"); len(got) != 2 {
		t.Fatalf("type filter: %v", got)
	}
	// actor: everything in this harness is written by "tester" except the
	// writer's own lifecycle batch (actor "system").
	all := e.histSeqs("")
	tester := e.histSeqs("?actor=tester")
	system := e.histSeqs("?actor=system")
	if len(tester)+len(system) != len(all) || len(system) == 0 || len(tester) == 0 {
		t.Fatalf("actor split: all=%d tester=%d system=%d", len(all), len(tester), len(system))
	}
	if got := e.histSeqs("?actor=nobody"); len(got) != 0 {
		t.Fatalf("actor=nobody: %v", got)
	}

	// seq bounds and limit compose.
	if got := e.histSeqs("?since_seq=7&until_seq=8"); len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Fatalf("seq bounds: %v", got)
	}
	if got := e.histSeqs("?since_seq=7&limit=1"); len(got) != 1 || got[0] != 7 {
		t.Fatalf("limit: %v", got)
	}
	// batch filter combined with type.
	batch := ""
	for _, raw := range e.call("GET", "/api/v1/history?type=dependency.asserted", nil, 200)["events"].([]any) {
		batch = str(raw.(map[string]any), "batch")
	}
	if got := e.histSeqs("?batch=" + batch + "&type=dependency.asserted"); len(got) != 1 {
		t.Fatalf("batch+type: %v", got)
	}
	// Bad params are 400.
	e.call("GET", "/api/v1/history?since_seq=x", nil, 400)
	e.call("GET", "/api/v1/history?format=xml", nil, 400)
}

// TestHistoryJSONLMatchesExport: format=jsonl streams canonical envelope
// lines byte-identical to the interchange export of the same rows.
func TestHistoryJSONLMatchesExport(t *testing.T) {
	e := newEnv(t)
	f := e.seed()
	e.thing(f, "x")

	resp, body := e.do("GET", "/api/v1/history?format=jsonl", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("jsonl status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("jsonl content type %q", ct)
	}

	var want bytes.Buffer
	if err := interchange.Export(e.st, &want); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want.Bytes()) {
		t.Fatalf("jsonl differs from export:\napi:    %q\nexport: %q",
			firstDiffLine(body, want.Bytes()), firstDiffLine(want.Bytes(), body))
	}

	// Every line parses as a JSON envelope.
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unparseable jsonl line %q: %v", line, err)
		}
	}

	// A filtered jsonl slice equals the corresponding export lines.
	resp, part := e.do("GET", "/api/v1/history?format=jsonl&since_seq=7", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("filtered jsonl status %d", resp.StatusCode)
	}
	lines := strings.SplitAfter(want.String(), "\n")
	if got, wantTail := string(part), strings.Join(lines[6:], ""); got != wantTail {
		t.Fatalf("filtered jsonl:\n%q\nwant:\n%q", got, wantTail)
	}
}

func firstDiffLine(a, b []byte) string {
	la := strings.Split(string(a), "\n")
	lb := strings.Split(string(b), "\n")
	for i := range la {
		if i >= len(lb) || la[i] != lb[i] {
			return la[i]
		}
	}
	return ""
}
