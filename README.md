# churn

Churn is a local-first work dependency and resource tracker: a single Go
binary that keeps a project graph ("things" and the dependencies between
them) and a shared resource pool in an append-only event log, and answers —
at any moment — what is ready to work on, what is blocked and by what, where
the bottlenecks are, and what to do next. It is deliberately
ontology-minimal: states, thing types, and capability tags are user-defined
vocabulary, not schema. The full specification is [DESIGN.md](DESIGN.md);
the build order and quality gates are [PLAN.md](PLAN.md).

## Quickstart

```
go build ./cmd/churn                 # produces churn / churn.exe
churn seed-demo --data ./demo        # a realistic two-project demo workspace
churn serve --data ./demo            # http://127.0.0.1:8080
```

Open the printed address in a browser: ready board, graph view, resource
board, bottleneck dashboard, hierarchy view, vocabulary manager, bulk table
editor, and per-entity history are all served from the binary (the frontend
is embedded; there is no CDN and no network dependency, ever).

For a real workspace, point `--data` at any directory you want the event
log to live in; it is created on first `serve`.

## CLI

| command | what it does |
|---|---|
| `churn serve --data <dir> [--listen 127.0.0.1:8080] [--actor <name>] [--verbose]` | run the workspace server (lock, replay, writer, HTTP API + UI) |
| `churn export-log --data <dir> [--out <file>]` | stream the event log as canonical JSONL (works against a live server) |
| `churn import-log --data <dir> <file\|->` | restore a JSONL log into an **empty** data directory, re-validating every batch |
| `churn backup --data <dir> <dest.db>` | consistent online snapshot via SQLite backup (works against a live server) |
| `churn reindex --data <dir>` | rebuild the derived `event_refs` side table |
| `churn seed-demo --data <dir>` | create a demo workspace in an empty directory |

Run `churn <command> -h` for flags.

## Backup and restore

The event log is the only persistent state; everything else is a
projection rebuilt by replay (DESIGN.md §5.4).

- **Backup**: `churn backup --data <dir> dest.db` takes a transactionally
  consistent snapshot while the server runs — no shutdown needed. A backup
  is a complete workspace: point `serve --data` at a directory containing
  it and go.
- **Text form**: `churn export-log` streams the log as canonical JSONL —
  greppable, diffable, git-versionable, and byte-stable (export → import →
  export is byte-identical). This is also the interchange/escape-hatch
  format: the SQLite file is a medium, never a lock-in.
- **Restore**: `churn import-log` replays a JSONL stream into an empty
  directory, running every batch through the same fold and validation as
  live writes. All-or-nothing: any violation aborts with nothing written.
- **The one operational "don't"**: never hand-copy `workspace.db` out of a
  live data directory — a mid-write copy can tear the WAL. The backup
  command exists so nobody has to.

## Development

Gate (green before every commit):

```
go build ./... && go vet ./... && go test -race ./...
```

On Windows, `-race` needs cgo — a mingw-w64 gcc on PATH:

```
PATH="/c/dev/mingw64/bin:$PATH" CGO_ENABLED=1 go test -race ./...
```

Lint (config in `.golangci.yml` — govet, errcheck, staticcheck, unused,
ineffassign, misspell, plus exhaustive scoped to the closed-set switches of
`internal/domain` and `internal/event`):

```
golangci-lint run ./...
```

Frontend: `web/dist/` is committed and embedded, so `go build` needs no
Node toolchain. To change the UI, rebuild `dist/` and commit it together
with the source change — see [web/README.md](web/README.md); a freshness
test fails the gate if a `web/src` change ships without a rebuild.

### Scripts

The common workflows live in `scripts/` in two equivalent flavors: Git
Bash (`.sh`) and Windows-native cmd/PowerShell (`.cmd`). Both derive the
repo root from their own location — run them from anywhere.

| workflow | Git Bash | Windows (cmd.exe) |
|---|---|---|
| Release build → `./churn.exe` (trimpath, stripped) | `scripts/build.sh` | `scripts\build.cmd` |
| Dev server: build + seed `./workspace` (if missing) + serve :8080 | `scripts/dev.sh [serve flags]` | `scripts\dev.cmd [serve flags]` |
| Full quality gate (gofmt, build, vet, race tests, lint) | `scripts/gate.sh` | `scripts\gate.cmd` |
| Frontend rebuild (`npm ci` if needed + esbuild → `web/dist`) | `scripts/web-build.sh` | `scripts\web-build.cmd` |
| Online DB snapshot (timestamped default name) | `scripts/backup.sh <data-dir> [dest]` | `scripts\backup.cmd <data-dir> [dest]` |
| JSONL log export (timestamped default name) | `scripts/export.sh <data-dir> [out]` | `scripts\export.cmd <data-dir> [out]` |

`SKIP_RACE=1` makes the gate run plain `go test` without the race
detector — a quick pre-check, not a substitute for the full gate.
`golangci-lint` is optional: the gate warns and skips when it is not
installed.

### Architecture map

| package | responsibility |
|---|---|
| `cmd/churn` | CLI wiring: flags, files, exit codes (and the seed-demo fixture) |
| `internal/canonjson` | canonical JSON: sorted keys, minimal whitespace, byte-stable |
| `internal/ulid` | ULIDs, monotonic within a millisecond, injectable clock/entropy |
| `internal/event` | envelope + typed event catalog: (type, v) → decode, validate, refs |
| `internal/domain` | pure core: projection, fold, batch validation, derived statuses |
| `internal/match` | the one bipartite matching engine behind readiness, contention, proposals |
| `internal/analytics` | ready list, blocked-by, criticality, contention, starvation, recommendations |
| `internal/store` | SQLite log: append-only triggers, batch transactions, backup, lock |
| `internal/writer` | the single writer goroutine: validate → commit → atomic publish |
| `internal/interchange` | canonical JSONL export and validated all-or-nothing import |
| `internal/server` | HTTP API `/api/v1` + embedded static frontend |
| `web/` | vanilla TypeScript frontend, vendored Cytoscape.js + dagre, esbuild |

### Performance envelope

The design scale is hundreds of things per project (DESIGN.md); everything
is computed in memory from scratch on demand. Measured on the checked-in
benchmarks (i9-12900HK, 500 things / 300 dependency edges, ~1,000-event
log):

- full replay (fold) in the pathological every-event-its-own-batch shape:
  ~82 ms (~13k events/s); realistically batched logs replay faster — startup
  and `as_of` time-travel both ride this path
- one writer batch (clone + validate + status refresh): ~1 ms
- full derived-status sweep: ~0.4 ms; analytics endpoints (ready,
  contention, criticality, recommend): 1–12 ms each

`as_of` views re-fold the log per request. That is well inside budget at
this scale and for years of history; if scrubbing ever stutters, the
reserved fix is derived projection snapshots (DESIGN.md §3.6), not caching
in the read path.

## Status

Phases 1 and 2 of the DESIGN.md evolution path are implemented: the
event-sourced core, vocabulary, derived statuses, all analytics, allocation
propose→confirm, atomic bulk editing, and the full web UI. Phase 3
(LAN/multi-user: sessions, server-stamped identity, CSRF hygiene) and phase
4 (consumables, durations, calendars, log merging) are not built; the log
format and API carry the seams they need (SSE refresh already works, and
`actor` attribution is on every event).
