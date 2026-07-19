//go:build ignore

// gen regenerates the golden fixture logs in this directory:
//
//	go run internal/domain/testdata/gen.go
//
// Every batch runs through domain.ValidateBatch, so a checked-in fixture is
// by construction a valid log. golden_test.go replays the fixtures and
// asserts projection snapshots field by field — regenerate AND re-derive the
// expected snapshots when the story changes.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"churn/internal/canonjson"
	"churn/internal/domain"
	"churn/internal/event"
)

type c struct {
	typ    string
	entity string
	pl     any
}

type fixture struct {
	p   *domain.Projection
	evs []event.Envelope
	nb  int
}

func newFixture() *fixture {
	f := &fixture{p: domain.NewProjection()}
	f.batch(c{event.TypeLogInitialized, "", event.LogInitialized{WorkspaceID: "ws_fixture"}})
	return f
}

func (f *fixture) batch(cmds ...c) {
	f.nb++
	ts := fmt.Sprintf("2026-07-19T10:%02d:00.000Z", f.nb)
	var evs []event.Envelope
	for i, cc := range cmds {
		data, err := canonjson.Encode(cc.pl)
		if err != nil {
			log.Fatalf("encoding %s: %v", cc.typ, err)
		}
		seq := f.p.LastSeq + int64(i) + 1
		evs = append(evs, event.Envelope{
			Seq: seq, ID: fmt.Sprintf("ev_%03d", seq), Origin: "wr_fix",
			Batch: fmt.Sprintf("b_%03d", f.nb), TS: ts, Actor: "daniel",
			Type: cc.typ, V: 1, Entity: cc.entity, Data: data,
		})
	}
	cand, err := domain.ValidateBatch(f.p, evs, nil)
	if err != nil {
		log.Fatalf("fixture batch %d invalid: %v", f.nb, err)
	}
	f.p = cand
	f.evs = append(f.evs, evs...)
}

func (f *fixture) write(name string) {
	path := filepath.Join("internal", "domain", "testdata", name)
	out, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	for _, ev := range f.evs {
		line, err := json.Marshal(ev)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(out, "%s\n", line)
	}
	fmt.Printf("%s: %d events, last seq %d\n", path, len(f.evs), f.p.LastSeq)
}

