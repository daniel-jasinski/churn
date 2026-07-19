package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"churn/internal/event"
)

func testEvent(id, typ, entity, data string) event.Envelope {
	return event.Envelope{
		ID:     id,
		Origin: "wr_1",
		Batch:  "batch_" + id,
		TS:     "2026-07-19T10:00:00.000Z",
		Actor:  "test",
		Type:   typ,
		V:      1,
		Entity: entity,
		Data:   []byte(data),
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesSchemaAndWAL(t *testing.T) {
	s := openTestStore(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	// Pin every pragma the DSN sets as actually in effect: a typo'd
	// _pragma name would otherwise silently no-op.
	for _, p := range []struct {
		pragma string
		want   int
	}{
		{"synchronous", 2}, // FULL
		{"recursive_triggers", 1},
		{"busy_timeout", 5000},
	} {
		var got int
		if err := s.db.QueryRow(`PRAGMA ` + p.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", p.pragma, err)
		}
		if got != p.want {
			t.Fatalf("PRAGMA %s = %d, want %d", p.pragma, got, p.want)
		}
	}
	for _, table := range []string{"events", "event_refs"} {
		var n int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("table %s missing", table)
		}
	}
}

func TestAppendBatchAssignsSeqAndScanReturnsInOrder(t *testing.T) {
	s := openTestStore(t)
	b1, err := s.AppendBatch([]event.Envelope{
		testEvent("01A", event.TypeLogInitialized, "", `{"workspace_id":"ws_1"}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b1[0].Seq != 1 {
		t.Fatalf("first seq = %d", b1[0].Seq)
	}
	b2, err := s.AppendBatch([]event.Envelope{
		testEvent("01B", event.TypeWriterStarted, "", `{}`),
		testEvent("01C", event.TypeWriterStarted, "ent_1", `{}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b2[0].Seq != 2 || b2[1].Seq != 3 {
		t.Fatalf("batch seqs = %d, %d", b2[0].Seq, b2[1].Seq)
	}

	var got []event.Envelope
	if err := s.Scan(func(ev event.Envelope) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("scanned %d events", len(got))
	}
	want := append(append([]event.Envelope{}, b1...), b2...)
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].ID != want[i].ID ||
			got[i].Type != want[i].Type || got[i].Entity != want[i].Entity ||
			string(got[i].Data) != string(want[i].Data) ||
			got[i].TS != want[i].TS || got[i].Actor != want[i].Actor ||
			got[i].Origin != want[i].Origin || got[i].Batch != want[i].Batch ||
			got[i].V != want[i].V || got[i].Causes != nil {
			t.Fatalf("event %d mismatch:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestAppendOnlyTriggersRaise(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.AppendBatch([]event.Envelope{
		testEvent("01A", event.TypeLogInitialized, "", `{"workspace_id":"ws_1"}`),
	}, nil); err != nil {
		t.Fatal(err)
	}

	_, err := s.db.Exec(`UPDATE events SET actor = 'mallory' WHERE seq = 1`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE must raise, got %v", err)
	}
	_, err = s.db.Exec(`DELETE FROM events WHERE seq = 1`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE must raise, got %v", err)
	}

	// INSERT OR REPLACE resolves the conflict via an implicit DELETE. With
	// SQLite's default recursive_triggers=0 that DELETE would skip the
	// trigger and silently rewrite history — the store sets
	// recursive_triggers(1) precisely so this raises. Conflict on the
	// primary key (seq) and on the UNIQUE id, plus the UPDATE OR REPLACE
	// variant.
	replaceStmts := []string{
		`INSERT OR REPLACE INTO events (seq, id, origin, batch, ts, actor, type, v, data)
		 VALUES (1, '01ZZZ', 'wr_evil', 'b', 't', 'mallory', 'x', 1, '{}')`,
		`INSERT OR REPLACE INTO events (seq, id, origin, batch, ts, actor, type, v, data)
		 VALUES (999, '01A', 'wr_evil', 'b', 't', 'mallory', 'x', 1, '{}')`,
		`UPDATE OR REPLACE events SET actor = 'mallory' WHERE seq = 1`,
	}
	for _, stmt := range replaceStmts {
		if _, err := s.db.Exec(stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s\nmust raise append-only, got %v", stmt, err)
		}
	}

	// The row is intact.
	var actor, id string
	if err := s.db.QueryRow(`SELECT actor, id FROM events WHERE seq = 1`).Scan(&actor, &id); err != nil {
		t.Fatal(err)
	}
	if actor != "test" || id != "01A" {
		t.Fatalf("row rewritten: actor=%q id=%q", actor, id)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("event count = %d after rejected writes", n)
	}
}

func TestOpenPathWithURIDelimiters(t *testing.T) {
	// '#' would truncate a raw file: URI (creating the DB outside the data
	// directory — two dirs "work#a"/"work#b" would then share one DB while
	// each holds its own lock); '%' would fail to parse. uriPath escapes
	// both.
	for _, name := range []string{"work#a", "pct%20dir", "both#and%x"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, name)
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.AppendBatch([]event.Envelope{
				testEvent("01A", event.TypeLogInitialized, "", `{"workspace_id":"ws_1"}`),
			}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, DBFileName)); err != nil {
				t.Fatalf("database not inside the data directory: %v", err)
			}
			// Nothing may have been created next to the directory (the
			// truncation failure mode).
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if !e.IsDir() {
					t.Fatalf("stray file %q created outside the data directory", e.Name())
				}
			}
		})
	}
}

