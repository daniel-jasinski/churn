# AGENTS.md

Guidance for any agent (or human) making changes in this repository. It exists
so contributions stay consistent with the patterns the codebase already
commits to. Read it before writing code, and follow it unless a change
deliberately and explicitly revises a pattern here.

churn is a local-first work dependency & resource tracker: a single Go binary
with an append-only SQLite event log, a pure in-memory projection built by
folding that log, analytics over the projection, and an embedded
vanilla-TypeScript web UI. The specification is [DESIGN.md](DESIGN.md); the
package map and performance envelope are in the [README](README.md).

## The gate — green before every commit

Run the full quality gate and make it pass before every commit:

```
scripts/gate.sh          # gofmt, go build, go vet, go test -race, golangci-lint
```

(`scripts\gate.cmd` on Windows-native shells.) The race detector needs cgo; on
Windows that means a mingw-w64 gcc on PATH, which the script picks up from
`C:\dev\mingw64\bin` when present. `SKIP_RACE=1 scripts/gate.sh` is a faster
pre-check, never a substitute. Never commit with a red gate.

## Commit messages

Keep them concise, and explain **why** the change was made — the motivation,
the problem being solved, the constraint being honored. Do **not** narrate
**what** changed line by line: the diff already shows that. State the reasoning
a reviewer or a future reader cannot recover from the diff alone.

- Subject: a short imperative summary, no trailing period.
- Body (when the change needs one): the why — intent, trade-offs, a bug's
  failure mode, the invariant being preserved. Wrap prose; use bullets for
  distinct points.
- Scope each commit to one logical change; commit per feature/milestone rather
  than dumping unrelated work together.
- **Never** add a `Co-Authored-By` trailer — not for a model, not for a tool,
  not for an agent. Authorship is already carried by the committer, and a
  trailer naming a model is a false attribution the moment a different one does
  the work. This is a hard rule: no exceptions, no variants (`Assisted-By`,
  `Generated-With`, and the like are the same thing wearing a different key).

## Established patterns to preserve

These are load-bearing. Breaking one is a defect even when tests pass.

**Event sourcing, append-only.** The log is the only source of truth; nothing
is ever mutated or deleted in place. All change is three verbs — assert/create,
supersede (full replacement of an attribute set), retract (tombstone). A new
event type is registered in `internal/event`'s catalog, gets a row in the
catalog test (which asserts registry and test stay in sync), and implements
`Refs()` when it references entities beyond its own — the `event_refs` side
table is derived from that.

**One brain.** Domain meaning is computed in exactly one place: the fold and
the pure `internal/domain` core. No SQL computes status; analytics never
re-derive what the domain already knows; the single `internal/match` engine
backs readiness, contention, and proposals alike. Do not add a second
interpretation of the log.

**fold ⇄ validate parity.** `ValidateBatch` rejects invalid facts before they
reach the log; the fold (`Apply`) is a total, deterministic function over
already-validated logs and fails closed only on structurally impossible input.
A new event needs BOTH a `validateEvent` case and a `fold` case, kept
consistent — a batch that validates must fold without error, and vice versa.

**Clone is a maintenance obligation.** `Projection.Clone` must deep-copy every
reference-typed field; a shared reference would let a candidate batch mutate
the published projection mid-flight. Every new reference-typed field on the
projection (or on an entity struct it stores) also needs a case in
`TestCloneIsIndependent`, which reflectively guards this.

**Determinism.** Iterate maps in sorted order anywhere output or ordering can
leak (each layer has a `sortedKeys` helper). The fold reads no wall clock and
mints no ids — timestamps and ids arrive inside events; the writer assigns
them. Replay must reproduce a projection identically.

**Permissive log, fail-closed reader.** Unknown payload *fields* are tolerated
so payloads can grow compatibly; an unknown event *type* or unsupported version
is an error — a reader halts rather than fold a plausible-but-wrong projection.

**Canonical everything.** JSON is canonicalized on write (sorted keys, minimal
whitespace, byte-stable); ids are typed-prefixed ULIDs; timestamps use the
fixed-width UTC form so lexical order is chronological order. Export → import →
export is byte-identical.

## Frontend

The UI is vanilla TypeScript under strict mode, bundled by esbuild. `web/dist/`
is committed and embedded via `embed.FS`, so `go build` needs no Node
toolchain.

- Rebuild `dist/` (`scripts/web-build.sh`) and commit it together with any
  `web/src` change — a freshness test in `internal/server` fails the gate if
  source ships without a matching rebuild.
- Never use `innerHTML` or build markup from strings. Use the `h()` builder
  (text nodes only): the UI is XSS-safe by construction, and it must stay that
  way.
- `web/src/api.ts` mirrors `internal/server/dto.go` by hand. Change one, change
  the other in the same commit.
- esbuild only strips types (there is no `tsc` in the pinned build); keep types
  honest regardless, and lean on the closed unions already in place.

## Testing

- Regression-pin every fix: a bug fix lands with a test that fails before it
  and passes after.
- Table-driven tests are the norm, run under the race detector (the gate does).
- Prefer testing through the real surface — the `internal/domain` batch
  harness, the `internal/server` httptest server, the in-process CLI `run` —
  over reaching into internals.

## Workflow for non-trivial changes

Implement → run the gate → for anything beyond a trivial change, get an
adversarial review (a skeptical pass hunting for real defects) → fix what it
finds → commit. Verify behavior rather than assume it: run the binary or the UI
when a change is observable there.
