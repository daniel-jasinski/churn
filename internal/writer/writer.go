// Package writer implements the single writer goroutine (DESIGN.md §5): the
// one serialized path through which events enter the log. Each batch is
// validated by applying it to a clone of the current projection, committed
// via store.AppendBatch in one transaction, and then published by an atomic
// pointer swap — the live projection can never fall behind durable truth.
// If anything goes wrong between commit and publish, the process terminates
// (Options.Fatal) and recovers by replay rather than continue on stale
// state.
//
// Time and entropy are injected (Options.Now, Options.Entropy), never read
// globally, so tests are deterministic. Event timestamps are writer-assigned
// and monotone with seq: they never decrease, even when the wall clock steps
// backwards. All events of one batch share the batch commit timestamp.
//
// Startup semantics (M1): opening a fresh data directory appends
// log.initialized (minting the immutable workspace id and the first writer
// lineage); opening an existing log appends writer.started with a fresh
// lineage. DESIGN.md only requires the latter when a restored or copied
// directory resumes writing; detecting "same lineage context" is deferred,
// so in M1 every startup of an existing log appends writer.started. That is
// spec-acceptable — extra lineages are harmless facts — and documented here
// deliberately.
package writer

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"churn/internal/canonjson"
	"churn/internal/domain"
	"churn/internal/event"
	"churn/internal/store"
	"churn/internal/ulid"
)

// tsFormat is the fixed-width UTC timestamp layout, shared with import
// validation (event.TSFormat): lexicographic order is chronological order,
// which the schema's ev_ts index and the fold's monotonicity check rely on.
const tsFormat = event.TSFormat

// Options configures a Writer. Zero values get production defaults.
type Options struct {
	// Actor is stamped on writer-internal lifecycle events
	// (log.initialized, writer.started). Default "system".
	Actor string
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// Entropy feeds ULID generation. Default crypto/rand.Reader.
	Entropy io.Reader
	// Fatal is invoked when the projection cannot be published after a
	// successful commit — the process must not continue on stale state.
	// Default log.Fatal. Tests inject a recorder; production code must
	// never let it return to normal control flow.
	Fatal func(error)
}

// Command is one event to be appended. The writer assigns id, ts, origin,
// batch, and seq; validation happens against the current projection before
// commit. Ids for NEW entities are minted by the caller (see MintID) and
// travel inside Entity and the payload — the domain never generates ids.
// The event_refs rows are derived from the payload (event.Referencer), never
// supplied by the caller.
type Command struct {
	// Type and V select the registered payload schema.
	Type string
	V    int
	// Entity is the primary entity the event is about; may be empty.
	Entity string
	// Causes targets a prior event id. RESERVED (§5.2): it must be nil in
	// V1 — Submit rejects a non-nil value, and import rejects a non-null
	// causes column, so no V1 log carries one. The field stays on Command so
	// the V2 seam exists without an envelope change.
	Causes *string
	// Payload is encoded with canonjson; use the payload struct registered
	// for (Type, V).
	Payload any
}

// Writer is the single writer. Create with Open; it must be Closed. The
// store's lifetime is the caller's responsibility.
type Writer struct {
	st    *store.Store
	gen   *ulid.Generator
	now   func() time.Time
	fatal func(error)
	// appendBatch is st.AppendBatch; a test seam for fault injection on the
	// commit path.
	appendBatch func([]event.Envelope, []store.Ref) ([]event.Envelope, error)

	proj atomic.Pointer[domain.Projection]
	hook atomic.Pointer[func(CommitInfo)]

	// origin and lastTS belong to the writer goroutine (and to Open before
	// the goroutine starts); they are never touched concurrently.
	origin string
	lastTS string

	cmds chan request
	quit chan struct{}
	wg   sync.WaitGroup
}

type request struct {
	actor    string
	cmds     []Command
	expected map[string]int64
	reply    chan response
}

type response struct {
	events []event.Envelope
	err    error
}

