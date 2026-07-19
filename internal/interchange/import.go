package interchange

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"churn/internal/canonjson"
	"churn/internal/domain"
	"churn/internal/event"
	"churn/internal/store"
	"churn/internal/ulid"
)

// maxLine bounds one JSONL line (64 MiB) — far beyond any real envelope, but
// finite, so a corrupt stream cannot balloon memory.
const maxLine = 64 << 20

// Import restores the JSONL stream r into the data directory dir, which must
// be empty (or not exist yet) — import is the restore path, never a merge
// (§5.4). It returns the number of events and batches restored.
//
// All-or-nothing rests on two mechanisms:
//
//   - Validate fully before writing: the entire stream is parsed and
//     validated in memory — envelope hygiene line by line, then every batch
//     through the same domain.ValidateBatch as live writes — so a validation
//     failure aborts with a precise line-numbered error before a single byte
//     reaches disk.
//   - Stage, then rename: batches are written (one transaction each) into
//     workspace.db.partial, and only a fully written database is renamed to
//     workspace.db (store.FinalizeRestore; the rename target never exists,
//     so it is safe on Windows too). A process killed mid-restore therefore
//     leaves at most a .partial file that no workspace open will accept as
//     truth; the next import removes it and starts over.
//
// The restored events keep their ORIGINAL ids, timestamps, actors, batch
// ids, and origins: import re-materializes history. Resuming writing
// afterwards appends writer.started with a fresh origin — the writer's
// ordinary reopen path (§5.2).
func Import(dir string, r io.Reader) (events, batches int, err error) {
	if err := prepareDir(dir); err != nil {
		return 0, 0, err
	}
	groups, err := validateStream(r)
	if err != nil {
		return 0, 0, fmt.Errorf("import: %w", err)
	}

	st, err := store.OpenRestore(dir)
	if err != nil {
		return 0, 0, err
	}
	for _, batch := range groups {
		if err := st.AppendRestoredBatch(batch); err != nil {
			st.Close()
			removeRestoreFiles(dir)
			return 0, 0, fmt.Errorf("import: writing batch %s: %w", batch[0].Batch, err)
		}
		events += len(batch)
	}
	if err := st.Close(); err != nil {
		removeRestoreFiles(dir)
		return 0, 0, fmt.Errorf("import: %w", err)
	}
	if err := store.FinalizeRestore(dir); err != nil {
		removeRestoreFiles(dir)
		return 0, 0, fmt.Errorf("import: %w", err)
	}
	return events, len(groups), nil
}

// restoreLeftovers are the files an interrupted or failed restore may leave
// behind. They never include DBFileName: a workspace database only appears
// on success, so their presence does not make a directory a workspace.
var restoreLeftovers = []string{
	store.RestoreDBFileName,
	store.RestoreDBFileName + "-wal",
	store.RestoreDBFileName + "-shm",
	store.LockFileName,
}

// prepareDir accepts a missing dir (the store creates it) or an existing
// directory that is empty apart from leftovers of a previously interrupted
// restore, which it removes. Anything else — in particular an existing
// workspace.db — is a refusal: import restores into an empty directory only.
func prepareDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	leftover := map[string]bool{}
	for _, name := range restoreLeftovers {
		leftover[name] = true
	}
	for _, e := range entries {
		if !leftover[e.Name()] {
			return fmt.Errorf("import: data directory %s is not empty (%d entries): import restores into an empty directory only", dir, len(entries))
		}
	}
	// Only stale restore leftovers: clear them and start over. Removal must
	// succeed — resuming on top of a half-written staging database would
	// corrupt the fresh restore.
	for _, name := range restoreLeftovers {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("import: clearing stale restore file: %w", err)
		}
	}
	return nil
}

// removeRestoreFiles best-effort removes the staging files of a failed
// restore, returning the directory to its pre-import emptiness.
func removeRestoreFiles(dir string) {
	for _, name := range restoreLeftovers {
		os.Remove(filepath.Join(dir, name))
	}
}

