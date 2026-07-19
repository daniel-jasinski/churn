// Package store is the SQLite event log substrate (DESIGN.md §5.2): one
// INSERT-only events table in WAL mode, append-only enforced by triggers,
// batches appended in single transactions, plus the derived event_refs side
// table (with reindex), online backup, restore-path appends, and the
// data-directory process lock.
//
// The store is deliberately dumb: it persists and streams envelopes. All
// meaning — validation, invariants, projection — lives in the domain fold
// and the writer.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"churn/internal/event"

	_ "modernc.org/sqlite"
)

// DBFileName is the SQLite file inside the data directory.
const DBFileName = "workspace.db"

// RestoreDBFileName is the staging file a restore (churn import-log) writes
// into; FinalizeRestore renames it to DBFileName only once the restore is
// complete, so a killed import can never leave a directory that opens as a
// legitimate workspace.
const RestoreDBFileName = DBFileName + ".partial"

// LockFileName is the exclusivity lock file inside the data directory.
const LockFileName = "churn.lock"

const schema = `
CREATE TABLE IF NOT EXISTS events (
  seq    INTEGER PRIMARY KEY,   -- position in the log
  id     TEXT UNIQUE NOT NULL,  -- ULID
  origin TEXT NOT NULL,         -- writer lineage (see log.initialized)
  batch  TEXT NOT NULL,
  causes TEXT,                  -- id of a targeted prior event; usually NULL
  ts     TEXT NOT NULL,         -- writer-assigned, monotone with seq
  actor  TEXT NOT NULL,
  type   TEXT NOT NULL,
  v      INTEGER NOT NULL,      -- payload schema version
  entity TEXT,
  data   TEXT NOT NULL          -- canonicalized JSON payload
);
CREATE INDEX IF NOT EXISTS ev_entity ON events(entity);
CREATE INDEX IF NOT EXISTS ev_type   ON events(type);
CREATE INDEX IF NOT EXISTS ev_batch  ON events(batch);
CREATE INDEX IF NOT EXISTS ev_actor  ON events(actor);
CREATE INDEX IF NOT EXISTS ev_ts     ON events(ts);

-- Append-only is enforced, not promised:
CREATE TRIGGER IF NOT EXISTS events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events is append-only: UPDATE forbidden'); END;
CREATE TRIGGER IF NOT EXISTS events_no_delete BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'events is append-only: DELETE forbidden'); END;

-- Derived, rebuildable (churn reindex); populated in the same transaction:
CREATE TABLE IF NOT EXISTS event_refs (event_seq INTEGER, entity_id TEXT, role TEXT);
CREATE INDEX IF NOT EXISTS er_entity ON event_refs(entity_id);
`

// readPoolSize bounds the read-side connection pool: enough for a streaming
// export plus a couple of API readers, small enough to keep the WAL from
// being pinned by a crowd of stale snapshots.
const readPoolSize = 4

// Store is an open event log. A writable Store (Open) holds the
// data-directory lock for its lifetime; Close releases it. A read-only Store
// (OpenReadOnly) holds no lock and rejects every write method.
//
// Connections are split into two pools so readers and the writer can never
// starve each other (WAL: one writer, N readers):
//
//   - db is the write side, pinned to ONE connection — the writer goroutine
//     is the only writer, and a single connection sidesteps intra-process
//     SQLITE_BUSY on the write path entirely;
//   - rdb is the read side (query_only), used by Scan — a long streaming
//     scan holds a read connection and its WAL snapshot, while appends
//     commit concurrently on the write connection. A scan callback may even
//     block on the writer without deadlocking, because the writer never
//     needs a read-pool connection.
type Store struct {
	db   *sql.DB // write pool (1 connection); nil on a read-only store
	rdb  *sql.DB // read pool (query_only)
	dsn  string  // plain read-write DSN; Backup opens its own connection from it
	lock *dirLock
}

// baseDSN renders the read-write DSN for the database file at path.
// recursive_triggers(1) is load-bearing: without it, INSERT OR REPLACE's
// implicit DELETE bypasses the append-only DELETE trigger and history could
// be silently rewritten. TestOpenCreatesSchemaAndWAL pins all pragmas as
// actually in effect, so a typo here cannot silently no-op.
func baseDSN(path string) string {
	return "file:" + uriPath(path) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)" +
		"&_pragma=recursive_triggers(1)"
}

// openReadPool opens the query_only read-side pool for dsn.
func openReadPool(dsn string) (*sql.DB, error) {
	rdb, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("store: opening read pool: %w", err)
	}
	rdb.SetMaxOpenConns(readPoolSize)
	return rdb, nil
}

// Open locks the data directory (creating it if needed) and opens the event
// log inside it, creating the schema on first use. It returns ErrLocked if
// another process holds the directory.
func Open(dir string) (*Store, error) {
	return open(dir, DBFileName)
}

