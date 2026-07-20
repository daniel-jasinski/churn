package event

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeLogInitialized(t *testing.T) {
	p, err := Decode(TypeLogInitialized, 1, []byte(`{"workspace_id":"ws_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	li, ok := p.(*LogInitialized)
	if !ok {
		t.Fatalf("wrong payload type %T", p)
	}
	if li.WorkspaceID != "ws_1" {
		t.Fatalf("workspace_id = %q", li.WorkspaceID)
	}
}

func TestDecodeWriterStarted(t *testing.T) {
	if _, err := Decode(TypeWriterStarted, 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
}

// catalog lists every registered event type with its entity prefix and a
// minimal valid v1 payload — the §5.2 catalog, in one place.
var catalog = []struct {
	typ    string
	prefix string
	valid  string
}{
	{TypeLogInitialized, "", `{"workspace_id":"ws_1"}`},
	{TypeWriterStarted, "", `{}`},
	{TypeStateDefined, PrefixState, `{"name":"todo","semantic":"pending"}`},
	{TypeStateSuperseded, PrefixState, `{"name":"todo","semantic":"pending","color":"#888"}`},
	{TypeStateRetracted, PrefixState, `{}`},
	{TypeTypeDefined, PrefixType, `{"name":"task"}`},
	{TypeTypeSuperseded, PrefixType, `{"name":"step"}`},
	{TypeTypeRetracted, PrefixType, `{}`},
	{TypeCapabilityDefined, PrefixCapability, `{"name":"editing"}`},
	{TypeCapabilitySuperseded, PrefixCapability, `{"name":"editing","description":"d"}`},
	{TypeCapabilityRetracted, PrefixCapability, `{}`},
	{TypeProjectCreated, PrefixProject, `{"name":"Alpha"}`},
	{TypeProjectSuperseded, PrefixProject, `{"name":"Alpha","metadata":{"k":1}}`},
	{TypeProjectRetracted, PrefixProject, `{}`},
	{TypeThingCreated, PrefixThing, `{"name":"X","project":"pr_1","type":"ty_1"}`},
	{TypeThingSuperseded, PrefixThing, `{"name":"X","type":"ty_1","parent":"th_2"}`},
	{TypeThingRetracted, PrefixThing, `{}`},
	{TypeThingStateChanged, PrefixThing, `{"state":"st_1"}`},
	{TypeDependencyAsserted, PrefixDependency, `{"from":"th_1","to":"th_2"}`},
	{TypeDependencyRetracted, PrefixDependency, `{}`},
	{TypeRequirementAsserted, PrefixRequirement, `{"thing":"th_1","quantity":2,"capabilities":["cap_1"]}`},
	{TypeRequirementSuperseded, PrefixRequirement, `{"quantity":1,"resource":"rs_1"}`},
	{TypeRequirementRetracted, PrefixRequirement, `{}`},
	{TypeResourceTypeDefined, PrefixResourceType, `{"name":"person"}`},
	{TypeResourceTypeSuperseded, PrefixResourceType, `{"name":"person","color":"#888"}`},
	{TypeResourceTypeRetracted, PrefixResourceType, `{}`},
	{TypeResourceCreated, PrefixResource, `{"name":"Reviewers","kind":"reusable","capacity":4}`},
	{TypeResourceSuperseded, PrefixResource, `{"name":"W-04","kind":"reusable","named":true,"capacity":1,"type":"rt_1"}`},
	{TypeResourceRetracted, PrefixResource, `{}`},
	{TypeResourceAvailabilityChanged, PrefixResource, `{"available":false,"note":"maintenance"}`},
	{TypeCapabilityGranted, PrefixResource, `{"capability":"cap_1"}`},
	{TypeCapabilityRevoked, PrefixResource, `{"capability":"cap_1"}`},
	{TypeAllocationOpened, PrefixAllocation, `{"thing":"th_1","resource":"rs_1","quantity":1,"requirement":"req_1"}`},
	{TypeAllocationClosed, PrefixAllocation, `{}`},
	{TypeNoteAdded, PrefixNote, `{"thing":"th_1","body":"looks good"}`},
	{TypeNoteSuperseded, PrefixNote, `{"body":"revised"}`},
	{TypeNoteRetracted, PrefixNote, `{}`},
}

func TestCatalogRegistered(t *testing.T) {
	for _, c := range catalog {
		if !Known(c.typ, 1) {
			t.Errorf("%s v1 not registered", c.typ)
		}
		if Known(c.typ, 2) {
			t.Errorf("%s v2 must not be registered yet", c.typ)
		}
		if _, err := Decode(c.typ, 1, []byte(c.valid)); err != nil {
			t.Errorf("%s: minimal valid payload rejected: %v", c.typ, err)
		}
		if _, err := Decode(c.typ, 2, []byte(c.valid)); err == nil {
			t.Errorf("%s v2 must fail closed", c.typ)
		}
		prefix, ok := EntityPrefix(c.typ, 1)
		if !ok || prefix != c.prefix {
			t.Errorf("%s entity prefix = %q, want %q", c.typ, prefix, c.prefix)
		}
	}
	if len(registry) != len(catalog) {
		t.Errorf("registry has %d entries, catalog test covers %d — keep them in sync", len(registry), len(catalog))
	}
}

func TestUnknownTypeFailsClosed(t *testing.T) {
	_, err := Decode("gizmo.created", 1, []byte(`{}`))
	if err == nil {
		t.Fatal("unknown type must fail closed")
	}
	if !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnsupportedVersionFailsClosed(t *testing.T) {
	if _, err := Decode(TypeLogInitialized, 2, []byte(`{"workspace_id":"ws_1"}`)); err == nil {
		t.Fatal("unsupported version must fail closed")
	}
	if _, err := Decode(TypeLogInitialized, 0, []byte(`{"workspace_id":"ws_1"}`)); err == nil {
		t.Fatal("version 0 must fail closed")
	}
}

func TestUnknownPayloadFieldsTolerated(t *testing.T) {
	p, err := Decode(TypeLogInitialized, 1, []byte(`{"workspace_id":"ws_1","future_field":[1,2,3]}`))
	if err != nil {
		t.Fatalf("unknown fields must be tolerated: %v", err)
	}
	if p.(*LogInitialized).WorkspaceID != "ws_1" {
		t.Fatal("known field lost")
	}
	if _, err := Decode(TypeThingCreated, 1,
		[]byte(`{"name":"X","project":"pr_1","type":"ty_1","future":true}`)); err != nil {
		t.Fatalf("unknown fields must be tolerated: %v", err)
	}
}

func TestPayloadValidation(t *testing.T) {
	if _, err := Decode(TypeLogInitialized, 1, []byte(`{}`)); err == nil {
		t.Fatal("empty workspace_id must be rejected")
	}
	if _, err := Decode(TypeLogInitialized, 1, []byte(`not json`)); err == nil {
		t.Fatal("malformed payload must be rejected")
	}
}

// TestShapeRejections drives every payload-shape rule of the catalog.
func TestShapeRejections(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		data string
		want string
	}{
		{"state without name", TypeStateDefined, `{"semantic":"pending"}`, "name"},
		{"state with invalid semantic", TypeStateDefined, `{"name":"x","semantic":"busy"}`, "semantic"},
		{"state superseded invalid semantic", TypeStateSuperseded, `{"name":"x","semantic":""}`, "semantic"},
		{"type without name", TypeTypeDefined, `{}`, "name"},
		{"type field without key", TypeTypeDefined, `{"name":"t","fields":[{"label":"x"}]}`, "key"},
		{"type field duplicate key", TypeTypeDefined, `{"name":"t","fields":[{"key":"a"},{"key":"a","kind":"date"}]}`, "twice"},
		{"type field bad kind", TypeTypeDefined, `{"name":"t","fields":[{"key":"a","kind":"toggle"}]}`, "kind"},
		{"type field options without select", TypeTypeDefined, `{"name":"t","fields":[{"key":"a","kind":"number","options":["x"]}]}`, "options"},
		{"type field options with default kind", TypeTypeDefined, `{"name":"t","fields":[{"key":"a","options":["x"]}]}`, "options"},
		{"type field select without options", TypeTypeDefined, `{"name":"t","fields":[{"key":"a","kind":"select"}]}`, "options"},
		{"type field empty option", TypeTypeDefined, `{"name":"t","fields":[{"key":"a","kind":"select","options":["x",""]}]}`, "empty"},
		{"type superseded bad field", TypeTypeSuperseded, `{"name":"t","fields":[{"key":"a","kind":"select"}]}`, "options"},
		{"resource type field duplicate key", TypeResourceTypeDefined, `{"name":"r","fields":[{"key":"a"},{"key":"a"}]}`, "twice"},
		{"resource type superseded bad field kind", TypeResourceTypeSuperseded, `{"name":"r","fields":[{"key":"a","kind":"bool"}]}`, "kind"},
		{"capability without name", TypeCapabilityDefined, `{}`, "name"},
		{"project without name", TypeProjectCreated, `{}`, "name"},
		{"thing without project", TypeThingCreated, `{"name":"X","type":"ty_1"}`, "project"},
		{"thing with wrong project prefix", TypeThingCreated, `{"name":"X","project":"th_1","type":"ty_1"}`, "prefix"},
		{"thing without type", TypeThingCreated, `{"name":"X","project":"pr_1"}`, "type"},
		{"thing with bad parent prefix", TypeThingCreated, `{"name":"X","project":"pr_1","type":"ty_1","parent":"pr_2"}`, "prefix"},
		{"thing with bare prefix parent", TypeThingCreated, `{"name":"X","project":"pr_1","type":"ty_1","parent":"th_"}`, "prefix"},
		{"state_changed without state", TypeThingStateChanged, `{}`, "state"},
		{"dependency without from", TypeDependencyAsserted, `{"to":"th_2"}`, "from"},
		{"dependency with bad policy", TypeDependencyAsserted, `{"from":"th_1","to":"th_2","on_abandoned":"explode"}`, "on_abandoned"},
		{"requirement with neither form", TypeRequirementAsserted, `{"thing":"th_1","quantity":1}`, "exactly one"},
		{"requirement with both forms", TypeRequirementAsserted, `{"thing":"th_1","quantity":1,"capabilities":["cap_1"],"resource":"rs_1"}`, "exactly one"},
		{"requirement with zero quantity", TypeRequirementAsserted, `{"thing":"th_1","quantity":0,"capabilities":["cap_1"]}`, "quantity"},
		{"requirement with duplicate capability", TypeRequirementAsserted, `{"thing":"th_1","quantity":1,"capabilities":["cap_1","cap_1"]}`, "twice"},
		{"pinned requirement with quantity 2", TypeRequirementAsserted, `{"thing":"th_1","quantity":2,"resource":"rs_1"}`, "quantity must be 1"},
		{"requirement superseded pinned qty 2", TypeRequirementSuperseded, `{"quantity":2,"resource":"rs_1"}`, "quantity must be 1"},
		{"resource type without name", TypeResourceTypeDefined, `{}`, "name"},
		{"resource type superseded without name", TypeResourceTypeSuperseded, `{}`, "name"},
		{"resource with bad type prefix", TypeResourceCreated, `{"name":"R","kind":"reusable","capacity":1,"type":"ty_1"}`, "prefix"},
		{"resource superseded with bare type prefix", TypeResourceSuperseded, `{"name":"R","kind":"reusable","capacity":1,"type":"rt_"}`, "prefix"},
		{"resource without kind", TypeResourceCreated, `{"name":"R","capacity":1}`, "kind"},
		{"resource with zero capacity", TypeResourceCreated, `{"name":"R","kind":"reusable","capacity":0}`, "capacity"},
		{"named resource with capacity 2", TypeResourceCreated, `{"name":"R","kind":"reusable","named":true,"capacity":2}`, "named"},
		{"named resource superseded to capacity 3", TypeResourceSuperseded, `{"name":"R","kind":"reusable","named":true,"capacity":3}`, "named"},
		{"grant without capability", TypeCapabilityGranted, `{}`, "capability"},
		{"allocation with zero quantity", TypeAllocationOpened, `{"thing":"th_1","resource":"rs_1","quantity":0,"requirement":"req_1"}`, "quantity"},
		{"allocation without requirement", TypeAllocationOpened, `{"thing":"th_1","resource":"rs_1","quantity":1}`, "requirement"},
		{"note without thing", TypeNoteAdded, `{"body":"x"}`, "thing"},
		{"note with bad thing prefix", TypeNoteAdded, `{"thing":"pr_1","body":"x"}`, "prefix"},
		{"note with empty body", TypeNoteAdded, `{"thing":"th_1"}`, "body"},
		{"note superseded with empty body", TypeNoteSuperseded, `{}`, "body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.typ, 1, []byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// TestRefs pins the event_refs derivation of every payload that references
// entities beyond its own entity column.
func TestRefs(t *testing.T) {
	cases := []struct {
		typ  string
		data string
		want []Ref
	}{
		{TypeThingCreated, `{"name":"X","project":"pr_1","type":"ty_1"}`,
			[]Ref{{"pr_1", "project"}, {"ty_1", "type"}}},
		{TypeThingCreated, `{"name":"X","project":"pr_1","type":"ty_1","parent":"th_9"}`,
			[]Ref{{"pr_1", "project"}, {"ty_1", "type"}, {"th_9", "parent"}}},
		{TypeThingSuperseded, `{"name":"X","type":"ty_1","parent":"th_9"}`,
			[]Ref{{"ty_1", "type"}, {"th_9", "parent"}}},
		{TypeThingStateChanged, `{"state":"st_1"}`, []Ref{{"st_1", "state"}}},
		{TypeDependencyAsserted, `{"from":"th_1","to":"th_2"}`,
			[]Ref{{"th_1", "from"}, {"th_2", "to"}}},
		{TypeRequirementAsserted, `{"thing":"th_1","quantity":1,"capabilities":["cap_1","cap_2"]}`,
			[]Ref{{"th_1", "thing"}, {"cap_1", "capability"}, {"cap_2", "capability"}}},
		{TypeRequirementAsserted, `{"thing":"th_1","quantity":1,"resource":"rs_1"}`,
			[]Ref{{"th_1", "thing"}, {"rs_1", "pin"}}},
		{TypeRequirementSuperseded, `{"quantity":1,"resource":"rs_1"}`,
			[]Ref{{"rs_1", "pin"}}},
		{TypeResourceCreated, `{"name":"R","kind":"reusable","capacity":1,"type":"rt_1"}`,
			[]Ref{{"rt_1", "type"}}},
		{TypeResourceSuperseded, `{"name":"R","kind":"reusable","capacity":1,"type":"rt_1"}`,
			[]Ref{{"rt_1", "type"}}},
		{TypeCapabilityGranted, `{"capability":"cap_1"}`, []Ref{{"cap_1", "capability"}}},
		{TypeCapabilityRevoked, `{"capability":"cap_1"}`, []Ref{{"cap_1", "capability"}}},
		{TypeAllocationOpened, `{"thing":"th_1","resource":"rs_1","quantity":1,"requirement":"req_1"}`,
			[]Ref{{"th_1", "thing"}, {"rs_1", "resource"}, {"req_1", "requirement"}}},
		{TypeNoteAdded, `{"thing":"th_1","body":"x"}`, []Ref{{"th_1", "thing"}}},
	}
	for _, tc := range cases {
		p, err := Decode(tc.typ, 1, []byte(tc.data))
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		r, ok := p.(Referencer)
		if !ok {
			t.Fatalf("%s payload does not implement Referencer", tc.typ)
		}
		if got := r.Refs(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s refs = %v, want %v", tc.typ, got, tc.want)
		}
	}
}

func TestKnown(t *testing.T) {
	if !Known(TypeLogInitialized, 1) || !Known(TypeWriterStarted, 1) {
		t.Fatal("registered types must be known")
	}
	if Known(TypeLogInitialized, 2) || Known("nope", 1) {
		t.Fatal("unregistered (type, v) must not be known")
	}
}
