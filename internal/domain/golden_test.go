package domain_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"churn/internal/domain"
	"churn/internal/event"
)

// ── compact snapshot model: what the golden tests assert, field by field ──

type stateSnap struct{ Name, Semantic string }

type thingSnap struct {
	Project, Name, Type, Parent, State string
	Children                           []string
	Version                            int64
}

type depSnap struct{ From, To, OnAbandoned string }

type reqSnap struct {
	Thing        string
	Quantity     int
	Capabilities []string
	Resource     string
	Version      int64
}

type resSnap struct {
	Name         string
	Named        bool
	Capacity     int
	Type         string
	Capabilities []string
	Available    bool
}

type alSnap struct {
	Thing, Resource, Requirement string
	Quantity                     int
	Open                         bool
	OpenedSeq, ClosedSeq, ReqVer int64
}

type snap struct {
	States       map[string]stateSnap
	Types        map[string]string // id → name
	Capabilities map[string]string // id → name
	Projects     map[string]string // id → name
	Things       map[string]thingSnap
	Dependencies map[string]depSnap
	Requirements map[string]reqSnap
	Resources    map[string]resSnap
	Allocations  map[string]alSnap
}

func snapshot(p *domain.Projection) snap {
	s := snap{
		States:       map[string]stateSnap{},
		Types:        map[string]string{},
		Capabilities: map[string]string{},
		Projects:     map[string]string{},
		Things:       map[string]thingSnap{},
		Dependencies: map[string]depSnap{},
		Requirements: map[string]reqSnap{},
		Resources:    map[string]resSnap{},
		Allocations:  map[string]alSnap{},
	}
	for id, st := range p.States {
		s.States[id] = stateSnap{st.Name, st.Semantic}
	}
	for id, ty := range p.Types {
		s.Types[id] = ty.Name
	}
	for id, c := range p.Capabilities {
		s.Capabilities[id] = c.Name
	}
	for id, pr := range p.Projects {
		s.Projects[id] = pr.Name
	}
	for id, th := range p.Things {
		var children []string
		for c := range th.Children {
			children = append(children, c)
		}
		sort.Strings(children)
		s.Things[id] = thingSnap{
			Project: th.Project, Name: th.Name, Type: th.Type, Parent: th.Parent,
			State: th.State, Children: children, Version: p.Version(id),
		}
	}
	for id, d := range p.Dependencies {
		s.Dependencies[id] = depSnap{d.From, d.To, d.OnAbandoned}
	}
	for id, r := range p.Requirements {
		s.Requirements[id] = reqSnap{
			Thing: r.Thing, Quantity: r.Quantity,
			Capabilities: append([]string(nil), r.Capabilities...),
			Resource:     r.Resource, Version: r.Version,
		}
	}
	for id, r := range p.Resources {
		var caps []string
		for c := range r.Capabilities {
			caps = append(caps, c)
		}
		sort.Strings(caps)
		s.Resources[id] = resSnap{
			Name: r.Name, Named: r.Named, Capacity: r.Capacity, Type: r.Type,
			Capabilities: caps, Available: r.Available,
		}
	}
	for id, a := range p.Allocations {
		s.Allocations[id] = alSnap{
			Thing: a.Thing, Resource: a.Resource, Requirement: a.Requirement,
			Quantity: a.Quantity, Open: a.Open,
			OpenedSeq: a.OpenedSeq, ClosedSeq: a.ClosedSeq, ReqVer: a.RequirementVersion,
		}
	}
	return s
}

// assertSection compares one snapshot map id-by-id so a failure names the
// entity and field values, not a wall of reflect output.
func assertSection[T any](t *testing.T, section string, got, want map[string]T) {
	t.Helper()
	for _, id := range sortedIDs(want) {
		g, ok := got[id]
		if !ok {
			t.Errorf("%s: %s missing", section, id)
			continue
		}
		if !reflect.DeepEqual(g, want[id]) {
			t.Errorf("%s: %s\n  got  %+v\n  want %+v", section, id, g, want[id])
		}
	}
	for _, id := range sortedIDs(got) {
		if _, ok := want[id]; !ok {
			t.Errorf("%s: unexpected %s: %+v", section, id, got[id])
		}
	}
}

func sortedIDs[T any](m map[string]T) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func assertSnapshot(t *testing.T, got, want snap) {
	t.Helper()
	assertSection(t, "states", got.States, want.States)
	assertSection(t, "types", got.Types, want.Types)
	assertSection(t, "capabilities", got.Capabilities, want.Capabilities)
	assertSection(t, "projects", got.Projects, want.Projects)
	assertSection(t, "things", got.Things, want.Things)
	assertSection(t, "dependencies", got.Dependencies, want.Dependencies)
	assertSection(t, "requirements", got.Requirements, want.Requirements)
	assertSection(t, "resources", got.Resources, want.Resources)
	assertSection(t, "allocations", got.Allocations, want.Allocations)
}