// OpenRestore locks the data directory and opens a restore-target log at
// RestoreDBFileName instead of DBFileName. A restore writes its batches
// here; FinalizeRestore publishes the result by rename, so an interrupted
// restore leaves only a .partial file that no workspace open will ever
// mistake for truth.
func OpenRestore(dir string) (*Store, error) {
	return open(dir, RestoreDBFileName)
}

// open is Open/OpenRestore over an explicit database filename.
func open(dir, dbName string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: creating data directory: %w", err)
	}
	lock, err := acquireDirLock(filepath.Join(dir, LockFileName))
	if err != nil {
		return nil, err
	}

	dsn := baseDSN(filepath.Join(dir, dbName))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = lock.release()
		return nil, fmt.Errorf("store: opening database: %w", err)
	}
	// See the Store comment: the write side is exactly one connection.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		_ = lock.release()
		return nil, fmt.Errorf("store: creating schema: %w", err)
	}
	rdb, err := openReadPool(dsn)
	if err != nil {
		_ = db.Close()
		_ = lock.release()
		return nil, err
	}
	return &Store{db: db, rdb: rdb, dsn: dsn, lock: lock}, nil
}

// OpenReadOnly opens an EXISTING event log for reading without acquiring the
// data-directory lock, so export-log and backup can run against a workspace
// held open by a live server (WAL allows concurrent readers; the lock guards
// the single-projection assumption, which readers cannot violate). Every
// write method fails on the returned store.
func OpenReadOnly(dir string) (*Store, error) {
	path := filepath.Join(dir, DBFileName)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("store: no workspace database at %s: %w", path, err)
	}
	dsn := baseDSN(path)
	rdb, err := openReadPool(dsn)
	if err != nil {
		return nil, err
	}
	// Probe now: a clear "not a churn workspace" beats a late scan error.
	var n int
	if err := rdb.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='events'`).Scan(&n); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("store: opening %s: %w", path, err)
	}
	if n != 1 {
		_ = rdb.Close()
		return nil, fmt.Errorf("store: %s is not a churn workspace (no events table)", path)
	}
	return &Store{rdb: rdb, dsn: dsn}, nil
}

// errReadOnly rejects writes on a store opened with OpenReadOnly.
var errReadOnly = errors.New("store: opened read-only")

// Close closes the database pools and releases the directory lock (if held).
func (s *Store) Close() error {
	var first error
	if s.db != nil {
		first = s.db.Close()
	}
	if err := s.rdb.Close(); first == nil {
		first = err
	}
	if s.lock != nil {
		if err := s.lock.release(); first == nil {
			first = err
		}
	}
	return first
}

// AmbiguousCommitError reports that the COMMIT step itself failed: the batch
// may or may not be durably on disk. Every other AppendBatch error occurs
// before COMMIT is issued and guarantees a rollback. A caller holding
// derived state (the writer's projection) must not treat this as "nothing
// happened" — a retry could duplicate the batch — and must instead terminate
// and recover by replay (DESIGN.md §5).
type AmbiguousCommitError struct {
	Err error
}

func (e *AmbiguousCommitError) Error() string {
	return fmt.Sprintf("store: commit outcome ambiguous: %v", e.Err)
}

// Unwrap exposes the underlying commit error.
func (e *AmbiguousCommitError) Unwrap() error { return e.Err }

// Ref is one event_refs row to be written alongside a batch. Event indexes
// into the batch slice; the store resolves it to the assigned seq.
type Ref struct {
	Event    int
	EntityID string
	Role     string
}

// AppendBatch inserts all events of one batch — and its event_refs rows — in
// a single transaction, assigning consecutive seq values. It returns the
// events with Seq filled in. On any failure nothing is written.
func (s *Store) AppendBatch(events []event.Envelope, refs []Ref) ([]event.Envelope, error) {
	if s.db == nil {
		return nil, errReadOnly
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("store: empty batch")
	}
	for _, r := range refs {
		if r.Event < 0 || r.Event >= len(events) {
			return nil, fmt.Errorf("store: ref event index %d out of range", r.Event)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful commit

	var last int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&last); err != nil {
		return nil, fmt.Errorf("store: reading last seq: %w", err)
	}

	out := make([]event.Envelope, len(events))
	copy(out, events)
	for i := range out {
		out[i].Seq = last + 1 + int64(i)
		_, err := tx.Exec(
			`INSERT INTO events (seq, id, origin, batch, causes, ts, actor, type, v, entity, data)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			out[i].Seq, out[i].ID, out[i].Origin, out[i].Batch, out[i].Causes,
			out[i].TS, out[i].Actor, out[i].Type, out[i].V,
			nullIfEmpty(out[i].Entity), string(out[i].Data),
		)
		if err != nil {
			return nil, fmt.Errorf("store: inserting event %s: %w", out[i].ID, err)
		}
	}
	for _, r := range refs {
		if _, err := tx.Exec(
			`INSERT INTO event_refs (event_seq, entity_id, role) VALUES (?, ?, ?)`,
			out[r.Event].Seq, r.EntityID, r.Role,
		); err != nil {
			return nil, fmt.Errorf("store: inserting event_ref: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		// Unlike every error above — which happens before COMMIT is issued
		// and therefore rolls back for certain — a failing COMMIT is
		// ambiguous: the transaction may or may not have become durable.
		return nil, &AmbiguousCommitError{Err: err}
	}
	return out, nil
}

// Scan streams every event in seq order to fn, for replay and export. A
// non-nil error from fn aborts the scan and is returned.
//
// Scan runs on the read pool: under WAL it holds a read transaction for the
// duration of the scan and therefore sees a consistent snapshot of the log
// taken when the scan starts reading — complete batches only, since batches
// are transactions. It never blocks the writer, and appends committed after
// the scan started are not visible to it. fn may safely block on (or call
// into) the writer: the write side uses its own dedicated connection.
func (s *Store) Scan(fn func(event.Envelope) error) error {
	rows, err := s.rdb.Query(
		`SELECT seq, id, origin, batch, causes, ts, actor, type, v, entity, data
		 FROM events ORDER BY seq`)
	if err != nil {
		return fmt.Errorf("store: scan: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var ev event.Envelope
		var causes, entity sql.NullString
		var data string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.Origin, &ev.Batch, &causes,
			&ev.TS, &ev.Actor, &ev.Type, &ev.V, &entity, &data); err != nil {
			return fmt.Errorf("store: scan row: %w", err)
		}
		if causes.Valid {
			ev.Causes = &causes.String
		}
		ev.Entity = entity.String
		ev.Data = []byte(data)
		if err := fn(ev); err != nil {
			return err
		}
	}
	return rows.Err()
}

// AppendRestoredBatch inserts one batch of restored envelopes VERBATIM — seq,
// id, origin, batch, causes, ts, and actor all preserved, because a restore
// re-materializes history rather than re-authoring it — in a single
// transaction, deriving the event_refs rows with event.RefsOf, the same
// derivation live appends use. The envelopes' seq values must continue the
// log contiguously (import validation guarantees it; this re-checks inside
// the transaction as defense). The append-only triggers stay armed: restore
// writes are ordinary INSERTs, indistinguishable from live ones.
func (s *Store) AppendRestoredBatch(events []event.Envelope) error {
	if s.db == nil {
		return errReadOnly
	}
	if len(events) == 0 {
		return fmt.Errorf("store: empty batch")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful commit

	var last int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&last); err != nil {
		return fmt.Errorf("store: reading last seq: %w", err)
	}
	for i, ev := range events {
		if want := last + 1 + int64(i); ev.Seq != want {
			return fmt.Errorf("store: restored event %s has seq %d, want %d", ev.ID, ev.Seq, want)
		}
		// A foreign-width ts would poison the writer's monotone clamp on the
		// next reopen; import validates this too, but the restore path is the
		// last line of defense before the bytes become durable.
		if !event.ValidTS(ev.TS) {
			return fmt.Errorf("store: restored event %s: ts %q is not in the writer timestamp format %s",
				ev.ID, ev.TS, event.TSFormat)
		}
		if _, err := tx.Exec(
			`INSERT INTO events (seq, id, origin, batch, causes, ts, actor, type, v, entity, data)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.Seq, ev.ID, ev.Origin, ev.Batch, ev.Causes,
			ev.TS, ev.Actor, ev.Type, ev.V,
			nullIfEmpty(ev.Entity), string(ev.Data),
		); err != nil {
			return fmt.Errorf("store: inserting restored event %s: %w", ev.ID, err)
		}
		refs, err := event.RefsOf(ev.Type, ev.V, ev.Data)
		if err != nil {
			return fmt.Errorf("store: restored event %s: %w", ev.ID, err)
		}
		for _, r := range refs {
			if _, err := tx.Exec(
				`INSERT INTO event_refs (event_seq, entity_id, role) VALUES (?, ?, ?)`,
				ev.Seq, r.Entity, r.Role,
			); err != nil {
				return fmt.Errorf("store: inserting event_ref: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		// Same ambiguity as AppendBatch's commit; restore callers abort and
		// clean up the whole destination rather than reason about it.
		return &AmbiguousCommitError{Err: err}
	}
	return nil
}

// Reindex rebuilds the derived event_refs table from the events table inside
// one transaction, deriving rows with event.RefsOf — the same derivation as
// live appends and restore. It returns the number of rows written. Process
// exclusivity is the caller's concern: reindex must run on a store opened
// with Open, whose lock keeps a live server out.
func (s *Store) Reindex() (int, error) {
	if s.db == nil {
		return 0, errReadOnly
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful commit

	// Collect first, then rewrite: the write pool is one connection, so the
	// INSERTs must not interleave with an open cursor.
	type row struct {
		seq  int64
		typ  string
		v    int
		data []byte
	}
	var evs []row
	rows, err := tx.Query(`SELECT seq, type, v, data FROM events ORDER BY seq`)
	if err != nil {
		return 0, fmt.Errorf("store: reindex scan: %w", err)
	}
	for rows.Next() {
		var r row
		var data string
		if err := rows.Scan(&r.seq, &r.typ, &r.v, &data); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: reindex scan row: %w", err)
		}
		r.data = []byte(data)
		evs = append(evs, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("store: reindex scan: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("store: reindex scan close: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM event_refs`); err != nil {
		return 0, fmt.Errorf("store: clearing event_refs: %w", err)
	}
	n := 0
	for _, r := range evs {
		refs, err := event.RefsOf(r.typ, r.v, r.data)
		if err != nil {
			return 0, fmt.Errorf("store: reindex: event seq %d: %w", r.seq, err)
		}
		for _, ref := range refs {
			if _, err := tx.Exec(
				`INSERT INTO event_refs (event_seq, entity_id, role) VALUES (?, ?, ?)`,
				r.seq, ref.Entity, ref.Role,
			); err != nil {
				return 0, fmt.Errorf("store: reindex insert: %w", err)
			}
		}
		n += len(refs)
	}
	// No AmbiguousCommitError treatment: event_refs is derived and reindex is
	// idempotent — on any doubt, run it again.
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: reindex commit: %w", err)
	}
	return n, nil
}

// Backup writes a transactionally consistent snapshot of the workspace
// database to dest, which must not exist yet, using VACUUM INTO.
//
// VACUUM INTO is chosen over the driver-level online-backup API because it
// is plain SQL — no modernc-specific connection surgery — and equally
// correct here: the statement runs inside one read transaction, so under WAL
// it snapshots the log while a live server keeps appending, and because
// batches are transactions the snapshot is always a complete-batch prefix.
// The destination path travels as a bound parameter, sidestepping filename
// quoting entirely. The copy arrives compact, with schema, indexes, and the
// append-only triggers intact.
//
// Backup opens its own dedicated connection (without query_only — VACUUM
// INTO must write the destination), so it can never queue behind, or starve,
// the writer's single connection. Works on both writable and read-only
// stores.
func (s *Store) Backup(dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("store: backup destination %s already exists", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: backup destination %s: %w", dest, err)
	}
	db, err := sql.Open("sqlite", s.dsn)
	if err != nil {
		return fmt.Errorf("store: backup: opening connection: %w", err)
	}
	defer func() { _ = db.Close() }() // dedicated backup connection; read-only use
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`VACUUM INTO ?`, dest); err != nil {
		// A failed VACUUM INTO may leave a partial destination file behind —
		// but only clean up files WE started: if another process created dest
		// between the pre-check and the statement, SQLite reports the
		// existing file and removing it would destroy someone else's data.
		if !strings.Contains(err.Error(), "already exists") {
			_ = os.Remove(dest)
		}
		return fmt.Errorf("store: backup: %w", err)
	}
	return nil
}

