// M4 store tests: reader/writer concurrency, read-only opens, the
// restore-path append, reindex, and online backup.
package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"churn/internal/event"
)

// appendSimpleLog seeds a store with n events after log.initialized, one
// batch per event.
func appendSimpleLog(t *testing.T, s *Store, n int) {
	t.Helper()
	if _, err := s.AppendBatch([]event.Envelope{
		testEvent("01AAAAAAAAAAAAAAAAAAAAAAA0", event.TypeLogInitialized, "", `{"workspace_id":"ws_1"}`),
	}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		ev := testEvent(fmt.Sprintf("01AAAAAAAAAAAAAAAAAAAAAB%02d", i), event.TypeWriterStarted, "", `{}`)
		if _, err := s.AppendBatch([]event.Envelope{ev}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// TestScanConcurrentWithAppend is the M1 review fix's gate: a slow Scan must
// not block AppendBatch (WAL: one writer, N readers), and the scan sees the
// snapshot taken when it started — not the concurrently appended rows.
func TestScanConcurrentWithAppend(t *testing.T) {
	s := openTestStore(t)
	appendSimpleLog(t, s, 2) // 3 events total

	firstRow := make(chan struct{})
	appended := make(chan struct{})
	scanDone := make(chan error, 1)
	seen := 0
	go func() {
		scanDone <- s.Scan(func(ev event.Envelope) error {
			seen++
			if seen == 1 {
				close(firstRow)
				<-appended
			}
			return nil
		})
	}()

	<-firstRow
	// The append must complete while the scan is mid-flight and holding its
	// read snapshot. Run it with a watchdog so a regression to the old
	// single-connection behavior fails fast instead of hanging the test.
	appendErr := make(chan error, 1)
	go func() {
		ev := testEvent("01AAAAAAAAAAAAAAAAAAAAAAC0", event.TypeWriterStarted, "", `{}`)
		_, err := s.AppendBatch([]event.Envelope{ev}, nil)
		appendErr <- err
	}()
	select {
	case err := <-appendErr:
		if err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("AppendBatch blocked behind a running Scan")
	}
	close(appended)

	if err := <-scanDone; err != nil {
		t.Fatal(err)
	}
	// Snapshot isolation: the scan's read transaction started before the
	// append committed, so the scan saw exactly the pre-append log.
	if seen != 3 {
		t.Fatalf("scan saw %d events, want the 3-event snapshot at scan start", seen)
	}
	// A fresh scan sees the appended row.
	after := 0
	if err := s.Scan(func(event.Envelope) error { after++; return nil }); err != nil {
		t.Fatal(err)
	}
	if after != 4 {
		t.Fatalf("post-append scan saw %d events, want 4", after)
	}
}

// TestScanCallbackCanAppend pins the deadlock case from the M1 review: a
// scan callback that performs (or waits on) an append must complete, because
// the writer has a dedicated connection the read pool can never exhaust.
func TestScanCallbackCanAppend(t *testing.T) {
	s := openTestStore(t)
	appendSimpleLog(t, s, 1) // 2 events

	seen := 0
	err := s.Scan(func(ev event.Envelope) error {
		seen++
		if seen == 1 {
			ev := testEvent("01AAAAAAAAAAAAAAAAAAAAAAD0", event.TypeWriterStarted, "", `{}`)
			if _, err := s.AppendBatch([]event.Envelope{ev}, nil); err != nil {
				return fmt.Errorf("append from scan callback: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatalf("scan saw %d events, want its 2-event snapshot", seen)
	}
}

func TestReadPoolIsQueryOnly(t *testing.T) {
	s := openTestStore(t)
	var qo int
	if err := s.rdb.QueryRow(`PRAGMA query_only`).Scan(&qo); err != nil {
		t.Fatal(err)
	}
	if qo != 1 {
		t.Fatalf("read pool query_only = %d, want 1", qo)
	}
	if _, err := s.rdb.Exec(`INSERT INTO event_refs (event_seq, entity_id, role) VALUES (1, 'x', 'y')`); err == nil {
		t.Fatal("write through the read pool must fail")
	}
}

func TestOpenReadOnly(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenReadOnly(dir); err == nil {
		t.Fatal("OpenReadOnly on a missing workspace must fail")
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	appendSimpleLog(t, s, 2)

	// While the writable store holds the lock, a read-only open succeeds —
	// this is the export/backup-against-a-live-server path.
	ro, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	n := 0
	if err := ro.Scan(func(event.Envelope) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("read-only scan saw %d events, want 3", n)
	}
	if _, err := ro.AppendBatch([]event.Envelope{
		testEvent("01AAAAAAAAAAAAAAAAAAAAAAE0", event.TypeWriterStarted, "", `{}`),
	}, nil); !errors.Is(err, errReadOnly) {
		t.Fatalf("AppendBatch on read-only store: got %v, want errReadOnly", err)
	}
	if err := ro.AppendRestoredBatch([]event.Envelope{
		testEvent("01AAAAAAAAAAAAAAAAAAAAAAE1", event.TypeWriterStarted, "", `{}`),
	}); !errors.Is(err, errReadOnly) {
		t.Fatalf("AppendRestoredBatch on read-only store: got %v, want errReadOnly", err)
	}
	if _, err := ro.Reindex(); !errors.Is(err, errReadOnly) {
		t.Fatalf("Reindex on read-only store: got %v, want errReadOnly", err)
	}
}

// restoredEnvelope builds a fully-specified envelope as a restore would see
// it: every field, including seq, comes from the source log.
func restoredEnvelope(seq int64, id, typ, entity, data string) event.Envelope {
	return event.Envelope{
		Seq: seq, ID: id, Origin: "wr_orig", Batch: "01BATCHAAAAAAAAAAAAAAAAAA0",
		TS: "2026-07-18T09:00:00.000Z", Actor: "restore-actor",
		Type: typ, V: 1, Entity: entity, Data: []byte(data),
	}
}

func TestAppendRestoredBatchVerbatim(t *testing.T) {
	s := openTestStore(t)
	batch := []event.Envelope{
		restoredEnvelope(1, "01AAAAAAAAAAAAAAAAAAAAAAF0", event.TypeLogInitialized, "", `{"workspace_id":"ws_r"}`),
		restoredEnvelope(2, "01AAAAAAAAAAAAAAAAAAAAAAF1", event.TypeProjectCreated, "pr_1", `{"name":"P"}`),
		restoredEnvelope(3, "01AAAAAAAAAAAAAAAAAAAAAAF2", event.TypeTypeDefined, "ty_1", `{"name":"task"}`),
		restoredEnvelope(4, "01AAAAAAAAAAAAAAAAAAAAAAF3", event.TypeThingCreated, "th_1",
			`{"name":"T","project":"pr_1","type":"ty_1"}`),
	}
	if err := s.AppendRestoredBatch(batch); err != nil {
		t.Fatal(err)
	}

	got := 0
	if err := s.Scan(func(ev event.Envelope) error {
		want := batch[got]
		if ev.Seq != want.Seq || ev.ID != want.ID || ev.Origin != want.Origin ||
			ev.Batch != want.Batch || ev.TS != want.TS || ev.Actor != want.Actor ||
			ev.Type != want.Type || ev.V != want.V || ev.Entity != want.Entity ||
			string(ev.Data) != string(want.Data) || ev.Causes != nil {
			t.Fatalf("restored event %d not verbatim:\n got %+v\nwant %+v", got, ev, want)
		}
		got++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got != 4 {
		t.Fatalf("scanned %d events, want 4", got)
	}

	// event_refs derived exactly as a live append would have.
	rows, err := s.rdb.Query(`SELECT event_seq, entity_id, role FROM event_refs ORDER BY event_seq, entity_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var seq int64
		var ent, role string
		if err := rows.Scan(&seq, &ent, &role); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, fmt.Sprintf("%d/%s/%s", seq, ent, role))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"4/pr_1/project", "4/ty_1/type"}
	if len(refs) != 2 || refs[0] != want[0] || refs[1] != want[1] {
		t.Fatalf("event_refs = %v, want %v", refs, want)
	}

	// Seq must continue contiguously; a gap writes nothing.
	gap := restoredEnvelope(7, "01AAAAAAAAAAAAAAAAAAAAAAF7", event.TypeWriterStarted, "", `{}`)
	if err := s.AppendRestoredBatch([]event.Envelope{gap}); err == nil {
		t.Fatal("seq gap must be rejected")
	}
	// A foreign ts format is rejected at the restore path too — the last
	// line of defense for the writer's monotone clamp.
	badTS := restoredEnvelope(5, "01AAAAAAAAAAAAAAAAAAAAAAF8", event.TypeWriterStarted, "", `{}`)
	badTS.TS = "2026-07-18T09:00:01Z" // seconds precision: not the writer's layout
	if err := s.AppendRestoredBatch([]event.Envelope{badTS}); err == nil ||
		!strings.Contains(err.Error(), "writer timestamp format") {
		t.Fatalf("foreign ts format: got %v, want a format rejection", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("failed restore left %d events, want 4", n)
	}

	// The append-only triggers are still armed after a restore.
	if _, err := s.db.Exec(`UPDATE events SET actor = 'mallory' WHERE seq = 1`); err == nil {
		t.Fatal("UPDATE after restore must raise: append-only")
	}
	if _, err := s.db.Exec(`DELETE FROM events WHERE seq = 1`); err == nil {
		t.Fatal("DELETE after restore must raise: append-only")
	}
}

// TestReindexRebuildsFromEvents corrupts event_refs directly (the triggers
// guard events, not the derived table) and asserts Reindex reconstructs
// exactly the freshly-derived rows.
func TestReindexRebuildsFromEvents(t *testing.T) {
	s := openTestStore(t)
	if err := s.AppendRestoredBatch([]event.Envelope{
		restoredEnvelope(1, "01AAAAAAAAAAAAAAAAAAAAAAG0", event.TypeLogInitialized, "", `{"workspace_id":"ws_r"}`),
		restoredEnvelope(2, "01AAAAAAAAAAAAAAAAAAAAAAG1", event.TypeProjectCreated, "pr_1", `{"name":"P"}`),
		restoredEnvelope(3, "01AAAAAAAAAAAAAAAAAAAAAAG2", event.TypeTypeDefined, "ty_1", `{"name":"task"}`),
		restoredEnvelope(4, "01AAAAAAAAAAAAAAAAAAAAAAG3", event.TypeThingCreated, "th_1",
			`{"name":"A","project":"pr_1","type":"ty_1"}`),
		restoredEnvelope(5, "01AAAAAAAAAAAAAAAAAAAAAAG4", event.TypeThingCreated, "th_2",
			`{"name":"B","parent":"th_1","project":"pr_1","type":"ty_1"}`),
		restoredEnvelope(6, "01AAAAAAAAAAAAAAAAAAAAAAG5", event.TypeDependencyAsserted, "dep_1",
			`{"from":"th_2","to":"th_1"}`),
	}); err != nil {
		t.Fatal(err)
	}

	readRefs := func() []string {
		t.Helper()
		rows, err := s.rdb.Query(`SELECT event_seq, entity_id, role FROM event_refs ORDER BY event_seq, entity_id, role`)
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
	want := readRefs()
	if len(want) == 0 {
		t.Fatal("expected derived refs from the restore")
	}

	// Corrupt the derived table: delete a row, add a bogus one, mangle another.
	for _, stmt := range []string{
		`DELETE FROM event_refs WHERE entity_id = 'pr_1'`,
		`INSERT INTO event_refs (event_seq, entity_id, role) VALUES (999, 'th_bogus', 'ghost')`,
		`UPDATE event_refs SET role = 'mangled' WHERE entity_id = 'ty_1' AND event_seq = 4`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if got := readRefs(); len(got) == len(want) {
		t.Fatal("corruption did not change event_refs; test is vacuous")
	}

	n, err := s.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	got := readRefs()
	if n != len(got) {
		t.Fatalf("Reindex reported %d rows, table has %d", n, len(got))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("reindexed refs = %v\nwant fresh derivation %v", got, want)
	}
}

func TestBackupSnapshotAndRefusals(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	appendSimpleLog(t, s, 3)

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(dest); err != nil {
		t.Fatal(err)
	}
	// The destination must not be overwritten — and the refusal must not
	// touch the existing file either (it is somebody's data).
	before, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(dest); err == nil {
		t.Fatal("backup onto an existing file must fail")
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed backup modified the pre-existing destination file")
	}

	// The snapshot opens as an ordinary workspace database: same events,
	// append-only triggers intact.
	bdir := t.TempDir()
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdir, DBFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := Open(bdir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	n := 0
	if err := b.Scan(func(ev event.Envelope) error {
		n++
		if ev.Seq != int64(n) {
			t.Fatalf("backup seq %d at position %d", ev.Seq, n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("backup contains %d events, want 4", n)
	}
	if _, err := b.db.Exec(`UPDATE events SET actor = 'mallory' WHERE seq = 1`); err == nil {
		t.Fatal("backup's append-only triggers must have been copied")
	}
}

// TestBackupWorksReadOnlyAgainstHeldWorkspace covers the CLI path: churn
// backup opens the workspace read-only (no lock) while a server holds it.
func TestBackupWorksReadOnlyAgainstHeldWorkspace(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	appendSimpleLog(t, s, 1)

	ro, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := ro.Backup(dest); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Size() == 0 {
		t.Fatalf("backup file: %v (size %v)", err, fi)
	}
}