// validateStream parses and validates the whole JSONL stream, returning the
// envelopes grouped into batches, in order. Envelope hygiene is checked line
// by line — seq contiguous from 1, canonical-form ULID ids (id and batch;
// origin is wr_ + ULID), id uniqueness, null causes, first-event and origin
// lineage rules, the writer's exact ts format, monotone non-decreasing ts,
// batch contiguity with one commit ts and one actor per batch, the writer's
// lifecycle batch shapes (writer.started alone; log.initialized shared only
// with its state.defined seeds), registered (type, v), payload shape, and
// canonical payload bytes. Every batch is then folded through
// domain.ValidateBatch, exactly as a live write would be, so a corrupt log
// can never launder itself into a plausible projection (§5.4) — the theme
// throughout: import accepts nothing a live writer could not have produced.
// Every error names the offending line.
func validateStream(r io.Reader) ([][]event.Envelope, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)

	var (
		groups   [][]event.Envelope
		cur      []event.Envelope
		curLine  int // line number of cur's first event
		proj     = domain.NewProjection()
		seenID   = map[string]struct{}{}
		seenOrig = map[string]struct{}{}
		finished = map[string]struct{}{} // batch ids that have ended
		origin   string                  // current writer lineage
		lastTS   string
		line     int
	)

	// flush validates the accumulated batch through the domain and advances
	// the projection.
	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		cand, err := domain.ValidateBatch(proj, cur, nil)
		if err != nil {
			return fmt.Errorf("lines %d-%d (batch %s): %w", curLine, curLine+len(cur)-1, cur[0].Batch, err)
		}
		proj = cand
		groups = append(groups, cur)
		cur = nil
		return nil
	}

	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, fmt.Errorf("line %d: empty line", line)
		}

		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var ev event.Envelope
		if err := dec.Decode(&ev); err != nil {
			return nil, fmt.Errorf("line %d: invalid envelope: %v", line, err)
		}
		if dec.More() {
			return nil, fmt.Errorf("line %d: trailing data after envelope", line)
		}

		// Envelope hygiene, in dependency order.
		if ev.Seq != int64(line) {
			return nil, fmt.Errorf("line %d: seq %d, want %d (seq must be contiguous from 1)", line, ev.Seq, line)
		}
		// Ids must be in canonical (uppercase) ULID form, the only form the
		// writer emits — otherwise a case alias could evade the byte-wise
		// duplicate check below.
		if !canonicalULID(ev.ID) {
			return nil, fmt.Errorf("line %d: id %q is not a canonical ULID", line, ev.ID)
		}
		if _, dup := seenID[ev.ID]; dup {
			return nil, fmt.Errorf("line %d: duplicate event id %s", line, ev.ID)
		}
		seenID[ev.ID] = struct{}{}
		if !canonicalULID(ev.Batch) {
			return nil, fmt.Errorf("line %d: batch %q is not a canonical ULID", line, ev.Batch)
		}
		if !strings.HasPrefix(ev.Origin, event.PrefixWriter) ||
			!canonicalULID(strings.TrimPrefix(ev.Origin, event.PrefixWriter)) {
			return nil, fmt.Errorf("line %d: origin %q is not %q + a canonical ULID", line, ev.Origin, event.PrefixWriter)
		}
		if ev.Actor == "" {
			return nil, fmt.Errorf("line %d: actor must not be empty", line)
		}
		if ev.Causes != nil {
			return nil, fmt.Errorf("line %d: causes is reserved and must be null in v1 (§5.2)", line)
		}
		if line == 1 && ev.Type != event.TypeLogInitialized {
			return nil, fmt.Errorf("line 1: first event is %q, want %s", ev.Type, event.TypeLogInitialized)
		}
		// The writer's exact timestamp form, then monotonicity. Anything else
		// (a garbage string, a width variant) would poison the reopened
		// writer's monotone clamp — every future event would inherit it.
		if !event.ValidTS(ev.TS) {
			return nil, fmt.Errorf("line %d: ts %q is not in the writer timestamp format %s", line, ev.TS, event.TSFormat)
		}
		if ev.TS < lastTS {
			return nil, fmt.Errorf("line %d: ts %q regresses below %q", line, ev.TS, lastTS)
		}
		lastTS = ev.TS

		// Origin lineage (§5.2): lifecycle events mint a fresh origin; every
		// other event carries the lineage the last lifecycle event minted.
		switch ev.Type {
		case event.TypeLogInitialized, event.TypeWriterStarted:
			if _, used := seenOrig[ev.Origin]; used {
				return nil, fmt.Errorf("line %d: %s must mint a fresh origin, %s was already used", line, ev.Type, ev.Origin)
			}
			seenOrig[ev.Origin] = struct{}{}
			origin = ev.Origin
		default:
			if ev.Origin != origin {
				return nil, fmt.Errorf("line %d: origin %s does not match the current writer lineage %s", line, ev.Origin, origin)
			}
		}

		// Payload: registered (type, v), valid shape, canonical bytes. The
		// canonical check keeps the "stored payloads are canonicalized"
		// invariant that lets export copy bytes instead of re-serializing.
		if _, err := event.Decode(ev.Type, ev.V, ev.Data); err != nil {
			return nil, fmt.Errorf("line %d: %v", line, err)
		}
		canon, err := canonjson.Canonicalize(ev.Data)
		if err != nil {
			return nil, fmt.Errorf("line %d: payload: %v", line, err)
		}
		if !bytes.Equal(canon, ev.Data) {
			return nil, fmt.Errorf("line %d: payload is not canonical JSON (want %s)", line, canon)
		}

		// Batch contiguity: all events of one batch are adjacent.
		if len(cur) == 0 || ev.Batch != cur[0].Batch {
			if _, done := finished[ev.Batch]; done {
				return nil, fmt.Errorf("line %d: batch %s resumes after other batches: events of one batch must be adjacent", line, ev.Batch)
			}
			if len(cur) > 0 {
				finished[cur[0].Batch] = struct{}{}
				if err := flush(); err != nil {
					return nil, err
				}
			}
			curLine = line
		} else {
			// Joining an open batch: the live writer stamps ONE commit ts and
			// ONE actor per batch, and emits lifecycle events in batches of a
			// fixed shape — writer.started alone; log.initialized followed
			// only by its state.defined vocabulary seeds.
			if ev.TS != cur[0].TS {
				return nil, fmt.Errorf("line %d: ts %q differs from its batch's commit ts %q (one batch, one commit timestamp)", line, ev.TS, cur[0].TS)
			}
			if ev.Actor != cur[0].Actor {
				return nil, fmt.Errorf("line %d: actor %q differs from its batch's actor %q (one batch, one actor)", line, ev.Actor, cur[0].Actor)
			}
			switch {
			case ev.Type == event.TypeLogInitialized || ev.Type == event.TypeWriterStarted:
				return nil, fmt.Errorf("line %d: %s must open its own batch", line, ev.Type)
			case cur[0].Type == event.TypeWriterStarted:
				return nil, fmt.Errorf("line %d: writer.started must be the only event of its batch", line)
			case cur[0].Type == event.TypeLogInitialized && ev.Type != event.TypeStateDefined:
				return nil, fmt.Errorf("line %d: %s cannot share the log.initialized batch (only the state.defined seeds may)", line, ev.Type)
			}
		}
		cur = append(cur, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("line %d: reading stream: %w", line+1, err)
	}
	if line == 0 {
		return nil, fmt.Errorf("the log is empty: a log's first event is %s", event.TypeLogInitialized)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return groups, nil
}

// canonicalULID reports whether s is a ULID in its one canonical spelling —
// it parses AND re-renders to identical bytes, so Crockford case aliases are
// rejected and byte-wise comparisons are sufficient afterwards.
func canonicalULID(s string) bool {
	u, err := ulid.Parse(s)
	return err == nil && u.String() == s
}