func TestBatchAtomicityFailedInsertLeavesNothing(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.AppendBatch([]event.Envelope{
		testEvent("01A", event.TypeLogInitialized, "", `{"workspace_id":"ws_1"}`),
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Third event reuses id "01A" → UNIQUE violation mid-batch.
	_, err := s.AppendBatch([]event.Envelope{
		testEvent("01B", event.TypeWriterStarted, "", `{}`),
		testEvent("01C", event.TypeWriterStarted, "", `{}`),
		testEvent("01A", event.TypeWriterStarted, "", `{}`),
	}, nil)
	if err == nil {
		t.Fatal("duplicate id must fail the batch")
	}
	// A pre-commit failure rolls back for certain and must NOT be reported
	// as an ambiguous commit.
	var ambiguous *AmbiguousCommitError
	if errors.As(err, &ambiguous) {
		t.Fatalf("pre-commit failure misclassified as ambiguous: %v", err)
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("failed batch left %d events, want 1", n)
	}
	var last int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != 1 {
		t.Fatalf("max seq = %d after failed batch", last)
	}

	// The next batch gets the seq the failed one would have used.
	b, err := s.AppendBatch([]event.Envelope{
		testEvent("01D", event.TypeWriterStarted, "", `{}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].Seq != 2 {
		t.Fatalf("seq after rollback = %d, want 2", b[0].Seq)
	}
}

func TestEventRefsWrittenInSameTransaction(t *testing.T) {
	s := openTestStore(t)
	b, err := s.AppendBatch([]event.Envelope{
		testEvent("01A", event.TypeLogInitialized, "", `{"workspace_id":"ws_1"}`),
		testEvent("01B", event.TypeWriterStarted, "", `{}`),
	}, []Ref{
		{Event: 1, EntityID: "ent_x", Role: "subject"},
		{Event: 1, EntityID: "ent_y", Role: "target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT event_seq, entity_id, role FROM event_refs ORDER BY entity_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var seq int64
		var ent, role string
		if err := rows.Scan(&seq, &ent, &role); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d/%s/%s", seq, ent, role))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		fmt.Sprintf("%d/ent_x/subject", b[1].Seq),
		fmt.Sprintf("%d/ent_y/target", b[1].Seq),
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("event_refs = %v, want %v", got, want)
	}

	// A failing batch writes no refs either.
	_, err = s.AppendBatch([]event.Envelope{
		testEvent("01A", event.TypeWriterStarted, "", `{}`), // duplicate id
	}, []Ref{{Event: 0, EntityID: "ent_z", Role: "subject"}})
	if err == nil {
		t.Fatal("expected failure")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM event_refs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("failed batch left %d refs, want 2", n)
	}
}

func TestAppendBatchRejectsEmptyAndBadRefs(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.AppendBatch(nil, nil); err == nil {
		t.Fatal("empty batch must be rejected")
	}
	_, err := s.AppendBatch([]event.Envelope{
		testEvent("01A", event.TypeLogInitialized, "", `{"workspace_id":"ws_1"}`),
	}, []Ref{{Event: 5, EntityID: "e", Role: "r"}})
	if err == nil {
		t.Fatal("out-of-range ref must be rejected")
	}
}

func TestCausesAndEntityNullability(t *testing.T) {
	s := openTestStore(t)
	target := "01TARGET"
	ev := testEvent("01A", event.TypeLogInitialized, "", `{"workspace_id":"ws_1"}`)
	ev2 := testEvent("01B", event.TypeWriterStarted, "ent_1", `{}`)
	ev2.Causes = &target
	if _, err := s.AppendBatch([]event.Envelope{ev, ev2}, nil); err != nil {
		t.Fatal(err)
	}
	var got []event.Envelope
	if err := s.Scan(func(e event.Envelope) error { got = append(got, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if got[0].Causes != nil || got[0].Entity != "" {
		t.Fatalf("event 1: causes=%v entity=%q, want nil/empty", got[0].Causes, got[0].Entity)
	}
	if got[1].Causes == nil || *got[1].Causes != target || got[1].Entity != "ent_1" {
		t.Fatalf("event 2: causes/entity not round-tripped: %+v", got[1])
	}
}

func TestSecondOpenSameProcessFails(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open: got %v, want ErrLocked", err)
	}
}
