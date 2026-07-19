package domain

import (
	"reflect"
	"strings"
	"testing"

	"churn/internal/event"
)

func env(seq int64, ts, typ, origin, data string) event.Envelope {
	return envE(seq, ts, typ, origin, "", data)
}

func envE(seq int64, ts, typ, origin, entity, data string) event.Envelope {
	return event.Envelope{
		Seq:    seq,
		ID:     ts + "-id", // uniqueness is the store's job; anything readable works here
		Origin: origin,
		Batch:  "b1",
		TS:     ts,
		Actor:  "test",
		Type:   typ,
		V:      1,
		Entity: entity,
		Data:   []byte(data),
	}
}

func initEv(seq int64, ts string) event.Envelope {
	return env(seq, ts, event.TypeLogInitialized, "wr_1", `{"workspace_id":"ws_1"}`)
}

const t0 = "2026-07-19T10:00:00.000Z"

// fullLog is a foldable log touching every projection map: vocabulary, a
// project, a parent/child pair, a third thing with a dependency, requirement,
// resource with a granted capability, an active state, and an allocation.
// (Fold does not run batch validation, so the sequence only has to be
// structurally sound.)
func fullLog() []event.Envelope {
	return []event.Envelope{
		initEv(1, t0),
		envE(2, t0, event.TypeStateDefined, "wr_1", "st_p", `{"name":"todo","semantic":"pending"}`),
		envE(3, t0, event.TypeStateDefined, "wr_1", "st_a", `{"name":"in_progress","semantic":"active"}`),
		envE(4, t0, event.TypeTypeDefined, "wr_1", "ty_t",
			`{"name":"task","fields":[{"key":"prio","kind":"select","options":["low","high"],"required":true}]}`),
		envE(5, t0, event.TypeCapabilityDefined, "wr_1", "cap_c", `{"name":"editing"}`),
		envE(6, t0, event.TypeProjectCreated, "wr_1", "pr_p", `{"name":"Alpha","metadata":{"k":"v"}}`),
		envE(7, t0, event.TypeThingCreated, "wr_1", "th_parent", `{"name":"Workstream","project":"pr_p","type":"ty_t"}`),
		envE(8, t0, event.TypeThingCreated, "wr_1", "th_child", `{"name":"Step","parent":"th_parent","project":"pr_p","type":"ty_t"}`),
		envE(9, t0, event.TypeThingCreated, "wr_1", "th_x", `{"name":"Task X","project":"pr_p","type":"ty_t"}`),
		envE(10, t0, event.TypeDependencyAsserted, "wr_1", "dep_d", `{"from":"th_x","to":"th_child"}`),
		envE(11, t0, event.TypeRequirementAsserted, "wr_1", "req_r", `{"capabilities":["cap_c"],"quantity":1,"thing":"th_x"}`),
		envE(12, t0, event.TypeResourceCreated, "wr_1", "rs_r", `{"capacity":2,"kind":"reusable","name":"Reviewers"}`),
		envE(13, t0, event.TypeCapabilityGranted, "wr_1", "rs_r", `{"capability":"cap_c"}`),
		envE(14, t0, event.TypeThingStateChanged, "wr_1", "th_x", `{"state":"st_a"}`),
		envE(15, t0, event.TypeAllocationOpened, "wr_1", "al_a", `{"quantity":1,"requirement":"req_r","resource":"rs_r","thing":"th_x"}`),
		envE(16, t0, event.TypeResourceTypeDefined, "wr_1", "rt_m",
			`{"name":"machine","fields":[{"key":"room","kind":"select","options":["a","b"]}]}`),
		envE(17, t0, event.TypeResourceSuperseded, "wr_1", "rs_r", `{"capacity":2,"kind":"reusable","name":"Reviewers","type":"rt_m"}`),
	}
}