// FinalizeRestore atomically publishes a completed restore: it fsyncs the
// RestoreDBFileName staging database, renames it to DBFileName — the rename
// target never exists, because import only runs into an empty directory —
// and best-effort syncs the directory so the rename itself is durable. Call
// it only after the OpenRestore store has been Closed.
func FinalizeRestore(dir string) error {
	partial := filepath.Join(dir, RestoreDBFileName)
	f, err := os.OpenFile(partial, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("store: finalize restore: %w", err)
	}
	syncErr := f.Sync()
	if err := f.Close(); syncErr == nil {
		syncErr = err
	}
	if syncErr != nil {
		return fmt.Errorf("store: finalize restore: syncing %s: %w", partial, syncErr)
	}
	if err := os.Rename(partial, filepath.Join(dir, DBFileName)); err != nil {
		return fmt.Errorf("store: finalize restore: %w", err)
	}
	// Best-effort directory sync; Windows commonly refuses directory-handle
	// flushes, and the rename is already visible either way.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// uriPath renders a filesystem path for use inside a file: URI. SQLite's
// URI parser percent-decodes the path and treats '?' and '#' as query and
// fragment delimiters, so a raw concatenation would truncate at '#' (opening
// a database OUTSIDE the data directory) and choke on '%'. Escape exactly
// those delimiters and the escape character itself.
func uriPath(p string) string {
	return strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(p))
}
