# Churn — Implementation Plan

Companion to [DESIGN.md](DESIGN.md). DESIGN.md is the specification and wins on any
conflict; this file fixes the build order, module layout, and quality gates.

## Ground rules

- **Go backend, one module, minimal deps.** Only third-party dependency:
  `modernc.org/sqlite` (pure Go). ULID generation and canonical JSON are
  implemented in-house (small, determinism-critical, must be fully under test).
- **The domain core is pure.** `internal/domain` (and everything it imports)
  performs no I/O, reads no clock, generates no ids. Time and ULIDs are
  injected into the writer only. `fold(events) → *Projection` and
  `Validate(proj, batch) → error` are pure functions.
- **Fail closed.** Unknown event type or unsupported payload version aborts
  fold/import/startup. Unknown payload *fields* are tolerated.
- **Determinism is tested, not assumed.** Every ordered output (analytics
  lists, proposals, canonical JSON, tie-breaks) sorts on an explicit key
  (entity id unless the spec says otherwise). `go test -race` in every gate.
- Comments/naming follow standard Go style; exported API of every package
  documented; no stutter, no speculative abstraction.

## Module layout

```
cmd/churn/            CLI: serve | export-log | import-log | backup | reindex
internal/canonjson/   canonical JSON encoding (sorted keys, minimal ws, stable numbers)
internal/ulid/        ULID: 48-bit ms timestamp + 80-bit random, Crockford base32;
                      monotonic-within-ms generator interface (injectable, seedable)
internal/event/       envelope, event catalog (typed payloads), registry:
                      (type, v) → decode + payload-validate; encode via canonjson
internal/domain/      projection model, fold, batch validation, all §5.2 invariants,
                      expanded-leaf-graph acyclicity, promotion/demotion,
                      derived status, composite rollup
internal/match/       bipartite matching (max-cardinality, deterministic tie-break);
                      powers readiness, resumable-now, contention, proposer
internal/analytics/   ready list, blocked-by frontier, criticality, contention,
                      starvation, recommendation score, progress rollup
internal/store/       SQLite: schema, append-only triggers, event_refs, tx append,
                      scan/replay, online backup, reindex; data-dir lock (churn.lock,
                      Windows share-mode / flock)
internal/writer/      the single writer goroutine: command channel,
                      validate → tx commit → atomic publish; expected_versions;
                      propose/confirm re-validation in the critical section
internal/server/      HTTP API /api/v1 (§5.1), embed.FS static frontend
web/                  vanilla TS frontend, esbuild bundling, vendored
                      cytoscape.js + dagre (committed to repo, no CDN at any point)
```

## Milestones (each gated: tests green under -race, then adversarial review, then fixes)

**M1 — Skeleton & log substrate.**
go.mod, canonjson, ulid, envelope struct, store (schema §5.2, append-only
triggers, WAL, batch-transaction append, replay scan), data-dir lock,
`log.initialized` / `writer.started`, writer goroutine with atomic projection
publish, minimal fold handling only log/writer events.
*Gate tests:* canonjson byte-stability (encode→decode→re-encode identical,
fuzz), ULID ordering/uniqueness, trigger enforcement (UPDATE/DELETE raise),
crash-shaped tx atomicity, lock exclusivity (second open fails), replay ==
incremental projection on generated sequences.

**M2 — Full event catalog & domain fold.**
All §5.2 event types with versioned payloads; vocabulary entities + default
states seeded as ordinary events; things (create/supersede/retract,
containment, promotion/demotion per §2.1), dependencies (on_abandoned
policy, composite endpoints, inherited edges), requirements, resources,
capabilities, allocations; every append-time invariant in §5.2;
expanded-leaf-graph acyclicity with cycle reporting; expected_versions
precondition checking.
*Gate tests:* golden JSONL fixture logs → projection snapshots; invariant
fuzzing (random valid command sequences → assert all invariants after every
batch); spec tests for promotion/demotion and the ancestor-subtree cycle
case; replay-determinism property test now covering the full catalog.

**M3 — Matching, statuses, analytics.**
match package (+ exhaustive brute-force oracle test on small instances);
derived status table §2.2 incl. precedence and resumable-now; composite
rollup table §2.1; ready list; blocked-by minimal frontier + inverse view;
criticality (downstream reach / immediate unlock / remaining depth over
expanded leaves); contention (authoritative matching number + labeled
heuristics); starvation stints & cumulative credit per §3.3–3.4;
recommendation score with explanations; progress rollup (abandoned-only "—"
case).
*Gate tests:* matcher vs brute force; status disjointness property; analytics
golden tests; starvation credit survives ready-flip; determinism of all
rankings.

**M4 — CLI & interchange.**
export-log (canonical JSONL streaming), import-log (empty-dir only, full
fold+validation replay, all-or-nothing), backup (SQLite online backup API),
reindex (rebuild event_refs), serve.
*Gate tests:* export→import→export byte-identical; import rejects tampered
logs (broken seq, dup id, bad first event, invalid domain fact); backup
taken under concurrent writes opens clean.

**M5 — HTTP API.**
§5.1 surface: entity CRUD as event appends, transition propose→confirm with
critical-section re-validation, /batch preview→commit, graph?as_of (batch-
boundary snapping), analytics endpoints, history (filters + format=jsonl).
Structured error envelope: {kind, message, ids[]} for cycles /
blocked-retractions / stale-version conflicts / infeasible allocations.
*Gate tests:* httptest end-to-end flows incl. conflict paths (concurrent
edit, drifted proposal), as_of snapping, JSON-only content-type handling.

**M6 — Frontend.**
Vendor cytoscape+dagre via npm into web/vendor (committed); esbuild bundle;
typed API client (hand-written types mirroring Go); screens: ready board
(daily driver), graph view (status colors, collapse, cone highlight,
details panel), resource board, bottleneck dashboard, hierarchy/progress
view, vocabulary manager, bulk table editor over /batch, per-entity history
timeline. Embedded via embed.FS.
*Gate:* build reproducible offline (vendored deps only); Go binary serves UI;
smoke-test flows against a seeded workspace via the in-app browser.

**M7 — Hardening pass.**
Full-suite -race run, golangci-lint clean, review of every TODO, README
(build, run, backup/restore discipline), seed-demo command for a realistic
example workspace.

## Quality gates (every milestone)

1. `go build ./... && go vet ./... && go test -race ./...` green.
2. Adversarial review agent: correctness vs DESIGN.md clause-by-clause for
   the milestone's scope, determinism hazards (map iteration, tie-breaks),
   concurrency (writer/readers), Windows specifics (paths, locking).
3. Findings fixed and re-verified before the next milestone starts.
