# churn

Churn is a local-first work dependency and resource tracker: a single Go
binary that keeps a project graph ("things" and the dependencies between
them) and a shared resource pool in an append-only event log, and answers —
at any moment — what is ready to work on, what is blocked and by what, where
the bottlenecks are, and what to do next. It is deliberately
ontology-minimal: states, thing types, resource types, and capability tags
are user-defined vocabulary, not schema. The full specification is
[DESIGN.md](DESIGN.md).

## Quickstart

```
go build ./cmd/churn                 # produces churn / churn.exe
churn seed-demo --data ./demo        # a realistic two-project demo workspace
churn serve --data ./demo            # serves + opens the UI in your browser
```

`serve` prints the address and opens it in your browser (pass `--no-open`
to skip). Everything is served from the one binary: ready board, graph
view, resource board, bottleneck dashboard, hierarchy view, vocabulary
manager, bulk table editor, and per-entity history — the frontend is
embedded, with no CDN and no network dependency, ever.

The workspace directory is `--data`, or the `CHURN_DATA` environment
variable, or the current directory — in that order. So inside a workspace
you can just run `churn serve`. It is created on first `serve`. The port is
`--port`, or `CHURN_PORT`, or a default of `24876`; `--listen host:port`
gives full control of the bind address.

## CLI

| command | what it does |
|---|---|
| `churn serve [--data <dir>] [--port <n>] [--listen <addr>] [--actor <name>] [--no-open] [--verbose]` | run the workspace server (lock, replay, writer, HTTP API + UI) and open the UI |
| `churn ls [projects\|things\|resources] [--data <dir>] [--project <id>] [--json]` | list workspace contents as a table (or `--json`) from the terminal |
| `churn export-log [--data <dir>] [file]` | stream the event log as canonical JSONL (works against a live server; `-`/omitted = stdout) |
| `churn import-log [--data <dir>] <file\|->` | restore a JSONL log into an **empty** data directory, re-validating every batch |
| `churn backup [--data <dir>] <dest.db>` | consistent online snapshot via SQLite backup (works against a live server) |
| `churn reindex [--data <dir>]` | rebuild the derived `event_refs` side table |
| `churn seed-demo [--data <dir>]` | create a demo workspace in an empty directory |
| `churn version` | print version and build information |

`--data` defaults to `CHURN_DATA` then the current directory. `ls`, like
`export-log` and `backup`, is read-only and works against a live server.
Run `churn <command> -h` (or `churn help <command>`) for flags; a mistyped
command suggests the nearest match.

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

Contributor and agent guidance — the quality gate, commit conventions, and
the architectural patterns to preserve — lives in [AGENTS.md](AGENTS.md).
In short: `scripts/gate.sh` (gofmt, build, vet, race tests, lint) must be
green before every commit, and any `web/src` change must ship with a
rebuilt, committed `web/dist/` (a freshness test enforces this). The lint
config is `.golangci.yml`; on Windows the race detector needs a mingw-w64
gcc on PATH, which `scripts/gate.sh` handles.

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
event-sourced core; the vocabulary of states, thing types, resource types,
and capabilities (with optional typed metadata fields declared per type);
derived statuses; all analytics; allocation propose→confirm; atomic bulk
editing; free-text notes on things; and the full web UI. Phase 3
(LAN/multi-user: sessions, server-stamped identity, CSRF hygiene) and phase
4 (consumables, durations, calendars, log merging) are not built; the log
format and API carry the seams they need (SSE refresh already works, and
`actor` attribution is on every event).