// basicProject: a small realistic project — vocabulary, three things with
// dependencies, a pool and a named resource, a capability requirement and a
// pin, and a start→finish of the requirement-free first step.
func basicProject() {
	f := newFixture()
	f.batch(
		c{event.TypeStateDefined, "st_todo", event.StateDefined{Name: "todo", Semantic: "pending"}},
		c{event.TypeStateDefined, "st_doing", event.StateDefined{Name: "doing", Semantic: "active"}},
		c{event.TypeStateDefined, "st_done", event.StateDefined{Name: "done", Semantic: "satisfied"}},
	)
	f.batch(
		c{event.TypeTypeDefined, "ty_task", event.TypeDefined{Name: "task"}},
		c{event.TypeTypeDefined, "ty_review", event.TypeDefined{Name: "review"}},
		c{event.TypeCapabilityDefined, "cap_edit", event.CapabilityDefined{Name: "editing"}},
		c{event.TypeCapabilityDefined, "cap_approve", event.CapabilityDefined{Name: "approval"}},
		c{event.TypeProjectCreated, "pr_site", event.ProjectCreated{Name: "Website relaunch"}},
	)
	f.batch(
		c{event.TypeThingCreated, "th_draft", event.ThingCreated{Project: "pr_site", Name: "Draft copy", Type: "ty_task"}},
		c{event.TypeThingCreated, "th_edit", event.ThingCreated{Project: "pr_site", Name: "Edit copy", Type: "ty_task"}},
		c{event.TypeThingCreated, "th_review", event.ThingCreated{Project: "pr_site", Name: "Final review", Type: "ty_review"}},
		c{event.TypeDependencyAsserted, "dep_edit_draft", event.DependencyAsserted{From: "th_edit", To: "th_draft"}},
		c{event.TypeDependencyAsserted, "dep_review_edit", event.DependencyAsserted{From: "th_review", To: "th_edit", OnAbandoned: "block"}},
		c{event.TypeResourceCreated, "rs_editors", event.ResourceCreated{Name: "Editors", Kind: "reusable", Capacity: 2}},
		c{event.TypeCapabilityGranted, "rs_editors", event.CapabilityGranted{Capability: "cap_edit"}},
		c{event.TypeResourceCreated, "rs_anna", event.ResourceCreated{Name: "Anna", Kind: "reusable", Named: true, Capacity: 1}},
		c{event.TypeCapabilityGranted, "rs_anna", event.CapabilityGranted{Capability: "cap_approve"}},
		c{event.TypeRequirementAsserted, "req_edit", event.RequirementAsserted{Thing: "th_edit", Quantity: 1, Capabilities: []string{"cap_edit"}}},
		c{event.TypeRequirementAsserted, "req_approve", event.RequirementAsserted{Thing: "th_review", Quantity: 1, Resource: "rs_anna"}},
		c{event.TypeThingStateChanged, "th_draft", event.ThingStateChanged{State: "st_todo"}},
		c{event.TypeThingStateChanged, "th_edit", event.ThingStateChanged{State: "st_todo"}},
		c{event.TypeThingStateChanged, "th_review", event.ThingStateChanged{State: "st_todo"}},
	)
	// Start the requirement-free first step (no allocations needed)…
	f.batch(c{event.TypeThingStateChanged, "th_draft", event.ThingStateChanged{State: "st_doing"}})
	// …and finish it.
	f.batch(c{event.TypeThingStateChanged, "th_draft", event.ThingStateChanged{State: "st_done"}})
	f.write("basic_project.jsonl")
}

// promotionDemotion: a leaf with state and a requirement is converted to a
// composite (the §2.1 one-batch conversion), grows a second child, shrinks
// again, and is finally demoted back to a leaf with the explicit pending
// transition.
func promotionDemotion() {
	f := newFixture()
	f.batch(
		c{event.TypeStateDefined, "st_todo", event.StateDefined{Name: "todo", Semantic: "pending"}},
		c{event.TypeStateDefined, "st_doing", event.StateDefined{Name: "doing", Semantic: "active"}},
		c{event.TypeStateDefined, "st_done", event.StateDefined{Name: "done", Semantic: "satisfied"}},
		c{event.TypeTypeDefined, "ty_task", event.TypeDefined{Name: "task"}},
		c{event.TypeCapabilityDefined, "cap_plan", event.CapabilityDefined{Name: "planning"}},
		c{event.TypeProjectCreated, "pr_p", event.ProjectCreated{Name: "Launch plan"}},
	)
	f.batch(
		c{event.TypeThingCreated, "th_plan", event.ThingCreated{Project: "pr_p", Name: "Plan the launch", Type: "ty_task"}},
		c{event.TypeThingStateChanged, "th_plan", event.ThingStateChanged{State: "st_todo"}},
		c{event.TypeRequirementAsserted, "req_plan", event.RequirementAsserted{Thing: "th_plan", Quantity: 1, Capabilities: []string{"cap_plan"}}},
	)
	// Conversion: requirements and state move onto an auto-created child.
	f.batch(
		c{event.TypeRequirementRetracted, "req_plan", event.RequirementRetracted{}},
		c{event.TypeThingCreated, "th_step1", event.ThingCreated{Project: "pr_p", Name: "Plan the launch (step)", Type: "ty_task", Parent: "th_plan"}},
		c{event.TypeRequirementAsserted, "req_step", event.RequirementAsserted{Thing: "th_step1", Quantity: 1, Capabilities: []string{"cap_plan"}}},
		c{event.TypeThingStateChanged, "th_step1", event.ThingStateChanged{State: "st_todo"}},
	)
	// A second child appears…
	f.batch(
		c{event.TypeThingCreated, "th_step2", event.ThingCreated{Project: "pr_p", Name: "Book the venue", Type: "ty_task", Parent: "th_plan"}},
		c{event.TypeThingStateChanged, "th_step2", event.ThingStateChanged{State: "st_todo"}},
	)
	// …and is retracted again (not the last child — no demotion yet).
	f.batch(c{event.TypeThingRetracted, "th_step2", event.ThingRetracted{}})
	// Demotion: the last child goes, and the SAME batch explicitly puts the
	// parent back into a pending state.
	f.batch(
		c{event.TypeRequirementRetracted, "req_step", event.RequirementRetracted{}},
		c{event.TypeThingRetracted, "th_step1", event.ThingRetracted{}},
		c{event.TypeThingStateChanged, "th_plan", event.ThingStateChanged{State: "st_todo"}},
	)
	f.write("promotion_demotion.jsonl")
}