// Open replays the log in st into a fresh projection, appends the startup
// lifecycle event (log.initialized for an empty log, writer.started
// otherwise), and starts the writer goroutine.
func Open(st *store.Store, opts Options) (*Writer, error) {
	if opts.Actor == "" {
		opts.Actor = "system"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Entropy == nil {
		opts.Entropy = rand.Reader
	}
	if opts.Fatal == nil {
		opts.Fatal = func(err error) { log.Fatal(err) }
	}

	w := &Writer{
		st:          st,
		gen:         ulid.NewGenerator(opts.Now, opts.Entropy),
		now:         opts.Now,
		fatal:       opts.Fatal,
		appendBatch: st.AppendBatch,
		cmds:        make(chan request),
		quit:        make(chan struct{}),
	}

	p := domain.NewProjection()
	if err := st.Scan(p.Apply); err != nil {
		return nil, fmt.Errorf("writer: replaying log: %w", err)
	}
	w.proj.Store(p)
	w.lastTS = p.LastTS

	if p.LastSeq == 0 {
		// Fresh directory: mint the workspace and the first lineage, and
		// append log.initialized together with the §2.2 default states as
		// ONE batch — a crash can therefore never leave a workspace that
		// exists but lacks its default vocabulary. (The internal append path
		// is not subject to Submit's log.*/writer.* ban.)
		wsID, err := w.MintID(event.PrefixWorkspace)
		if err != nil {
			return nil, err
		}
		if w.origin, err = w.MintID(event.PrefixWriter); err != nil {
			return nil, err
		}
		cmds := []Command{{
			Type:    event.TypeLogInitialized,
			V:       1,
			Payload: event.LogInitialized{WorkspaceID: wsID},
		}}
		seeds, err := w.defaultStateCommands()
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, seeds...)
		if _, err := w.append(opts.Actor, cmds, nil); err != nil {
			return nil, fmt.Errorf("writer: initializing log: %w", err)
		}
	} else {
		// Existing log: resume under a fresh lineage (see package comment).
		var err error
		if w.origin, err = w.MintID(event.PrefixWriter); err != nil {
			return nil, err
		}
		_, err = w.append(opts.Actor, []Command{{
			Type:    event.TypeWriterStarted,
			V:       1,
			Payload: event.WriterStarted{},
		}}, nil)
		if err != nil {
			return nil, fmt.Errorf("writer: starting lineage: %w", err)
		}
	}

	w.wg.Add(1)
	go w.loop()
	return w, nil
}

// Projection returns the currently published projection. The returned value
// is immutable; a later publish swaps in a new one.
func (w *Writer) Projection() *domain.Projection {
	return w.proj.Load()
}

// Origin returns the writer's current lineage id.
func (w *Writer) Origin() string {
	// Written only before the goroutine starts; safe to read after Open.
	return w.origin
}

// Submit sends one batch of commands through the writer: validated against
// the current projection, appended atomically, projection published. It
// returns the committed envelopes (with seq assigned) or the validation
// error that rejected the batch (a *domain.Error for invariant violations,
// reachable through errors.As).
//
// expected is the batch's optional expected_versions precondition (§5.2):
// entity id → the version (seq of the last touching event) the client last
// saw, for entities the batch writes. A mismatch against the pre-batch
// projection rejects the batch with a KindStaleVersion conflict. It is a
// command, not a fact — never persisted to the log. Pass nil to skip.
//
// The log and writer namespaces (log.*, writer.*) are lifecycle events the
// writer appends itself; submitting them is rejected.
func (w *Writer) Submit(actor string, cmds []Command, expected map[string]int64) ([]event.Envelope, error) {
	req := request{actor: actor, cmds: cmds, expected: expected, reply: make(chan response, 1)}
	select {
	case w.cmds <- req:
	case <-w.quit:
		return nil, fmt.Errorf("writer: closed")
	}
	resp := <-req.reply
	return resp.events, resp.err
}

