package writer

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"churn/internal/domain"
	"churn/internal/domain/domaintest"
	"churn/internal/event"
	"churn/internal/store"

	_ "modernc.org/sqlite"
)

// testClock is a settable clock. Not safe for concurrent mutation — tests
// set it only between synchronous writer calls.
type testClock struct{ ms int64 }

func (c *testClock) now() time.Time { return time.UnixMilli(c.ms) }

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func scanAll(t *testing.T, st *store.Store) []event.Envelope {
	t.Helper()
	var evs []event.Envelope
	if err := st.Scan(func(ev event.Envelope) error { evs = append(evs, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestFreshDirWritesLogInitialized(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	defer st.Close()

	clock := &testClock{ms: 1721390000000}
	w, err := Open(st, Options{Now: clock.now, Entropy: rand.New(rand.NewSource(1))})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	p := w.Projection()
	if p.WorkspaceID == "" || !strings.HasPrefix(p.WorkspaceID, "ws_") {
		t.Fatalf("WorkspaceID = %q", p.WorkspaceID)
	}
	if p.Origin != w.Origin() || !strings.HasPrefix(p.Origin, "wr_") {
		t.Fatalf("Origin = %q, writer origin %q", p.Origin, w.Origin())
	}
	// 1 log.initialized + 5 seeded default states.
	if p.LastSeq != 6 {
		t.Fatalf("LastSeq = %d", p.LastSeq)
	}

	evs := scanAll(t, st)
	if len(evs) != 6 || evs[0].Type != event.TypeLogInitialized || evs[0].Seq != 1 {
		t.Fatalf("unexpected log contents: %+v", evs)
	}
	// log.initialized and the seeded states are ONE batch (one transaction):
	// no crash window can leave a workspace without its default vocabulary.
	for _, ev := range evs {
		if ev.Batch != evs[0].Batch {
			t.Fatalf("seed event %d in batch %q, want the log.initialized batch %q", ev.Seq, ev.Batch, evs[0].Batch)
		}
	}
	wantData := fmt.Sprintf(`{"workspace_id":%q}`, p.WorkspaceID)
	if string(evs[0].Data) != wantData {
		t.Fatalf("payload not canonical: %s, want %s", evs[0].Data, wantData)
	}
	if evs[0].Actor != "system" {
		t.Fatalf("actor = %q", evs[0].Actor)
	}
}

func TestFreshDirSeedsDefaultStates(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	defer st.Close()
	w, err := Open(st, Options{Now: (&testClock{ms: 1721390000000}).now, Entropy: rand.New(rand.NewSource(7))})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// The §2.2 defaults ship as ordinary state.defined events, actor system.
	p := w.Projection()
	bySemantic := map[string]string{}
	for id, s := range p.States {
		if !strings.HasPrefix(id, "st_") {
			t.Fatalf("state id %q lacks st_ prefix", id)
		}
		bySemantic[s.Semantic] = s.Name
	}
	want := map[string]string{
		"pending": "todo", "active": "in_progress", "satisfied": "done",
		"paused": "on_hold", "abandoned": "cancelled",
	}
	if !reflect.DeepEqual(bySemantic, want) {
		t.Fatalf("seeded states = %v, want %v", bySemantic, want)
	}
	for _, ev := range scanAll(t, st)[1:] {
		if ev.Type != event.TypeStateDefined || ev.Actor != "system" {
			t.Fatalf("seed event %+v, want state.defined by system", ev)
		}
	}
}

func TestReopenAppendsWriterStartedWithFreshLineage(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{ms: 1721390000000}

	st := openStore(t, dir)
	w, err := Open(st, Options{Now: clock.now, Entropy: rand.New(rand.NewSource(1))})
	if err != nil {
		t.Fatal(err)
	}
	ws, firstOrigin := w.Projection().WorkspaceID, w.Origin()
	w.Close()
	st.Close()

	clock.ms += 5000
	st = openStore(t, dir)
	defer st.Close()
	w, err = Open(st, Options{Now: clock.now, Entropy: rand.New(rand.NewSource(2))})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	p := w.Projection()
	if p.WorkspaceID != ws {
		t.Fatalf("workspace changed across restart: %q → %q", ws, p.WorkspaceID)
	}
	if p.Origin == firstOrigin {
		t.Fatal("lineage not renewed on resume")
	}
	if p.LastSeq != 7 {
		t.Fatalf("LastSeq = %d", p.LastSeq)
	}
	evs := scanAll(t, st)
	if evs[6].Type != event.TypeWriterStarted || evs[6].Origin != p.Origin {
		t.Fatalf("resume event: %+v", evs[6])
	}
	// Historical event keeps the origin it was written under.
	if evs[0].Origin != firstOrigin {
		t.Fatalf("history rewritten: first event origin %q", evs[0].Origin)
	}
}

func TestReplayDeterminism(t *testing.T) {
	// A generated sequence of appends across restarts must produce a live
	// projection deep-equal to a from-disk fold, after every step.
	dir := t.TempDir()
	clock := &testClock{ms: 1721390000000}
	rng := rand.New(rand.NewSource(42))

	for cycle := 0; cycle < 8; cycle++ {
		st := openStore(t, dir)
		w, err := Open(st, Options{Now: clock.now, Entropy: rand.New(rand.NewSource(int64(cycle)))})
		if err != nil {
			t.Fatal(err)
		}

		// Clock wanders, sometimes backwards.
		clock.ms += rng.Int63n(2000) - 500

		live := w.Projection()
		replayed, err := domain.Fold(scanAll(t, st))
		if err != nil {
			t.Fatalf("cycle %d: fold from disk: %v", cycle, err)
		}
		if !reflect.DeepEqual(live, replayed) {
			t.Fatalf("cycle %d: live projection diverged from replay:\nlive   %+v\nreplay %+v",
				cycle, live, replayed)
		}

		w.Close()
		st.Close()
	}
}

func TestSubmitDomainBatchAndRefs(t *testing.T) {
	// No deferred closes: the test closes store and writer explicitly to
	// inspect the database file afterwards.
	dir := t.TempDir()
	st := openStore(t, dir)
	w, err := Open(st, Options{Now: (&testClock{ms: 1721390000000}).now, Entropy: rand.New(rand.NewSource(3))})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}

	evs, err := w.Submit("daniel", []Command{
		{Type: event.TypeTypeDefined, V: 1, Entity: "ty_task", Payload: event.TypeDefined{Name: "task"}},
		{Type: event.TypeProjectCreated, V: 1, Entity: "pr_1", Payload: event.ProjectCreated{Name: "Alpha"}},
		{Type: event.TypeThingCreated, V: 1, Entity: "th_1", Payload: event.ThingCreated{
			Project: "pr_1", Name: "First", Type: "ty_task"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].Batch != evs[2].Batch {
		t.Fatalf("committed envelopes: %+v", evs)
	}
	p := w.Projection()
	if p.Things["th_1"] == nil || p.Things["th_1"].Name != "First" {
		t.Fatalf("projection not updated: %+v", p.Things)
	}
	if got := p.Version("th_1"); got != evs[2].Seq {
		t.Fatalf("version = %d, want %d", got, evs[2].Seq)
	}

	// A domain-invalid batch surfaces the structured error through Submit.
	_, err = w.Submit("daniel", []Command{
		{Type: event.TypeDependencyAsserted, V: 1, Entity: "dep_1", Payload: event.DependencyAsserted{
			From: "th_1", To: "th_1"}},
	}, nil)
	var de *domain.Error
	if !errors.As(err, &de) || de.Kind != domain.KindCycle {
		t.Fatalf("want structured cycle error, got %v", err)
	}

	// The thing.created event derived its event_refs rows from the payload.
	w.Close()
	st.Close()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, store.DBFileName)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(
		`SELECT entity_id, role FROM event_refs WHERE event_seq = ? ORDER BY role`, evs[2].Seq)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][2]string
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]string{id, role})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"pr_1", "project"}, {"ty_task", "type"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event_refs = %v, want %v", got, want)
	}
}

func TestExpectedVersionsThroughSubmit(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	defer st.Close()
	w, err := Open(st, Options{Now: (&testClock{ms: 1721390000000}).now, Entropy: rand.New(rand.NewSource(4))})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	evs, err := w.Submit("daniel", []Command{
		{Type: event.TypeProjectCreated, V: 1, Entity: "pr_1", Payload: event.ProjectCreated{Name: "Alpha"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := evs[0].Seq

	// Matching expectation commits.
	if _, err := w.Submit("daniel", []Command{
		{Type: event.TypeProjectSuperseded, V: 1, Entity: "pr_1", Payload: event.ProjectSuperseded{Name: "Alpha v2"}},
	}, map[string]int64{"pr_1": v}); err != nil {
		t.Fatal(err)
	}

	// A second editor still holding version v conflicts loudly.
	_, err = w.Submit("daniel", []Command{
		{Type: event.TypeProjectSuperseded, V: 1, Entity: "pr_1", Payload: event.ProjectSuperseded{Name: "Alpha v2b"}},
	}, map[string]int64{"pr_1": v})
	var de *domain.Error
	if !errors.As(err, &de) || de.Kind != domain.KindStaleVersion || len(de.IDs) != 1 || de.IDs[0] != "pr_1" {
		t.Fatalf("want stale_version [pr_1], got %v", err)
	}
	if w.Projection().Projects["pr_1"].Name != "Alpha v2" {
		t.Fatal("stale batch must not commit")
	}
	// Nothing of the precondition is persisted: the log gained no events.
	if got := w.Projection().LastSeq; got != evs[0].Seq+1 {
		t.Fatalf("LastSeq = %d, want %d", got, evs[0].Seq+1)
	}
}

// TestReplayDeterminismFuzz extends M1's replay test over the full catalog:
// a fuzzed command stream through Submit (with a restart in the middle), then
// Fold(Scan(disk)) must deep-equal the live projection.
func TestReplayDeterminismFuzz(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{ms: 1721390000000}
	g := domaintest.NewGenerator(11)

	st := openStore(t, dir)
	w, err := Open(st, Options{Now: clock.now, Entropy: rand.New(rand.NewSource(1))})
	if err != nil {
		t.Fatal(err)
	}

	submitFuzz := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			cmds := g.Next(w.Projection())
			if cmds == nil {
				continue
			}
			wcmds := make([]Command, len(cmds))
			for j, c := range cmds {
				wcmds[j] = Command{Type: c.Type, V: c.V, Entity: c.Entity, Payload: c.Payload}
			}
			clock.ms += int64(rng2(i)) // wandering clock
			if _, err := w.Submit("fuzz", wcmds, nil); err != nil {
				t.Fatalf("fuzz batch %d rejected: %v", i, err)
			}
		}
	}
	submitFuzz(80)

	// Restart mid-stream: replay must reconstruct the projection exactly.
	live := w.Projection()
	replayed, err := domain.Fold(scanAll(t, st))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, replayed) {
		t.Fatal("live projection diverged from replay before restart")
	}
	w.Close()
	st.Close()

	st = openStore(t, dir)
	defer st.Close()
	w, err = Open(st, Options{Now: clock.now, Entropy: rand.New(rand.NewSource(2))})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	submitFuzz(60)

	live = w.Projection()
	if err := domaintest.CheckInvariants(live); err != nil {
		t.Fatalf("invariants after fuzz: %v", err)
	}
	replayed, err = domain.Fold(scanAll(t, st))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, replayed) {
		t.Fatal("live projection diverged from replay after restart")
	}
}

// rng2 is a tiny deterministic jitter for the fuzz clock.
func rng2(i int) int { return (i*7919)%1500 - 300 }

func TestTimestampsMonotoneWithBackwardsClock(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{ms: 1721390000000}

	// Restart repeatedly with a clock that steps back 10s each time.
	for i := 0; i < 4; i++ {
		st := openStore(t, dir)
		w, err := Open(st, Options{Now: clock.now, Entropy: rand.New(rand.NewSource(int64(i)))})
		if err != nil {
			t.Fatalf("open %d (clock backwards): %v", i, err)
		}
		w.Close()
		st.Close()
		clock.ms -= 10000
	}

	st := openStore(t, dir)
	defer st.Close()
	evs := scanAll(t, st)
	// First open: log.initialized + 5 seeds; three reopens: writer.started each.
	if len(evs) != 9 {
		t.Fatalf("expected 9 events, got %d", len(evs))
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].TS < evs[i-1].TS {
			t.Fatalf("ts decreased at seq %d: %q after %q", evs[i].Seq, evs[i].TS, evs[i-1].TS)
		}
		if evs[i].Seq != evs[i-1].Seq+1 {
			t.Fatalf("seq gap at %d", i)
		}
	}
	// And the whole log still folds (the fold enforces ts monotonicity).
	if _, err := domain.Fold(evs); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitRejections(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	defer st.Close()
	clock := &testClock{ms: 1721390000000}
	w, err := Open(st, Options{Now: clock.now, Entropy: rand.New(rand.NewSource(1))})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Submit("daniel", nil, nil); err == nil {
		t.Fatal("empty batch must be rejected")
	}
	if _, err := w.Submit("", []Command{{Type: "x", V: 1}}, nil); err == nil {
		t.Fatal("empty actor must be rejected")
	}
	if _, err := w.Submit("daniel", []Command{
		{Type: event.TypeWriterStarted, V: 1, Payload: event.WriterStarted{}},
	}, nil); err == nil || !strings.Contains(err.Error(), "writer-internal") {
		t.Fatalf("lifecycle events must be rejected via Submit, got %v", err)
	}
	if _, err := w.Submit("daniel", []Command{
		{Type: "gizmo.created", V: 1, Payload: map[string]any{"name": "x"}},
	}, nil); err == nil || !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("unknown type must fail closed, got %v", err)
	}
	if _, err := w.Submit("daniel", []Command{
		{Type: event.TypeThingCreated, V: 2, Entity: "th_1", Payload: map[string]any{"name": "x"}},
	}, nil); err == nil || !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("unsupported version must fail closed, got %v", err)
	}

	// Nothing above may have written anything.
	if got := len(scanAll(t, st)); got != 6 {
		t.Fatalf("rejected submits wrote events: log has %d", got)
	}
	// A rejected batch leaves the projection untouched.
	if w.Projection().LastSeq != 6 {
		t.Fatalf("projection moved: %+v", w.Projection())
	}
}