// allocationLifecycle: enter active with an exact allocation, supersede the
// requirement while active (out-of-step versions), re-propose atomically,
// finish and release.
func allocationLifecycle() {
	f := newFixture()
	f.batch(
		c{event.TypeStateDefined, "st_todo", event.StateDefined{Name: "todo", Semantic: "pending"}},
		c{event.TypeStateDefined, "st_doing", event.StateDefined{Name: "doing", Semantic: "active"}},
		c{event.TypeStateDefined, "st_done", event.StateDefined{Name: "done", Semantic: "satisfied"}},
		c{event.TypeTypeDefined, "ty_task", event.TypeDefined{Name: "task"}},
		c{event.TypeCapabilityDefined, "cap_edit", event.CapabilityDefined{Name: "editing"}},
		c{event.TypeProjectCreated, "pr_p", event.ProjectCreated{Name: "Copy edit"}},
	)
	f.batch(
		c{event.TypeResourceCreated, "rs_pool", event.ResourceCreated{Name: "Editors", Kind: "reusable", Capacity: 2}},
		c{event.TypeCapabilityGranted, "rs_pool", event.CapabilityGranted{Capability: "cap_edit"}},
	)
	f.batch(
		c{event.TypeThingCreated, "th_a", event.ThingCreated{Project: "pr_p", Name: "Edit the copy", Type: "ty_task"}},
		c{event.TypeThingStateChanged, "th_a", event.ThingStateChanged{State: "st_todo"}},
		c{event.TypeRequirementAsserted, "req_1", event.RequirementAsserted{Thing: "th_a", Quantity: 2, Capabilities: []string{"cap_edit"}}},
	)
	// Enter active with quantity-exact coverage.
	f.batch(
		c{event.TypeThingStateChanged, "th_a", event.ThingStateChanged{State: "st_doing"}},
		c{event.TypeAllocationOpened, "al_1", event.AllocationOpened{Thing: "th_a", Resource: "rs_pool", Quantity: 2, Requirement: "req_1"}},
	)
	// Requirement superseded WHILE ACTIVE (§2.5): al_1 is now out of step.
	f.batch(
		c{event.TypeRequirementSuperseded, "req_1", event.RequirementSuperseded{Quantity: 1, Capabilities: []string{"cap_edit"}}},
	)
	// One-click re-propose: close the obsolete allocation, open the
	// replacement, thing active throughout.
	f.batch(
		c{event.TypeAllocationClosed, "al_1", event.AllocationClosed{}},
		c{event.TypeAllocationOpened, "al_2", event.AllocationOpened{Thing: "th_a", Resource: "rs_pool", Quantity: 1, Requirement: "req_1"}},
	)
	// Finish: leave active, closing the open allocation.
	f.batch(
		c{event.TypeThingStateChanged, "th_a", event.ThingStateChanged{State: "st_done"}},
		c{event.TypeAllocationClosed, "al_2", event.AllocationClosed{}},
	)
	f.write("allocation_lifecycle.jsonl")
}

func main() {
	basicProject()
	promotionDemotion()
	allocationLifecycle()
}