// loadFixture reads a JSONL fixture and replays it twice: once through the
// plain fold, and once batch-by-batch through ValidateBatch (asserting the
// fixture is a VALID log, not merely a foldable one, and that the two paths
// agree — the writer's incremental path IS the replay path).
func loadFixture(t *testing.T, name string) *domain.Projection {
	t.Helper()
	fh, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var evs []event.Envelope
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		var ev event.Envelope
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		evs = append(evs, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	folded, err := domain.Fold(evs)
	if err != nil {
		t.Fatalf("%s does not fold: %v", name, err)
	}

	validated := domain.NewProjection()
	for i := 0; i < len(evs); {
		j := i
		for j < len(evs) && evs[j].Batch == evs[i].Batch {
			j++
		}
		validated, err = domain.ValidateBatch(validated, evs[i:j], nil)
		if err != nil {
			t.Fatalf("%s: batch %s rejected: %v", name, evs[i].Batch, err)
		}
		i = j
	}
	if !reflect.DeepEqual(folded, validated) {
		t.Fatalf("%s: fold and validated replay disagree", name)
	}
	return folded
}

func TestGoldenBasicProject(t *testing.T) {
	p := loadFixture(t, "basic_project.jsonl")
	assertSnapshot(t, snapshot(p), snap{
		States: map[string]stateSnap{
			"st_todo":  {"todo", "pending"},
			"st_doing": {"doing", "active"},
			"st_done":  {"done", "satisfied"},
		},
		Types:        map[string]string{"ty_task": "task", "ty_review": "review"},
		Capabilities: map[string]string{"cap_edit": "editing", "cap_approve": "approval"},
		Projects:     map[string]string{"pr_site": "Website relaunch"},
		Things: map[string]thingSnap{
			"th_draft":  {Project: "pr_site", Name: "Draft copy", Type: "ty_task", State: "st_done", Version: 25},
			"th_edit":   {Project: "pr_site", Name: "Edit copy", Type: "ty_task", State: "st_todo", Version: 22},
			"th_review": {Project: "pr_site", Name: "Final review", Type: "ty_review", State: "st_todo", Version: 23},
		},
		Dependencies: map[string]depSnap{
			"dep_edit_draft":  {"th_edit", "th_draft", "ignore"}, // default policy normalized
			"dep_review_edit": {"th_review", "th_edit", "block"},
		},
		Requirements: map[string]reqSnap{
			"req_edit":    {Thing: "th_edit", Quantity: 1, Capabilities: []string{"cap_edit"}, Version: 19},
			"req_approve": {Thing: "th_review", Quantity: 1, Resource: "rs_anna", Version: 20},
		},
		Resources: map[string]resSnap{
			"rs_editors": {Name: "Editors", Capacity: 2, Capabilities: []string{"cap_edit"}, Available: true},
			"rs_anna":    {Name: "Anna", Named: true, Capacity: 1, Capabilities: []string{"cap_approve"}, Available: true},
		},
		Allocations: map[string]alSnap{},
	})
	if p.WorkspaceID != "ws_fixture" || p.LastSeq != 25 {
		t.Fatalf("envelope facts: ws=%q lastSeq=%d", p.WorkspaceID, p.LastSeq)
	}
}

func TestGoldenPromotionDemotion(t *testing.T) {
	p := loadFixture(t, "promotion_demotion.jsonl")
	assertSnapshot(t, snapshot(p), snap{
		States: map[string]stateSnap{
			"st_todo":  {"todo", "pending"},
			"st_doing": {"doing", "active"},
			"st_done":  {"done", "satisfied"},
		},
		Types:        map[string]string{"ty_task": "task"},
		Capabilities: map[string]string{"cap_plan": "planning"},
		Projects:     map[string]string{"pr_p": "Launch plan"},
		Things: map[string]thingSnap{
			// After the full round trip th_plan is a childless leaf again, in
			// the explicitly appended pending state — nothing resurrected.
			"th_plan": {Project: "pr_p", Name: "Plan the launch", Type: "ty_task", State: "st_todo", Version: 20},
		},
		Dependencies: map[string]depSnap{},
		Requirements: map[string]reqSnap{},
		Resources:    map[string]resSnap{},
		Allocations:  map[string]alSnap{},
	})
	// Retracted ids stay burned: Versions still remembers them.
	for _, id := range []string{"th_step1", "th_step2", "req_plan", "req_step"} {
		if p.Version(id) == 0 {
			t.Errorf("retracted id %s lost its version entry (id reuse would open up)", id)
		}
	}
}

func TestGoldenAllocationLifecycle(t *testing.T) {
	p := loadFixture(t, "allocation_lifecycle.jsonl")
	assertSnapshot(t, snapshot(p), snap{
		States: map[string]stateSnap{
			"st_todo":  {"todo", "pending"},
			"st_doing": {"doing", "active"},
			"st_done":  {"done", "satisfied"},
		},
		Types:        map[string]string{"ty_task": "task"},
		Capabilities: map[string]string{"cap_edit": "editing"},
		Projects:     map[string]string{"pr_p": "Copy edit"},
		Things: map[string]thingSnap{
			"th_a": {Project: "pr_p", Name: "Edit the copy", Type: "ty_task", State: "st_done", Version: 18},
		},
		Dependencies: map[string]depSnap{},
		Requirements: map[string]reqSnap{
			// Superseded while active at seq 15: quantity 2 → 1.
			"req_1": {Thing: "th_a", Quantity: 1, Capabilities: []string{"cap_edit"}, Version: 15},
		},
		Resources: map[string]resSnap{
			"rs_pool": {Name: "Editors", Capacity: 2, Capabilities: []string{"cap_edit"}, Available: true},
		},
		Allocations: map[string]alSnap{
			// al_1 opened against requirement version 12 — after the seq-15
			// supersession it was "out of step" (12 < 15), then closed by the
			// atomic re-propose.
			"al_1": {Thing: "th_a", Resource: "rs_pool", Requirement: "req_1",
				Quantity: 2, Open: false, OpenedSeq: 14, ClosedSeq: 16, ReqVer: 12},
			// al_2 is its replacement at the current version, closed on finish.
			"al_2": {Thing: "th_a", Resource: "rs_pool", Requirement: "req_1",
				Quantity: 1, Open: false, OpenedSeq: 17, ClosedSeq: 19, ReqVer: 15},
		},
	})
	if got := p.AllocatedQuantity("rs_pool"); got != 0 {
		t.Fatalf("everything closed, allocated = %d", got)
	}
}
