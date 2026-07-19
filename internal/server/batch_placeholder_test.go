package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// placeholderOps is the canonical single-batch scenario: vocab + project +
// parent/child things + a dependency + a transition, all wired with "$N"
// placeholder references to ids minted earlier in the same batch.
func placeholderOps(t *testing.T, e *env, mode string) map[string]any {
	t.Helper()
	// the seeded default paused state (on_hold) for the transition op
	resp, body := e.do(http.MethodGet, "/api/v1/vocab/states", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("states: %d %s", resp.StatusCode, body)
	}
	var states []struct {
		ID, Name string
	}
	if err := json.Unmarshal(body, &states); err != nil {
		t.Fatal(err)
	}
	onHold := ""
	for _, st := range states {
		if st.Name == "on_hold" {
			onHold = st.ID
		}
	}
	if onHold == "" {
		t.Fatal("default on_hold state missing")
	}
	return map[string]any{
		"mode": mode,
		"operations": []map[string]any{
			{"op": "create", "kind": "type", "data": map[string]any{"name": "t"}},
			{"op": "create", "kind": "project", "data": map[string]any{"name": "p"}},
			{"op": "create", "kind": "thing", "data": map[string]any{"project": "$1", "name": "parent", "type": "$0"}},
			{"op": "create", "kind": "thing", "data": map[string]any{"project": "$1", "name": "child", "type": "$0", "parent": "$2"}},
			{"op": "create", "kind": "thing", "data": map[string]any{"project": "$1", "name": "other", "type": "$0"}},
			{"op": "create", "kind": "dependency", "data": map[string]any{"from": "$4", "to": "$2"}},
			{"op": "transition", "kind": "thing", "id": "$4", "data": map[string]any{"state": onHold}},
		},
	}
}

type placeholderResp struct {
	Committed    bool                  `json:"committed"`
	Results      []struct{ ID string } `json:"results"`
	Placeholders map[string]string     `json:"placeholders"`
}

// TestBatchPlaceholdersCommit: one atomic batch may wire later ops to ids
// minted by earlier create ops via "$N" — the §2.1 conversion and bulk-add
// substrate.
func TestBatchPlaceholdersCommit(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(http.MethodPost, "/api/v1/batch", placeholderOps(t, e, "commit"), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("commit: %d %s", resp.StatusCode, body)
	}
	var br placeholderResp
	if err := json.Unmarshal(body, &br); err != nil {
		t.Fatal(err)
	}
	if !br.Committed || len(br.Results) != 7 {
		t.Fatalf("commit response: %s", body)
	}
	// placeholders map covers exactly the create ops, matching results
	if len(br.Placeholders) != 6 {
		t.Fatalf("placeholders: %v", br.Placeholders)
	}
	for _, n := range []string{"$0", "$1", "$2", "$3", "$4", "$5"} {
		i := int(n[1] - '0')
		if br.Placeholders[n] != br.Results[i].ID || br.Placeholders[n] == "" {
			t.Fatalf("placeholder %s: %v vs results %v", n, br.Placeholders, br.Results)
		}
	}

	// the wiring took: child is parented under parent, dependency exists,
	// "other" sits in on_hold
	p := e.w.Projection()
	parentID, childID, otherID := br.Placeholders["$2"], br.Placeholders["$3"], br.Placeholders["$4"]
	if p.Things[childID] == nil || p.Things[childID].Parent != parentID {
		t.Fatalf("child not parented under parent: %+v", p.Things[childID])
	}
	depID := br.Placeholders["$5"]
	dep := p.Dependencies[depID]
	if dep == nil || dep.From != otherID || dep.To != parentID {
		t.Fatalf("dependency not wired: %+v", dep)
	}
	if p.Things[otherID].State == "" {
		t.Fatalf("transition on placeholder target did not apply")
	}
}

// TestBatchPlaceholdersPreview: preview resolves and validates the same
// batch, returns the mapping, and commits nothing.
func TestBatchPlaceholdersPreview(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(http.MethodPost, "/api/v1/batch", placeholderOps(t, e, "preview"), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("preview: %d %s", resp.StatusCode, body)
	}
	var br placeholderResp
	if err := json.Unmarshal(body, &br); err != nil {
		t.Fatal(err)
	}
	if br.Committed || len(br.Placeholders) != 6 || br.Placeholders["$3"] == "" {
		t.Fatalf("preview response: %s", body)
	}
	if n := len(e.w.Projection().Things); n != 0 {
		t.Fatalf("preview committed %d things", n)
	}
}

// TestBatchPlaceholderErrors: malformed, out-of-range, forward/self, and
// non-create references are 400s naming the offending operation.
func TestBatchPlaceholderErrors(t *testing.T) {
	e := newEnv(t)
	// one real thing to hang ops off
	resp, body := e.do(http.MethodPost, "/api/v1/batch", map[string]any{
		"mode": "commit",
		"operations": []map[string]any{
			{"op": "create", "kind": "type", "data": map[string]any{"name": "t"}},
			{"op": "create", "kind": "project", "data": map[string]any{"name": "p"}},
			{"op": "create", "kind": "thing", "data": map[string]any{"project": "$1", "name": "a", "type": "$0"}},
		},
	}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("setup: %d %s", resp.StatusCode, body)
	}
	var setup placeholderResp
	if err := json.Unmarshal(body, &setup); err != nil {
		t.Fatal(err)
	}
	thing := setup.Placeholders["$2"]

	cases := []struct {
		name string
		ops  []map[string]any
		want string
	}{
		{"forward reference", []map[string]any{
			{"op": "create", "kind": "dependency", "data": map[string]any{"from": "$1", "to": thing}},
			{"op": "create", "kind": "thing", "data": map[string]any{"project": "pr_x", "name": "b", "type": "ty_x"}},
		}, "EARLIER"},
		{"self reference", []map[string]any{
			{"op": "create", "kind": "thing", "data": map[string]any{"project": "pr_x", "name": "b", "type": "ty_x", "parent": "$0"}},
		}, "EARLIER"},
		{"out of range", []map[string]any{
			{"op": "create", "kind": "dependency", "data": map[string]any{"from": "$9", "to": thing}},
		}, "does not index"},
		{"malformed", []map[string]any{
			{"op": "create", "kind": "dependency", "data": map[string]any{"from": "$x", "to": thing}},
		}, "does not index"},
		{"non-create reference", []map[string]any{
			{"op": "retract", "kind": "dependency", "id": "dep_nonexistent"},
			{"op": "create", "kind": "dependency", "data": map[string]any{"from": "$0", "to": thing}},
		}, "not a create"},
		{"placeholder as op id of non-create target", []map[string]any{
			{"op": "transition", "kind": "thing", "id": "$5", "data": map[string]any{"state": "st_x"}},
		}, "does not index"},
	}
	for _, tc := range cases {
		resp, body := e.do(http.MethodPost, "/api/v1/batch", map[string]any{
			"mode": "preview", "operations": tc.ops,
		}, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", tc.name, resp.StatusCode, body)
			continue
		}
		if !strings.Contains(string(body), "bad_request") || !strings.Contains(string(body), tc.want) {
			t.Errorf("%s: body %s, want kind bad_request mentioning %q", tc.name, body, tc.want)
		}
		if !strings.Contains(string(body), "operation ") {
			t.Errorf("%s: error does not name the operation: %s", tc.name, body)
		}
	}
}