// CommitInfo describes one committed batch, for the commit hook: the batch
// id, the seq of its last event, the batch commit timestamp, and the sorted,
// de-duplicated event types it contained.
type CommitInfo struct {
	Batch string
	Seq   int64
	TS    string
	Types []string
}

// SetCommitHook installs fn to be called synchronously after each batch
// committed from then on is published. fn runs on the writer goroutine and
// must not block; a nil fn removes the hook. This is the phase-3 SSE seam
// (DESIGN.md §6) — the one point where "the world changed" is known.
func (w *Writer) SetCommitHook(fn func(CommitInfo)) {
	if fn == nil {
		w.hook.Store(nil)
		return
	}
	w.hook.Store(&fn)
}

// Close stops the writer goroutine. It does not close the store.
func (w *Writer) Close() {
	close(w.quit)
	w.wg.Wait()
}

func (w *Writer) loop() {
	defer w.wg.Done()
	for {
		select {
		case req := <-w.cmds:
			evs, err := w.handle(req.actor, req.cmds, req.expected)
			req.reply <- response{events: evs, err: err}
		case <-w.quit:
			return
		}
	}
}

func (w *Writer) handle(actor string, cmds []Command, expected map[string]int64) ([]event.Envelope, error) {
	if actor == "" {
		return nil, fmt.Errorf("writer: actor must not be empty")
	}
	for _, c := range cmds {
		if strings.HasPrefix(c.Type, "log.") || strings.HasPrefix(c.Type, "writer.") {
			return nil, fmt.Errorf("writer: %s is writer-internal and cannot be submitted", c.Type)
		}
		if c.Causes != nil {
			return nil, fmt.Errorf("writer: causes is reserved and must be nil in v1 (§5.2)")
		}
	}
	return w.append(actor, cmds, expected)
}

// append is the serialized validate → commit → publish step. Called from
// Open (before the goroutine exists) and from the goroutine — never
// concurrently.
func (w *Writer) append(actor string, cmds []Command, expected map[string]int64) ([]event.Envelope, error) {
	if len(cmds) == 0 {
		return nil, fmt.Errorf("writer: empty batch")
	}

	cur := w.proj.Load()

	batchULID, err := w.gen.New()
	if err != nil {
		return nil, fmt.Errorf("writer: minting batch id: %w", err)
	}
	ts := w.nextTS()

	evs := make([]event.Envelope, len(cmds))
	var refs []store.Ref
	for i, c := range cmds {
		data, err := canonjson.Encode(c.Payload)
		if err != nil {
			return nil, fmt.Errorf("writer: encoding %s payload: %w", c.Type, err)
		}
		id, err := w.gen.New()
		if err != nil {
			return nil, fmt.Errorf("writer: minting event id: %w", err)
		}
		evs[i] = event.Envelope{
			Seq:    cur.LastSeq + 1 + int64(i),
			ID:     id.String(),
			Origin: w.origin,
			Batch:  batchULID.String(),
			Causes: c.Causes,
			TS:     ts,
			Actor:  actor,
			Type:   c.Type,
			V:      c.V,
			Entity: c.Entity,
			Data:   data,
		}
		// Derive the event_refs rows from the canonical payload — refs are an
		// index over events, so they must be computable from events alone
		// (event.RefsOf is the same derivation restore and reindex use).
		evRefs, err := event.RefsOf(c.Type, c.V, data)
		if err != nil {
			return nil, fmt.Errorf("writer: %w", err)
		}
		for _, ref := range evRefs {
			refs = append(refs, store.Ref{Event: i, EntityID: ref.Entity, Role: ref.Role})
		}
	}

	// Validate the batch — every §5.2 invariant plus expected_versions —
	// producing the candidate projection through the same fold replay uses,
	// so nothing can commit that would not fold back.
	cand, err := domain.ValidateBatch(cur, evs, expected)
	if err != nil {
		return nil, err
	}

	committed, err := w.appendBatch(evs, refs)
	if err != nil {
		var ambiguous *store.AmbiguousCommitError
		if errors.As(err, &ambiguous) {
			// The batch may be durable even though COMMIT reported failure.
			// Treating it as "nothing written" could leave the projection
			// behind durable truth, and a retry could duplicate the batch —
			// terminate and recover by replay instead (§5).
			err := fmt.Errorf("writer: %w; terminating to recover by replay", err)
			w.fatal(err)
			// Reached only under an injected test Fatal.
			return nil, err
		}
		// Everything else rolled back for certain: safe to report as a
		// clean rejection with the projection untouched.
		return nil, fmt.Errorf("writer: append: %w", err)
	}

	// Committed. Any inconsistency past this point means the durable log and
	// the candidate projection disagree — publishing either would lie, so
	// the process must terminate and recover by replay.
	for i := range committed {
		if committed[i].Seq != evs[i].Seq {
			err := fmt.Errorf(
				"writer: post-commit seq mismatch (store %d, projection %d): log written outside the writer; terminating",
				committed[i].Seq, evs[i].Seq)
			w.fatal(err)
			// Reached only under an injected test Fatal.
			return nil, err
		}
	}
	w.lastTS = ts
	w.proj.Store(cand)
	if h := w.hook.Load(); h != nil {
		types := make([]string, 0, len(committed))
		seen := map[string]struct{}{}
		for _, ev := range committed {
			if _, dup := seen[ev.Type]; !dup {
				seen[ev.Type] = struct{}{}
				types = append(types, ev.Type)
			}
		}
		sort.Strings(types)
		(*h)(CommitInfo{
			Batch: committed[0].Batch,
			Seq:   committed[len(committed)-1].Seq,
			TS:    ts,
			Types: types,
		})
	}
	return committed, nil
}