func TestSubmitAfterCloseFails(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	defer st.Close()
	w, err := Open(st, Options{Now: (&testClock{ms: 1}).now, Entropy: rand.New(rand.NewSource(1))})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := w.Submit("daniel", []Command{{Type: "x", V: 1}}, nil); err == nil {
		t.Fatal("Submit after Close must fail")
	}
}

func TestPublishFailureAfterCommitIsFatal(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	defer st.Close()
	clock := &testClock{ms: 1721390000000}

	var fatalErr error
	w, err := Open(st, Options{
		Now:     clock.now,
		Entropy: rand.New(rand.NewSource(1)),
		Fatal:   func(err error) { fatalErr = err },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Sabotage: write to the log behind the writer's back, so the store's
	// next assigned seq disagrees with the writer's candidate projection.
	if _, err := st.AppendBatch([]event.Envelope{{
		ID: "01SNEAKY", Origin: "wr_evil", Batch: "b", TS: "2026-07-19T10:00:00.000Z",
		Actor: "evil", Type: event.TypeWriterStarted, V: 1, Data: []byte(`{}`),
	}}, nil); err != nil {
		t.Fatal(err)
	}

	// White-box: drive the append path directly (Submit rejects lifecycle
	// events).
	_, err = w.append("system", []Command{
		{Type: event.TypeWriterStarted, V: 1, Payload: event.WriterStarted{}},
	}, nil)
	if err == nil {
		t.Fatal("expected post-commit failure")
	}
	if fatalErr == nil {
		t.Fatal("Fatal hook was not invoked on publish failure after commit")
	}
	if !strings.Contains(fatalErr.Error(), "seq mismatch") {
		t.Fatalf("unexpected fatal error: %v", fatalErr)
	}
}

func TestAmbiguousCommitIsFatal(t *testing.T) {
	// A COMMIT that errors may still have landed durably; treating it as
	// "nothing written" would leave the projection behind durable truth and
	// invite a duplicating retry. The writer must take the Fatal path.
	dir := t.TempDir()
	st := openStore(t, dir)
	defer st.Close()

	var fatalErr error
	w, err := Open(st, Options{
		Now:     (&testClock{ms: 1721390000000}).now,
		Entropy: rand.New(rand.NewSource(1)),
		Fatal:   func(err error) { fatalErr = err },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Fault-inject via the appendBatch seam: report an ambiguous commit.
	w.appendBatch = func([]event.Envelope, []store.Ref) ([]event.Envelope, error) {
		return nil, &store.AmbiguousCommitError{Err: fmt.Errorf("disk full during commit")}
	}
	_, err = w.append("system", []Command{
		{Type: event.TypeWriterStarted, V: 1, Payload: event.WriterStarted{}},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if fatalErr == nil {
		t.Fatal("ambiguous commit did not take the Fatal path")
	}
	var ambiguous *store.AmbiguousCommitError
	if !errors.As(fatalErr, &ambiguous) {
		t.Fatalf("fatal error lost its classification: %v", fatalErr)
	}

	// A rolled-back-for-certain error, by contrast, is an ordinary
	// rejection: no Fatal, projection untouched.
	fatalErr = nil
	before := w.Projection()
	w.appendBatch = func([]event.Envelope, []store.Ref) ([]event.Envelope, error) {
		return nil, fmt.Errorf("constraint violation before commit")
	}
	if _, err := w.append("system", []Command{
		{Type: event.TypeWriterStarted, V: 1, Payload: event.WriterStarted{}},
	}, nil); err == nil {
		t.Fatal("expected error")
	}
	if fatalErr != nil {
		t.Fatalf("pre-commit failure must not be fatal: %v", fatalErr)
	}
	if w.Projection() != before {
		t.Fatal("rejected batch moved the projection")
	}
}
