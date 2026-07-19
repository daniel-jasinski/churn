package interchange_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"churn/internal/canonjson"
	"churn/internal/domain"
	"churn/internal/domain/domaintest"
	"churn/internal/event"
	"churn/internal/interchange"
	"churn/internal/store"
	"churn/internal/ulid"
	"churn/internal/writer"

	_ "modernc.org/sqlite"
)

type testClock struct{ ms int64 }

func (c *testClock) now() time.Time { return time.UnixMilli(c.ms) }

func scanAll(t *testing.T, st *store.Store) []event.Envelope {
	t.Helper()
	var evs []event.Envelope
	if err := st.Scan(func(ev event.Envelope) error { evs = append(evs, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	return evs
}

func exportBytes(t *testing.T, st *store.Store) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := interchange.Export(st, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readRefs reads the full event_refs table of a workspace database directly.
func readRefs(t *testing.T, dir string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, store.DBFileName)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT event_seq, entity_id, role FROM event_refs ORDER BY event_seq, entity_id, role`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var seq int64
		var ent, role string
		if err := rows.Scan(&seq, &ent, &role); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d/%s/%s", seq, ent, role))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRoundTrip is the M4 gate: a realistic fuzzed workspace exports,
// imports into a fresh directory, and exports again to BYTE-IDENTICAL
// JSONL; the folded projections deep-equal; and resuming writing on the
// restored directory appends writer.started with a fresh origin while the
// restored events keep their original ones (§5.4).
func TestRoundTrip(t *testing.T) {
	dir1 := t.TempDir()
	clock := &testClock{ms: 1721390000000}
	st1, err := store.Open(dir1)
	if err != nil {
		t.Fatal(err)
	}
	w, err := writer.Open(st1, writer.Options{Now: clock.now, Entropy: rand.New(rand.NewSource(3))})
	if err != nil {
		t.Fatal(err)
	}
	g := domaintest.NewGenerator(17)
	fuzz := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			cmds := g.Next(w.Projection())
			if cmds == nil {
				continue
			}
			wcmds := make([]writer.Command, len(cmds))
			for j, c := range cmds {
				wcmds[j] = writer.Command{Type: c.Type, V: c.V, Entity: c.Entity, Payload: c.Payload}
			}
			clock.ms += int64(i%5) * 250
			if _, err := w.Submit("fuzz", wcmds, nil); err != nil {
				t.Fatalf("fuzz batch %d: %v", i, err)
			}
		}
	}
	fuzz(80)
	// Restart the writer mid-build so the exported log carries a
	// writer.started lineage renewal too.
	w.Close()
	w, err = writer.Open(st1, writer.Options{Now: clock.now, Entropy: rand.New(rand.NewSource(5))})
	if err != nil {
		t.Fatal(err)
	}
	fuzz(60)
	// Supplemental deterministic batches: the generator cannot be relied on
	// to supersede or retract every vocabulary kind/projects, and the
	// round-trip fixture must cover the whole catalog (define → supersede →
	// retract an unused entity of each).
	mint := func(prefix string) string {
		t.Helper()
		id, err := w.MintID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	stR, tyR, capR, prR, rtR := mint(event.PrefixState), mint(event.PrefixType),
		mint(event.PrefixCapability), mint(event.PrefixProject), mint(event.PrefixResourceType)
	// A deterministic allocation cycle guarantees allocation.opened AND
	// .closed appear even when the fuzz never closed one: a dedicated
	// resource/requirement pair, a transition into an active state (opening
	// the allocation), and a transition out (closing it).
	stateOf := func(semantic string) string {
		t.Helper()
		p := w.Projection()
		best := ""
		for id, st := range p.States {
			if st.Semantic == semantic && (best == "" || id < best) {
				best = id
			}
		}
		if best == "" {
			t.Fatalf("no %s-semantic state in the fixture workspace", semantic)
		}
		return best
	}
	stActive, stPending := stateOf(event.SemanticActive), stateOf(event.SemanticPending)
	capZ, tyZ, prZ, rsZ, rtZ := mint(event.PrefixCapability), mint(event.PrefixType),
		mint(event.PrefixProject), mint(event.PrefixResource), mint(event.PrefixResourceType)
	thZ, reqZ, alZ := mint(event.PrefixThing), mint(event.PrefixRequirement), mint(event.PrefixAllocation)
	for _, batch := range [][]writer.Command{
		{
			{Type: event.TypeCapabilityDefined, V: 1, Entity: capZ, Payload: event.CapabilityDefined{Name: "alloc cap"}},
			// Fields-carrying type.defined: declared metadata field shapes
			// (§5.3) must cross the export → import boundary intact.
			{Type: event.TypeTypeDefined, V: 1, Entity: tyZ, Payload: event.TypeDefined{
				Name: "alloc type",
				Fields: []event.MetadataField{
					{Key: "prio", Label: "Priority", Kind: event.FieldKindSelect,
						Options: []string{"low", "high"}, Required: true},
					{Key: "ref"},
				},
			}},
			{Type: event.TypeResourceTypeDefined, V: 1, Entity: rtZ, Payload: event.ResourceTypeDefined{Name: "alloc rtype"}},
			{Type: event.TypeProjectCreated, V: 1, Entity: prZ, Payload: event.ProjectCreated{Name: "alloc project"}},
			{Type: event.TypeResourceCreated, V: 1, Entity: rsZ, Payload: event.ResourceCreated{Name: "alloc rs", Kind: event.KindReusable, Capacity: 1, Type: rtZ}},
			{Type: event.TypeCapabilityGranted, V: 1, Entity: rsZ, Payload: event.CapabilityGranted{Capability: capZ}},
			{Type: event.TypeThingCreated, V: 1, Entity: thZ, Payload: event.ThingCreated{Project: prZ, Name: "alloc thing", Type: tyZ}},
			{Type: event.TypeRequirementAsserted, V: 1, Entity: reqZ, Payload: event.RequirementAsserted{Thing: thZ, Quantity: 1, Capabilities: []string{capZ}}},
		},
		{
			{Type: event.TypeThingStateChanged, V: 1, Entity: thZ, Payload: event.ThingStateChanged{State: stActive}},
			{Type: event.TypeAllocationOpened, V: 1, Entity: alZ, Payload: event.AllocationOpened{Thing: thZ, Resource: rsZ, Quantity: 1, Requirement: reqZ}},
		},
		{
			{Type: event.TypeThingStateChanged, V: 1, Entity: thZ, Payload: event.ThingStateChanged{State: stPending}},
			{Type: event.TypeAllocationClosed, V: 1, Entity: alZ, Payload: event.AllocationClosed{}},
		},
		// Deterministic coverage of the resource sub-facts, independent of
		// what the fuzz happened to roll: an availability toggle and a
		// capability revocation.
		{
			{Type: event.TypeResourceAvailabilityChanged, V: 1, Entity: rsZ, Payload: event.ResourceAvailabilityChanged{Available: false, Note: "maintenance"}},
			{Type: event.TypeCapabilityRevoked, V: 1, Entity: rsZ, Payload: event.CapabilityRevoked{Capability: capZ}},
		},
		{
			{Type: event.TypeStateDefined, V: 1, Entity: stR, Payload: event.StateDefined{Name: "temp", Semantic: event.SemanticPending}},
			{Type: event.TypeTypeDefined, V: 1, Entity: tyR, Payload: event.TypeDefined{Name: "temp type"}},
			{Type: event.TypeResourceTypeDefined, V: 1, Entity: rtR, Payload: event.ResourceTypeDefined{Name: "temp rtype"}},
			{Type: event.TypeCapabilityDefined, V: 1, Entity: capR, Payload: event.CapabilityDefined{Name: "temp cap"}},
			{Type: event.TypeProjectCreated, V: 1, Entity: prR, Payload: event.ProjectCreated{Name: "temp project"}},
		},
		{
			{Type: event.TypeStateSuperseded, V: 1, Entity: stR, Payload: event.StateSuperseded{Name: "temp'", Semantic: event.SemanticPending, Color: "#abc"}},
			{Type: event.TypeTypeSuperseded, V: 1, Entity: tyR, Payload: event.TypeSuperseded{Name: "temp type'"}},
			{Type: event.TypeResourceTypeSuperseded, V: 1, Entity: rtR, Payload: event.ResourceTypeSuperseded{Name: "temp rtype'"}},
			{Type: event.TypeCapabilitySuperseded, V: 1, Entity: capR, Payload: event.CapabilitySuperseded{Name: "temp cap'"}},
			{Type: event.TypeProjectSuperseded, V: 1, Entity: prR, Payload: event.ProjectSuperseded{Name: "temp project'"}},
		},
		{
			{Type: event.TypeStateRetracted, V: 1, Entity: stR, Payload: event.StateRetracted{}},
			{Type: event.TypeTypeRetracted, V: 1, Entity: tyR, Payload: event.TypeRetracted{}},
			{Type: event.TypeResourceTypeRetracted, V: 1, Entity: rtR, Payload: event.ResourceTypeRetracted{}},
			{Type: event.TypeCapabilityRetracted, V: 1, Entity: capR, Payload: event.CapabilityRetracted{}},
			{Type: event.TypeProjectRetracted, V: 1, Entity: prR, Payload: event.ProjectRetracted{}},
		},
	} {
		clock.ms += 500
		if _, err := w.Submit("fuzz", batch, nil); err != nil {
			t.Fatalf("supplemental batch: %v", err)
		}
	}
	w.Close()

	export1 := exportBytes(t, st1)
	evs1 := scanAll(t, st1)
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}

	// The fixture must exercise the ENTIRE catalog: every event type crosses
	// the export → import boundary at least once.
	seenTypes := map[string]bool{}
	for _, ev := range evs1 {
		seenTypes[ev.Type] = true
	}
	for _, typ := range []string{
		event.TypeLogInitialized, event.TypeWriterStarted,
		event.TypeStateDefined, event.TypeStateSuperseded, event.TypeStateRetracted,
		event.TypeTypeDefined, event.TypeTypeSuperseded, event.TypeTypeRetracted,
		event.TypeCapabilityDefined, event.TypeCapabilitySuperseded, event.TypeCapabilityRetracted,
		event.TypeProjectCreated, event.TypeProjectSuperseded, event.TypeProjectRetracted,
		event.TypeThingCreated, event.TypeThingSuperseded, event.TypeThingRetracted,
		event.TypeThingStateChanged,
		event.TypeDependencyAsserted, event.TypeDependencyRetracted,
		event.TypeRequirementAsserted, event.TypeRequirementSuperseded, event.TypeRequirementRetracted,
		event.TypeResourceTypeDefined, event.TypeResourceTypeSuperseded, event.TypeResourceTypeRetracted,
		event.TypeResourceCreated, event.TypeResourceSuperseded, event.TypeResourceRetracted,
		event.TypeResourceAvailabilityChanged,
		event.TypeCapabilityGranted, event.TypeCapabilityRevoked,
		event.TypeAllocationOpened, event.TypeAllocationClosed,
	} {
		if !seenTypes[typ] {
			t.Errorf("round-trip fixture never produced %s; extend the fuzz or the supplemental batches", typ)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// Import into a fresh directory.
	dir2 := filepath.Join(t.TempDir(), "restored")
	nEvents, nBatches, err := interchange.Import(dir2, bytes.NewReader(export1))
	if err != nil {
		t.Fatal(err)
	}
	if nEvents != len(evs1) {
		t.Fatalf("imported %d events, want %d", nEvents, len(evs1))
	}
	if nBatches < 2 {
		t.Fatalf("imported %d batches, want a realistic multi-batch log", nBatches)
	}

	// Re-export: byte-identical.
	st2, err := store.OpenReadOnly(dir2)
	if err != nil {
		t.Fatal(err)
	}
	export2 := exportBytes(t, st2)
	evs2 := scanAll(t, st2)
	if err := st2.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(export1, export2) {
		t.Fatal("export → import → export is not byte-identical")
	}

	// Folded projections deep-equal.
	p1, err := domain.Fold(evs1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := domain.Fold(evs2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p1, p2) {
		t.Fatal("folded projections differ after round-trip")
	}

	// The derived event_refs table is identical too: the restore path used
	// the same Refs() derivation as the live writes.
	if r1, r2 := readRefs(t, dir1), readRefs(t, dir2); !reflect.DeepEqual(r1, r2) {
		t.Fatalf("event_refs diverged:\nsource   %v\nrestored %v", r1, r2)
	}

	// Resume writing on the restored directory: the writer's reopen path
	// appends writer.started under a FRESH origin; history keeps its own.
	priorOrigins := map[string]struct{}{}
	for _, ev := range evs2 {
		priorOrigins[ev.Origin] = struct{}{}
	}
	st2rw, err := store.Open(dir2)
	if err != nil {
		t.Fatal(err)
	}
	defer st2rw.Close()
	clock.ms += 60000
	w2, err := writer.Open(st2rw, writer.Options{Now: clock.now, Entropy: rand.New(rand.NewSource(4))})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	after := scanAll(t, st2rw)
	if len(after) != len(evs2)+1 {
		t.Fatalf("reopen appended %d events, want exactly 1", len(after)-len(evs2))
	}
	last := after[len(after)-1]
	if last.Type != event.TypeWriterStarted {
		t.Fatalf("reopen appended %s, want writer.started", last.Type)
	}
	if _, used := priorOrigins[last.Origin]; used {
		t.Fatalf("writer.started reused origin %s", last.Origin)
	}
	if last.Origin != w2.Origin() {
		t.Fatalf("writer.started origin %s, writer reports %s", last.Origin, w2.Origin())
	}
	for i, ev := range after[:len(evs2)] {
		if ev.Origin != evs2[i].Origin || ev.ID != evs2[i].ID {
			t.Fatalf("restored event %d changed after reopen: %+v vs %+v", i, ev, evs2[i])
		}
	}
}

// ── import rejection table ──

// baseLog hand-builds a small valid, writer-shaped log (canonical ULID ids
// and batch ids, wr_+ULID origins, one commit ts and actor per batch) whose
// every envelope the rejection cases can tamper with. Batches: b1 =
// log.initialized; b2 = vocabulary + project; b3 = a thing; b4 =
// writer.started (new lineage); b5 = a second thing under the new lineage.
func baseLog(t *testing.T) []event.Envelope {
	t.Helper()
	gen := ulid.NewGenerator(
		func() time.Time { return time.UnixMilli(1784455200000) },
		rand.New(rand.NewSource(42)))
	newULID := func() string {
		u, err := gen.New()
		if err != nil {
			t.Fatal(err)
		}
		return u.String()
	}
	wrA := "wr_" + newULID()
	wrB := "wr_" + newULID()
	b1, b2, b3, b4, b5 := newULID(), newULID(), newULID(), newULID(), newULID()
	seq := int64(0)
	mk := func(origin, batch, ts, typ, entity string, payload event.Payload) event.Envelope {
		data, err := canonjson.Encode(payload)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		return event.Envelope{
			Seq: seq, ID: newULID(), Origin: origin, Batch: batch, TS: ts,
			Actor: "daniel", Type: typ, V: 1, Entity: entity, Data: data,
		}
	}
	const (
		t0 = "2026-07-19T10:00:00.000Z"
		t1 = "2026-07-19T10:00:01.000Z"
		t2 = "2026-07-19T10:00:02.000Z"
		t3 = "2026-07-19T10:00:03.000Z"
		t4 = "2026-07-19T10:00:04.000Z"
	)
	return []event.Envelope{
		mk(wrA, b1, t0, event.TypeLogInitialized, "", &event.LogInitialized{WorkspaceID: "ws_test"}),
		mk(wrA, b2, t1, event.TypeStateDefined, "st_1", &event.StateDefined{Name: "todo", Semantic: event.SemanticPending}),
		mk(wrA, b2, t1, event.TypeTypeDefined, "ty_1", &event.TypeDefined{Name: "task"}),
		mk(wrA, b2, t1, event.TypeProjectCreated, "pr_1", &event.ProjectCreated{Name: "P"}),
		mk(wrA, b3, t2, event.TypeThingCreated, "th_1", &event.ThingCreated{Project: "pr_1", Name: "one", Type: "ty_1"}),
		mk(wrB, b4, t3, event.TypeWriterStarted, "", &event.WriterStarted{}),
		mk(wrB, b5, t4, event.TypeThingCreated, "th_2", &event.ThingCreated{Project: "pr_1", Name: "two", Type: "ty_1"}),
	}
}

func renderLines(t *testing.T, evs []event.Envelope) string {
	t.Helper()
	var buf bytes.Buffer
	for _, ev := range evs {
		line, err := interchange.AppendEnvelope(nil, ev)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.String()
}

// freshULID mints one more valid ULID lexically after the base log's ids.
func freshULID(t *testing.T) string {
	t.Helper()
	gen := ulid.NewGenerator(
		func() time.Time { return time.UnixMilli(1784455300000) },
		rand.New(rand.NewSource(43)))
	u, err := gen.New()
	if err != nil {
		t.Fatal(err)
	}
	return u.String()
}

// assertNothingWritten asserts the data directory is untouched: either still
// nonexistent, or exactly as empty as it started.
func assertNothingWritten(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("failed import left files behind: %v", names)
	}
}

// TestImportRejections exercises one case per import rule (§5.4). Every case
// must fail with an error naming the offending line, and must leave the data
// directory with nothing written.
func TestImportRejections(t *testing.T) {
	cases := []struct {
		name string
		// mutate transforms the base log; nil keeps it as-is.
		mutate func(t *testing.T, evs []event.Envelope) []event.Envelope
		// raw, if set, REPLACES the rendered stream entirely.
		raw *string
		// trailing is appended verbatim after the rendered log.
		trailing string
		wants    []string // required substrings of the error
	}{
		{
			name: "seq gap",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[4].Seq = 6
				return evs
			},
			wants: []string{"line 5: seq 6, want 5"},
		},
		{
			name: "seq not starting at 1",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[0].Seq = 2
				return evs
			},
			wants: []string{"line 1: seq 2, want 1"},
		},
		{
			name: "duplicate id",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[2].ID = evs[1].ID
				return evs
			},
			wants: []string{"line 3: duplicate event id"},
		},
		{
			name: "invalid ULID",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[1].ID = "definitely-not-a-ulid!!!!!"
				return evs
			},
			wants: []string{"line 2: id", "not a canonical ULID"},
		},
		{
			name: "lowercase (case-alias) id",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[1].ID = strings.ToLower(evs[1].ID)
				return evs
			},
			wants: []string{"line 2: id", "not a canonical ULID"},
		},
		{
			name: "case-alias duplicate id",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				// A lowercase alias of line 2's id must not slip past the
				// duplicate check; canonical-form enforcement rejects it.
				evs[2].ID = strings.ToLower(evs[1].ID)
				return evs
			},
			wants: []string{"line 3: id", "not a canonical ULID"},
		},
		{
			name: "batch id not a canonical ULID",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[1].Batch = "b2"
				return evs
			},
			wants: []string{`line 2: batch "b2" is not a canonical ULID`},
		},
		{
			name: "origin not writer-shaped",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[1].Origin = "wr_A"
				return evs
			},
			wants: []string{`line 2: origin "wr_A" is not "wr_" + a canonical ULID`},
		},
		{
			name: "first event not log.initialized",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs = evs[1:]
				for i := range evs {
					evs[i].Seq = int64(i + 1)
				}
				return evs
			},
			wants: []string{`line 1: first event is "state.defined"`},
		},
		{
			name: "garbage ts",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[4].TS = "zzz-garbage-06"
				return evs
			},
			wants: []string{"line 5: ts", "not in the writer timestamp format"},
		},
		{
			name: "seconds-precision ts",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				// The §5.2 prose example's width — a live writer never emits
				// it, and it would poison the reopened writer's clamp.
				evs[4].TS = "2026-07-19T10:00:02Z"
				return evs
			},
			wants: []string{"line 5: ts", "not in the writer timestamp format"},
		},
		{
			name: "ts regression",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[4].TS = "2026-07-19T09:59:59.000Z"
				return evs
			},
			wants: []string{"line 5: ts", "regresses below"},
		},
		{
			name: "intra-batch ts variation",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[2].TS = "2026-07-19T10:00:01.500Z" // b2's other events carry t1
				return evs
			},
			wants: []string{"line 3:", "one batch, one commit timestamp"},
		},
		{
			name: "intra-batch actor variation",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[2].Actor = "mallory"
				return evs
			},
			wants: []string{"line 3:", "one batch, one actor"},
		},
		{
			name: "non-null causes",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				target := evs[0].ID
				evs[1].Causes = &target
				return evs
			},
			wants: []string{"line 2: causes is reserved"},
		},
		{
			name: "writer.started sharing a batch",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				// Pull the b5 thing.created into writer.started's batch.
				evs[6].Batch = evs[5].Batch
				evs[6].TS = evs[5].TS
				return evs
			},
			wants: []string{"line 7:", "writer.started must be the only event of its batch"},
		},
		{
			name: "domain event in the log.initialized batch",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				// Move project.created to line 2, inside the init batch: only
				// state.defined seeds may share it.
				evs[1], evs[3] = evs[3], evs[1]
				evs[1].Batch, evs[1].TS = evs[0].Batch, evs[0].TS
				for i := range evs {
					evs[i].Seq = int64(i + 1)
				}
				return evs
			},
			wants: []string{"line 2: project.created cannot share the log.initialized batch"},
		},
		{
			name: "batch interleaving",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				// Move b2's project.created after b3: b2 resumes at line 5.
				moved := evs[3]
				moved.TS = evs[4].TS
				out := append(append([]event.Envelope{}, evs[:3]...), evs[4], moved)
				out = append(out, evs[5:]...)
				for i := range out {
					out[i].Seq = int64(i + 1)
				}
				return out
			},
			wants: []string{"line 5: batch ", "resumes after other batches"},
		},
		{
			name: "unknown event type",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[4].Type = "thing.exploded"
				return evs
			},
			wants: []string{`line 5: event: unknown event type "thing.exploded"`},
		},
		{
			name: "unsupported v",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[4].V = 99
				return evs
			},
			wants: []string{`line 5: event: unknown event type "thing.created" v99`},
		},
		{
			name: "payload failing shape validation",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[1].Data = []byte(`{"name":"todo","semantic":"warped"}`)
				return evs
			},
			wants: []string{"line 2: event: invalid state.defined"},
		},
		{
			name: "payload not canonical",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[1].Data = []byte(`{"semantic":"pending","name":"todo"}`)
				return evs
			},
			wants: []string{"line 2: payload is not canonical JSON"},
		},
		{
			name: "origin not matching lineage",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				evs[6].Origin = evs[0].Origin // wr_A after writer.started minted wr_B
				return evs
			},
			wants: []string{"line 7: origin ", "does not match the current writer lineage"},
		},
		{
			name: "batch failing domain validation",
			mutate: func(t *testing.T, evs []event.Envelope) []event.Envelope {
				data, err := canonjson.Encode(&event.ThingStateChanged{State: "st_missing"})
				if err != nil {
					t.Fatal(err)
				}
				return append(evs, event.Envelope{
					Seq: 8, ID: freshULID(t), Origin: evs[6].Origin, Batch: freshULID(t),
					TS: "2026-07-19T10:00:05.000Z", Actor: "daniel",
					Type: event.TypeThingStateChanged, V: 1, Entity: "th_1", Data: data,
				})
			},
			wants: []string{"lines 8-8 (batch "},
		},
		{
			name:     "trailing garbage line",
			trailing: "{{{ this is not an envelope\n",
			wants:    []string{"line 8: invalid envelope"},
		},
		{
			name:  "empty file",
			raw:   new(string),
			wants: []string{"the log is empty"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stream string
			if tc.raw != nil {
				stream = *tc.raw
			} else {
				evs := baseLog(t)
				if tc.mutate != nil {
					evs = tc.mutate(t, evs)
				}
				stream = renderLines(t, evs) + tc.trailing
			}
			dir := filepath.Join(t.TempDir(), "ws")
			_, _, err := interchange.Import(dir, strings.NewReader(stream))
			if err == nil {
				t.Fatalf("import accepted a %s log", tc.name)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
			assertNothingWritten(t, dir)
		})
	}
}

// TestImportKilledMidRestoreLeavesNoWorkspace simulates an import killed
// between batch transactions: the abandoned staging file must not open as a
// workspace, and a fresh import into the same directory succeeds.
func TestImportKilledMidRestoreLeavesNoWorkspace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws")
	evs := baseLog(t)

	// Write only the first batch to the restore target, then "die" (closing
	// the store stands in for process death, which would also drop the lock).
	st, err := store.OpenRestore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendRestoredBatch(evs[:1]); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// The half-restored directory is NOT a workspace: no workspace.db.
	if _, err := os.Stat(filepath.Join(dir, store.DBFileName)); !os.IsNotExist(err) {
		t.Fatalf("workspace.db exists after a killed restore: %v", err)
	}
	if _, err := store.OpenReadOnly(dir); err == nil {
		t.Fatal("a half-restored directory must not open as a workspace")
	}

	// A fresh import clears the stale staging file and completes.
	n, _, err := interchange.Import(dir, strings.NewReader(renderLines(t, evs)))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(evs) {
		t.Fatalf("imported %d events, want %d", n, len(evs))
	}
	ro, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if got := scanAll(t, ro); len(got) != len(evs) {
		t.Fatalf("restored log has %d events, want %d", len(got), len(evs))
	}
	if _, err := os.Stat(filepath.Join(dir, store.RestoreDBFileName)); !os.IsNotExist(err) {
		t.Fatalf("staging file still present after a successful import: %v", err)
	}
}

func TestImportAcceptsBaseLogIntoExistingEmptyDir(t *testing.T) {
	dir := t.TempDir() // exists, empty
	evs := baseLog(t)
	n, nb, err := interchange.Import(dir, strings.NewReader(renderLines(t, evs)))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(evs) || nb != 5 {
		t.Fatalf("imported %d events / %d batches, want %d / 5", n, nb, len(evs))
	}
	st, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got := scanAll(t, st)
	if !reflect.DeepEqual(got, evs) {
		t.Fatalf("restored events differ:\n got %+v\nwant %+v", got, evs)
	}
}

func TestImportRefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	stray := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(stray, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := interchange.Import(dir, strings.NewReader(renderLines(t, baseLog(t))))
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("got %v, want a not-empty refusal", err)
	}
	data, err := os.ReadFile(stray)
	if err != nil || string(data) != "keep me" {
		t.Fatalf("stray file disturbed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("import wrote into a non-empty dir: %v", entries)
	}
}