// nextTS returns the batch timestamp: wall clock, clamped to never decrease
// below the last assigned (or replayed) timestamp.
func (w *Writer) nextTS() string {
	ts := w.now().UTC().Format(tsFormat)
	if ts < w.lastTS {
		ts = w.lastTS
	}
	return ts
}

// MintID returns prefix + a fresh ULID, e.g. "th_01J3ZK…" — the id-minting
// service for callers composing commands that create entities (the domain
// never generates ids). Safe for concurrent use.
func (w *Writer) MintID(prefix string) (string, error) {
	u, err := w.gen.New()
	if err != nil {
		return "", fmt.Errorf("writer: minting id: %w", err)
	}
	return prefix + u.String(), nil
}

// defaultStateCommands mints the §2.2 predefined states of a fresh
// workspace as ordinary state.defined commands — data, not code. They are
// appended in the SAME batch as log.initialized; nothing references their
// ids specially afterwards, and users may supersede or (once unused) retract
// them like any other vocabulary.
func (w *Writer) defaultStateCommands() ([]Command, error) {
	defaults := []event.StateDefined{
		{Name: "todo", Semantic: event.SemanticPending, Color: "#9ca3af", Description: "Not started"},
		{Name: "in_progress", Semantic: event.SemanticActive, Color: "#3b82f6", Description: "Being worked"},
		{Name: "done", Semantic: event.SemanticSatisfied, Color: "#22c55e", Description: "Finished successfully"},
		{Name: "on_hold", Semantic: event.SemanticPaused, Color: "#f59e0b", Description: "Deliberately not being worked"},
		{Name: "cancelled", Semantic: event.SemanticAbandoned, Color: "#ef4444", Description: "Will not be done"},
	}
	cmds := make([]Command, len(defaults))
	for i, d := range defaults {
		id, err := w.MintID(event.PrefixState)
		if err != nil {
			return nil, err
		}
		cmds[i] = Command{Type: event.TypeStateDefined, V: 1, Entity: id, Payload: d}
	}
	return cmds, nil
}