func TestFoldMinimalLog(t *testing.T) {
	p, err := Fold([]event.Envelope{
		initEv(1, "2026-07-19T10:00:00.000Z"),
		env(2, "2026-07-19T10:00:01.000Z", event.TypeWriterStarted, "wr_2", `{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.WorkspaceID != "ws_1" {
		t.Fatalf("WorkspaceID = %q", p.WorkspaceID)
	}
	if p.Origin != "wr_2" {
		t.Fatalf("Origin = %q, want the latest lineage", p.Origin)
	}
	if p.LastSeq != 2 || p.LastTS != "2026-07-19T10:00:01.000Z" {
		t.Fatalf("LastSeq=%d LastTS=%q", p.LastSeq, p.LastTS)
	}
}

func TestFoldFullLog(t *testing.T) {
	p, err := Fold(fullLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.States) != 2 || len(p.Things) != 3 || len(p.Allocations) != 1 {
		t.Fatalf("unexpected projection sizes: %d states, %d things, %d allocations",
			len(p.States), len(p.Things), len(p.Allocations))
	}
	if !p.IsComposite("th_parent") || p.IsComposite("th_child") {
		t.Fatal("composite detection wrong")
	}
	if got := p.Version("th_x"); got != 14 {
		t.Fatalf("Version(th_x) = %d, want 14 (its last touching event)", got)
	}
	if al := p.Allocations["al_a"]; !al.Open || al.RequirementVersion != 11 {
		t.Fatalf("allocation = %+v, want open with requirement version 11", al)
	}
	wantFields := []MetadataField{{Key: "prio", Kind: "select", Options: []string{"low", "high"}, Required: true}}
	if got := p.Types["ty_t"].Fields; !reflect.DeepEqual(got, wantFields) {
		t.Fatalf("ty_t fields = %+v, want %+v", got, wantFields)
	}
	if got := p.ResourceTypes["rt_m"].Fields; len(got) != 1 || got[0].Key != "room" {
		t.Fatalf("rt_m fields = %+v", got)
	}
}

// TestMetadataFieldDeclarationsFold: declared metadata fields fold onto the
// type entity with Kind normalized to its default, and supersession is full
// replacement — a superseding payload without fields drops the declaration
// (§5.2, §5.3).
func TestMetadataFieldDeclarationsFold(t *testing.T) {
	log := []event.Envelope{
		initEv(1, t0),
		envE(2, t0, event.TypeTypeDefined, "wr_1", "ty_t",
			`{"name":"task","fields":[{"key":"ref","label":"Ticket"},{"key":"due","kind":"date"}]}`),
	}
	p, err := Fold(log)
	if err != nil {
		t.Fatal(err)
	}
	want := []MetadataField{
		{Key: "ref", Label: "Ticket", Kind: "text"}, // kind defaulted
		{Key: "due", Kind: "date"},
	}
	if got := p.Types["ty_t"].Fields; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %+v, want %+v", got, want)
	}

	p, err = Fold(append(log,
		envE(3, t0, event.TypeTypeSuperseded, "wr_1", "ty_t", `{"name":"task"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Types["ty_t"].Fields; got != nil {
		t.Fatalf("fields after fieldless supersession = %+v, want none (full replacement)", got)
	}
}

func TestFirstEventMustBeLogInitialized(t *testing.T) {
	_, err := Fold([]event.Envelope{
		env(1, "2026-07-19T10:00:00.000Z", event.TypeWriterStarted, "wr_1", `{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "log.initialized") {
		t.Fatalf("expected first-event error, got %v", err)
	}
}

func TestLogInitializedOnlyAtSeqOne(t *testing.T) {
	_, err := Fold([]event.Envelope{
		initEv(1, "2026-07-19T10:00:00.000Z"),
		env(2, "2026-07-19T10:00:01.000Z", event.TypeLogInitialized, "wr_1", `{"workspace_id":"ws_2"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "must be the first event") {
		t.Fatalf("expected repeat-init rejection, got %v", err)
	}
}

func TestSeqMustBeContiguous(t *testing.T) {
	_, err := Fold([]event.Envelope{
		initEv(1, "2026-07-19T10:00:00.000Z"),
		env(3, "2026-07-19T10:00:01.000Z", event.TypeWriterStarted, "wr_2", `{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "seq") {
		t.Fatalf("expected seq gap rejection, got %v", err)
	}
}

func TestTimestampMustBeMonotone(t *testing.T) {
	_, err := Fold([]event.Envelope{
		initEv(1, "2026-07-19T10:00:05.000Z"),
		env(2, "2026-07-19T10:00:04.000Z", event.TypeWriterStarted, "wr_2", `{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "before predecessor") {
		t.Fatalf("expected ts monotonicity rejection, got %v", err)
	}
}

func TestUnknownTypeFailsClosed(t *testing.T) {
	_, err := Fold([]event.Envelope{
		initEv(1, "2026-07-19T10:00:00.000Z"),
		env(2, "2026-07-19T10:00:01.000Z", "gizmo.created", "wr_1", `{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("expected fail-closed, got %v", err)
	}
}

func TestUnsupportedVersionFailsClosed(t *testing.T) {
	ev := initEv(1, "2026-07-19T10:00:00.000Z")
	ev.V = 99
	if _, err := Fold([]event.Envelope{ev}); err == nil {
		t.Fatal("unsupported version must fail closed")
	}
}

func TestEntityPrefixEnforced(t *testing.T) {
	// A thing event whose entity carries the wrong typed prefix must be
	// rejected by the fold.
	_, err := Fold([]event.Envelope{
		initEv(1, t0),
		envE(2, t0, event.TypeProjectCreated, "wr_1", "pr_p", `{"name":"Alpha"}`),
		envE(3, t0, event.TypeTypeDefined, "wr_1", "ty_t", `{"name":"task"}`),
		envE(4, t0, event.TypeThingCreated, "wr_1", "rs_oops", `{"name":"X","project":"pr_p","type":"ty_t"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("expected entity prefix rejection, got %v", err)
	}
}

func TestIdsNeverReused(t *testing.T) {
	// Retracting an entity must not free its id for reuse.
	log := []event.Envelope{
		initEv(1, t0),
		envE(2, t0, event.TypeTypeDefined, "wr_1", "ty_t", `{"name":"task"}`),
		envE(3, t0, event.TypeTypeRetracted, "wr_1", "ty_t", `{}`),
		envE(4, t0, event.TypeTypeDefined, "wr_1", "ty_t", `{"name":"reborn"}`),
	}
	if _, err := Fold(log); err == nil || !strings.Contains(err.Error(), "never reused") {
		t.Fatalf("expected id-reuse rejection, got %v", err)
	}
}

// TestCloneIsIndependent verifies that mutating a clone never reaches the
// original. It is table-driven per field so it grows with Projection:
// M2+ MUST add a mutation case for EVERY new field — for reference-typed
// fields (maps, slices, pointers) the case must mutate through the
// reference (e.g. write into the clone's map, or through a stored pointer),
// which is exactly what a missed deep copy in Clone fails on.
func TestCloneIsIndependent(t *testing.T) {
	cases := []struct {
		field  string
		mutate func(c *Projection)
	}{
		{"WorkspaceID", func(c *Projection) { c.WorkspaceID = "ws_mutated" }},
		{"Origin", func(c *Projection) { c.Origin = "wr_mutated" }},
		{"LastSeq", func(c *Projection) { c.LastSeq = 99 }},
		{"LastTS", func(c *Projection) { c.LastTS = "9999-01-01T00:00:00.000Z" }},
		{"LastBatch", func(c *Projection) { c.LastBatch = "b_mutated" }},
		{"States map", func(c *Projection) { c.States["st_new"] = &State{Name: "x"} }},
		{"States entry", func(c *Projection) { c.States["st_p"].Name = "mutated" }},
		{"Types map", func(c *Projection) { c.Types["ty_new"] = &ThingType{Name: "x"} }},
		{"Types entry", func(c *Projection) { c.Types["ty_t"].Color = "mutated" }},
		{"Types.Fields", func(c *Projection) { c.Types["ty_t"].Fields[0].Key = "mutated" }},
		{"Types.Fields options", func(c *Projection) { c.Types["ty_t"].Fields[0].Options[0] = "mutated" }},
		{"ResourceTypes map", func(c *Projection) { c.ResourceTypes["rt_new"] = &ResourceType{Name: "x"} }},
		{"ResourceTypes entry", func(c *Projection) { c.ResourceTypes["rt_m"].Color = "mutated" }},
		{"ResourceTypes.Fields", func(c *Projection) { c.ResourceTypes["rt_m"].Fields[0].Options[0] = "mutated" }},
		{"Capabilities map", func(c *Projection) { c.Capabilities["cap_new"] = &Capability{} }},
		{"Capabilities entry", func(c *Projection) { c.Capabilities["cap_c"].Name = "mutated" }},
		{"Projects map", func(c *Projection) { c.Projects["pr_new"] = &Project{} }},
		{"Projects entry", func(c *Projection) { c.Projects["pr_p"].Name = "mutated" }},
		{"Things map", func(c *Projection) { c.Things["th_new"] = &Thing{} }},
		{"Things entry", func(c *Projection) { c.Things["th_x"].Name = "mutated" }},
		{"Things.Children", func(c *Projection) { c.Things["th_parent"].Children["th_new"] = struct{}{} }},
		{"Dependencies map", func(c *Projection) { c.Dependencies["dep_new"] = &Dependency{} }},
		{"Dependencies entry", func(c *Projection) { c.Dependencies["dep_d"].OnAbandoned = "block" }},
		{"Requirements map", func(c *Projection) { c.Requirements["req_new"] = &Requirement{} }},
		{"Requirements entry", func(c *Projection) { c.Requirements["req_r"].Quantity = 99 }},
		{"Requirements.Capabilities", func(c *Projection) { c.Requirements["req_r"].Capabilities[0] = "cap_mut" }},
		{"Resources map", func(c *Projection) { c.Resources["rs_new"] = &Resource{} }},
		{"Resources entry", func(c *Projection) { c.Resources["rs_r"].Capacity = 99 }},
		{"Resources.Capabilities", func(c *Projection) { c.Resources["rs_r"].Capabilities["cap_mut"] = struct{}{} }},
		{"Allocations map", func(c *Projection) { c.Allocations["al_new"] = &Allocation{} }},
		{"Allocations entry", func(c *Projection) { c.Allocations["al_a"].Open = false }},
		{"Versions map", func(c *Projection) { c.Versions["th_x"] = 999 }},
		{"Statuses map", func(c *Projection) { c.Statuses["th_new"] = &ThingStatus{} }},
		{"Statuses entry", func(c *Projection) { c.Statuses["th_x"].Status = "mutated" }},
		{"StatusSeq", func(c *Projection) { c.StatusSeq = 999 }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			p, err := Fold(fullLog())
			if err != nil {
				t.Fatal(err)
			}
			before, err := Fold(fullLog()) // independent reference copy
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(p.Clone())
			if !reflect.DeepEqual(p, before) {
				t.Fatalf("mutating clone's %s reached the original", tc.field)
			}
		})
	}

	// Guard against silently missing new fields. Two sweeps:
	//  1. every Projection field needs a mutation case;
	//  2. every REFERENCE-TYPED field of an entity struct stored in a
	//     Projection map (Thing.Children, Requirement.Capabilities, …) needs
	//     a case named "<ProjectionField>.<EntityField>" that mutates
	//     through the nested reference — exactly what a missed deep copy in
	//     the entity's clone() fails on.
	requireCase := func(name string) {
		for _, tc := range cases {
			if strings.HasPrefix(tc.field, name) {
				return
			}
		}
		t.Errorf("no mutation case for %s — add one (and a deep copy in Clone)", name)
	}
	isReference := func(k reflect.Kind) bool {
		switch k {
		case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface,
			reflect.Chan, reflect.Func, reflect.UnsafePointer:
			return true
		}
		return false
	}
	pt := reflect.TypeOf(Projection{})
	for i := 0; i < pt.NumField(); i++ {
		f := pt.Field(i)
		requireCase(f.Name)
		// Recurse into entity structs stored as map[string]*T.
		if f.Type.Kind() != reflect.Map || f.Type.Elem().Kind() != reflect.Pointer ||
			f.Type.Elem().Elem().Kind() != reflect.Struct {
			continue
		}
		et := f.Type.Elem().Elem()
		for j := 0; j < et.NumField(); j++ {
			if ef := et.Field(j); isReference(ef.Type.Kind()) {
				requireCase(f.Name + "." + ef.Name)
			}
		}
	}

	// And the intended use: folding a batch into a clone leaves the
	// published original untouched.
	p, err := Fold([]event.Envelope{initEv(1, "2026-07-19T10:00:00.000Z")})
	if err != nil {
		t.Fatal(err)
	}
	c := p.Clone()
	if err := c.Apply(env(2, "2026-07-19T10:00:01.000Z", event.TypeWriterStarted, "wr_2", `{}`)); err != nil {
		t.Fatal(err)
	}
	if p.LastSeq != 1 || p.Origin != "wr_1" {
		t.Fatal("Apply on clone mutated the original")
	}
	if c.LastSeq != 2 || c.Origin != "wr_2" {
		t.Fatal("clone did not fold the event")
	}
}
