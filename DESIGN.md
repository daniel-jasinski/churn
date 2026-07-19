# Churn — Work Dependency & Resource Tracker

## Design Document — v0.4 (2026-07-19, post third design review)

---

## 1. Purpose

A tool for people coordinating multi-step work across projects. It keeps the
dependency graph and resource picture out of the team's heads and answers, at
any moment:

- **What is ready to be worked on right now?**
- **What is blocked, and by what?**
- **Where are the bottlenecks** — which things or resources gate the most downstream work?
- **What should we work on next** to keep the flow moving?

The tool is deliberately **ontology-minimal**: it understands only two
categories — **Things** (work to be done) and **Resources** (what work is done
with). All domain vocabulary (task, deliverable, phase, reviewer, workspace,
tool…) is user-defined data, not schema.

### Decisions made (with the user)

| Question | Decision |
|---|---|
| Time model | **None in V1.** Pure dependency/readiness intelligence. Durations may be added later as optional metadata; nothing depends on them. |
| Project shape | Multiple independent **one-off projects**; no template/instance layer. |
| Scale | **Hundreds of things** per project — everything computed in-memory, no pagination. |
| Platform | **Local web app**: single Go binary, SQLite, embedded browser UI. |
| Resource allocation | **Tracked**: starting a thing checks resources out; finishing releases them. Tool computes `ready` vs `resource-blocked`. |
| Resource pool | **Shared globally** across projects in a workspace. Cross-project contention is first-class. |
| Data entry | Web UI only, including bulk operations (multi-add, outline/table editing) committed as single atomic event batches. CSV import/export considered and **dropped** — the log is the sole persistent format and the backup. |
| Hierarchy semantics | **Containment implies rollup**: composites are computed from children; only leaves are worked directly. |
| Consumables | **Later phase.** Schema carries a resource `kind` field so consumable stock can be added without migration. |
| Multi-user | **Small trusted team on LAN** later: lightweight identity, change attribution, audit log. No roles/permissions. |
| Persistence | **Append-only event log** (beads-style): immutable facts only — edits are supersessions, deletes are retractions, nothing is ever rewritten. Current state is an in-memory projection rebuilt by replay. History, audit, and time-travel are inherent, not features. Medium: an **INSERT-only SQLite table** (batch = transaction, WAL crash safety); canonical **JSONL via export** for grep/diff/git/interchange. |
| States | **Not hard-coded.** The engine knows a closed set of state *semantics*; users define arbitrarily many named states bound to a semantic. Default states ship as ordinary data. |

---

## 2. Domain model

### 2.1 Thing

A node in a directed acyclic graph, scoped to a project.

- **type** — a reference to a user-declared type in the vocabulary (§5.3) —
  e.g. `task`, `step`, `deliverable`, `review`. The system attaches no meaning
  to it; it drives filtering, coloring, and reporting only.
- **metadata** — arbitrary key/value JSON (external reference, priority,
  revision, notes…). Schema-free by design.
- **parent** — optional containment: a thing may live inside a composite thing
  (task inside a workstream, workstream inside a project). Containment forms a tree.
- **dependencies** — edges to other things that must be satisfied before this
  thing can start. Dependencies may cross containment boundaries and (rarely)
  projects — the graph is per-workspace, partitioned by project for display.
- **requirements** — what resources this thing needs while being worked
  (see 2.3).

**Leaf vs composite.** A thing with children is a **composite**: it is never
worked on directly, carries no requirements, and its state is a **rollup**
computed from its children (satisfied when all children satisfied; working
when any child active; etc.). Work that belongs to the composite itself — e.g.
"final review of Initiative A" — is modeled as a *child leaf step* with explicit
dependencies on its sibling tasks. This keeps one uniform rule: **only leaves
have state transitions, requirements, and allocations.**

**Composite rollup is a closed table** (the composite analogue of the
semantics table in §2.2) — no case is left to intuition:

| composite reads as | condition |
|---|---|
| `finished` | every leaf in the subtree is satisfied or abandoned |
| `working` | any leaf active |
| `held` | no leaf active, some leaf paused, no leaf pending |
| `pending` | otherwise (some leaf still pending) |

The subtree additionally carries a computed `has_abandoned` flag.

A dependency edge pointing *at a composite* means "depends on the entire
subtree" — the ergonomic shortcut that avoids drawing N edges to every child.
Such an edge applies **its own** `on_abandoned` policy to the subtree's
`has_abandoned` flag: policy *block* keeps the dependent blocked while any
subtree leaf is abandoned; *ignore* unblocks with the warning badge. (Without
this rule the per-edge policy would be dead code — abandonment happens at
leaves, where composite-targeted edges never look.) Formally: *an edge
targeting composite C is satisfied ⇔ every leaf in C's subtree is terminal
(satisfied or abandoned) ∧ every abandoned leaf is permitted by this edge's
policy.* Abandonment never accelerates satisfaction — a subtree with
pending leaves satisfies no one.

Edges are symmetric in what they may touch: **an edge may also originate at
a composite**, meaning "every leaf in this subtree depends on the target" —
the constraint is *inherited* by all current **and future** children
  ("nothing in Workstream B starts before the approval"). Inheritance is
what makes promotion meaning-preserving: when a leaf with outbound
dependencies becomes a composite, its edges stay exactly where they are and
now bind the whole subtree — no edge rewriting, and no later-added child can
silently escape a constraint the original leaf carried. (Moving the edges
onto one generated child would create exactly that escape.)

**Acyclicity is validated on the expanded leaf graph** — every composite
endpoint expanded to its leaves, inherited edges included — not on the
declared graph. This catches constraints that are declared-acyclic but
effectively cyclic, e.g. a leaf depending on its own ancestor's subtree
(expansion makes it depend on itself → rejected, with the expanded cycle
shown).

**Promotion and demotion.** Composites are not created — they *happen*, the
moment a first child is parented under a leaf. To keep the leaf-only-state
invariant honest: parenting a child under a leaf is **rejected** unless that
leaf is in a pending-semantic state with no requirements; the UI offers the
one-click conversion instead (move the leaf's state and requirements onto an
  auto-created child step — the same affordance as the "final review"
pattern). The conversion is precise: it requires **no open allocations** —
never while active; pause first — and is one batch emitting the child's
`thing.created`, the parent's requirements retracted and re-asserted on the
child, and the child's `state_changed` into the former leaf's state. The
entity's pre-promotion history (states held, allocations closed) stays
attached to its id as immutable history — visible in timelines, never
re-folded into current state. Retracting a composite's last child demotes it to a leaf, which
enters the default pending state via an explicitly appended `state_changed` —
stale pre-composite facts in the log are never resurrected by structural
change.

### 2.2 States and semantics

The engine hard-codes **no state names**. It understands a closed set of
**semantics** — the behavioral categories every computation is keyed off —
and users define named **states** bound to exactly one semantic:

| semantic | what the engine does with it |
|---|---|
| `pending` | Not started. Eligible for the ready list once dependencies are satisfied. |
| `active` | Being worked. Holds resource allocations. |
| `paused` | Deliberately not being worked. Excluded from ready lists; holds no allocations; dependents stay blocked. |
| `satisfied` | Terminal success. Satisfies dependents; counts toward progress. |
| `abandoned` | Terminal non-success. Per-dependency-edge policy decides whether dependents unblock (default: unblock, with a warning badge). |

**Predefined states ship as ordinary data**, not code:
`todo`→pending, `in_progress`→active, `done`→satisfied, `on_hold`→paused,
`cancelled`→abandoned. The user can add any number of their own —
`queued`→pending, `executing`→active, `awaiting-approval`→paused,
`completed`→satisfied, `declined`→abandoned — each with a name, semantic,
color, and description. The engine understands a new state the moment it is
defined, because it only ever reads the semantic; the name exists for human
classification, filtering, and reporting. States are workspace-level vocabulary entities,
created through events like everything else (`state.defined`) and referenced
by stable id — names are display facts (§5.3).

Every engine rule references semantics only:

- **readiness** — all dependencies are in a `satisfied` state (or `abandoned`
  with edge policy *ignore*);
- **allocation** — entering any `active` state opens allocations
  (feasibility-checked); leaving active semantics releases them; moving
  between two active states keeps them untouched;
- **ready list** — draws from leaves in `pending` states;
- **progress rollup** — satisfied leaves over non-abandoned leaves.

Transitions are unconstrained (any state → any state): each transition is
simply a new appended fact. The UI may warn on unusual moves
(satisfied → pending, i.e. reopening); the engine records rather than forbids.

**Derived status** (computed, never stored, and *not* user-extensible):

| status | condition |
|---|---|
| `blocked` | some dependency not satisfied |
| `ready` | deps satisfied ∧ all requirements currently satisfiable |
| `resource_blocked` | deps satisfied ∧ some requirement not satisfiable right now (resources busy or unavailable) |
| `working` / `finished` / `held` / `dropped` | mirrors the thing's state semantic (active / satisfied / paused / abandoned) |

Rows are disjoint by precedence: the state-semantic mirrors
(`working`/`finished`/`held`/`dropped`) win; `blocked`/`ready`/
`resource_blocked` apply only to leaves in pending-semantic states. (So a
satisfied thing that later gains an unsatisfied dependency reads `finished`,
with a consistency warning — not `blocked`.) `held` things additionally carry
a computed **resumable-now** indicator (same feasibility routine as `ready`),
so un-pausable work is visible without trying to resume it.

The division of vocabulary is deliberate: **users extend the vocabulary of
facts** (states they assert about the world); **the engine owns the vocabulary
of conclusions** (statuses it computes). Derived status is recomputed from
scratch on every read — at hundreds of nodes this is microseconds, and it
eliminates an entire class of cache-staleness bugs.

### 2.3 Resource

A workspace-global entity work is done *with*.

- **name** — always present ("Reviewers", "Workspace-04", "Maria K.").
- **kind** — `reusable` (V1) | `consumable` (reserved for later).
- **capacity** — integer ≥ 1. A **fungible pool** is one resource row with
  capacity N ("4 reviewers"). A **named (non-fungible)** resource is
  capacity 1 with `named = true` ("Workspace-04").
- **capabilities** — set of user-defined tags (`editing`, `data-analysis`,
  `facilitation`, `approval`), each a declared vocabulary entity (§5.3) — a
  typo cannot silently break matching. Named resources carry their individual
  capability sets; a pool's capabilities apply to every unit in it.
- **availability** — boolean toggle + note ("maintenance until further
  notice", "on leave"). Unavailable resources count as capacity 0. (A real
  calendar is a later-phase upgrade; the toggle covers the V1 need.)
- **type** — optional reference to a user-declared **resource type** (§5.3):
  categorization for boards and reports ("person", "room", "license"). The
  engine attaches no meaning to it — matching remains capability-based
  (§2.4); a typed and an untyped resource behave identically.

> Modeling guidance surfaced in the UI: if individual people or tools within a
> group differ in skills or you care *which one* did the work, model them as
> named resources sharing capability tags — fungibility then emerges from the
> capability match rather than from a pool. Pools are for genuinely
> interchangeable units you don't track individually.

### 2.4 Requirement

A thing (leaf) declares zero or more requirements. Each requirement is:

- **quantity** — how many units needed (default 1), and *either*
- **capabilities** — a set of tags; satisfied by any resource(s) carrying
  **all** of them (AND semantics), *or*
- **resource** — a pin to one specific named resource.

Multiple requirements on one thing are all needed simultaneously
("1× `editing` + 1× `approval` + specifically `Workspace-04`").

**Satisfiability is an assignment, not a per-requirement count.** One
resource unit satisfies at most one requirement of one thing at a time. So
"this thing could start now" means *a feasible matching of its requirements
onto distinct free units exists* — the greedy per-requirement check would
wrongly call a thing ready when one multi-capability unit is the only
candidate for two requirements. Computed as a tiny bipartite matching over
free units; it is the same routine that powers the `ready` status, the
resumable-now indicator, and the allocation proposer — shared code, trivial
at this scale. The definition is deliberately **per-thing**: N ready things
may compete for the same unit, and that competition is precisely what the
contention dashboard (§3.3) surfaces — the tool informs, the human sequences.

Pins must reference a `named` resource at assert time; a resource referenced
by pins cannot lose its `named` flag or be retracted (same rule shape as
state retraction, §5.2).

*Deliberately excluded from V1:* OR-alternatives ("reviewer OR approver"),
partial/staged requirements, and preemption. AND-of-requirements plus
capability tags covers the stated need; alternatives can be approximated with
a shared tag.

### 2.5 Allocation

Opened when a thing enters a state with `active` semantics; closed when it
leaves active semantics.

- Links thing → resource, with quantity, timestamps, the user who acted —
  and **the requirement it satisfies**. The matching's *result* is recorded,
  not just its net effect; "allocations out of step with requirements" is
  then explainable slot by slot instead of being a diff puzzle.
- On start, the tool proposes a concrete assignment (which resources satisfy
  which requirement) and the user confirms or overrides — the human stays in
  control; the tool does the matching legwork.
- Allocations are the ground truth for "who/what is busy", the resource board,
  and `resource_blocked` computation.
- Closed allocations are kept forever → a free work-history log per resource
  and per thing.
- **Reality wins; warnings follow.** Resource events never force-close
  allocations. If capacity is lowered or availability toggled off while
  units are allocated, free capacity clamps at
  `max(0, effective − allocated)` and both the resource row and the affected
  active things get an **over-allocated / pinned-resource-down** badge — the
  thing's card *suggests* pausing, never forces it (a named resource becoming
  unavailable during work is a normal case, not an error). Likewise, requirements may be edited on
  an active thing — as `requirement.superseded` (retraction stays blocked
  while any open allocation references the requirement, per §5.2; without
  supersession this whole flow would contradict reference integrity). The
  thing is badged *allocations out of step with requirements* — computed by
  comparing the requirement's current version against its version when each
  allocation opened, both derivable in the fold — and a one-click re-propose
  reconciles **atomically**: one batch closes the obsolete allocations and
  opens their replacements while the thing remains active throughout.

---

## 3. Analytics (the point of the tool)

All computed on the in-memory graph; all are simple graph algorithms — no
solver needed at this scale and without a time model.

### 3.1 Ready-work discovery
Ready list = leaves with derived status `ready`, grouped/filterable by
project, type, composite subtree, or required capability. The everyday screen.

### 3.2 Blocked-by explanation
For any thing: the minimal frontier of unfinished dependencies (transitively
reduced — show the *nearest* blockers, expandable to full chains). Inverse
view: "if X finishes, these N things become ready."

### 3.3 Bottleneck detection
Without durations, bottlenecks are structural:

- **Thing criticality** — two honestly separated numbers, because
  transitive reach does *not* mean immediate unblocking (dependents may have
  other blockers): **downstream reach** (count of transitive dependents —
  everything that can never finish without this) and **immediate unlock**
  (dependents that become dependency-ready under a simulated completion of
  this thing). Plus **remaining depth** (longest chain of unfinished things
  through it — the critical path in *steps*). Edges pointing at composites
  are expanded to the subtree before counting, so all three are measured
  over leaves.
- **Resource contention** — measured with the *same matching engine that
  defines readiness*, per **requirement signature** (the full AND-set, or
  the pin): compute a **maximum-cardinality** assignment of all ready +
  frontier requirement units onto free units. The authoritative number is
  **total unmet requirement units**; per-signature and per-resource
  attribution depends on matching tie-breaks and is labeled *indicative*
  (with a deterministic tie-break for stable displays). Slot-level matching
  is never read as "how many things can start" — a thing needs all its
  requirements at once, and that all-or-nothing grouping is checked only by
  the per-thing readiness matching.
  Naive per-tag `demand/capacity` ratios double-count multi-capability units
  and ignore conjunctions — where shown as at-a-glance indicators they are
  labeled *heuristic*; the matching-based number is the authoritative one.
  A signature wanted by 6 ready things with marginal capacity 2 is a
  flashing light.
- **Starvation** — things `resource_blocked` for a long **uninterrupted
  stint**. Derived status is never stored, but the projection re-evaluates
  it after every committed batch and keeps each thing's current
  status-entry timestamp — a pure function of the log, rebuilt identically
  on replay. The dashboard highlights the current stint; the recommendation
  credit (§3.4) uses **cumulative resource-blocked time since the thing last
  held allocations** — a number that must survive the flip to `ready`, or it
  would vanish exactly when it could finally matter.

### 3.4 Next-work recommendation ("optimal path" without time)
Rank the ready list by a transparent score:

```
score = w1 · immediate_unlock           (dependents made ready if this finishes)
      + w2 · downstream_reach           (everything transitively waiting on it)
      + w3 · remaining_depth            (keeps the longest chain moving)
      + w4 · waiting_age                (starvation credit — first claim on freed capacity)
      − w5 · resource_scarcity_penalty  (prefer work that doesn't hog contended
                                         resources unless it's high-impact)
```

`resource_scarcity_penalty` is the matching-based pressure (§3.3),
aggregated over a multi-requirement thing as the **max** among its
signatures — its bottleneck. A pure penalty would systematically starve
exactly the work that needs contended resources, so `waiting_age`
counterweights it: cumulative resource-blocked time since the thing last
held allocations, **retained when the thing flips to ready** — credit that
expired the moment capacity freed would influence nothing, and the point is
that long-starved work gets first claim on the unit that finally freed.
Every explanation discloses it ("waited 6 days for approval capacity"). Weights are
live workspace settings — recommendations are advice given *now*, not
reproducible historical artifacts; the log records the decisions taken
(transitions), never the advice.

Weights visible and adjustable; every score explains itself
("unblocks 23; on the deepest chain (14 steps); needs contended `approval`").
This is a *decision aid*, not an optimizer — an honest match to a no-time-model
world. If durations arrive later, this slot is where real scheduling plugs in.

### 3.5 Progress rollup
Every composite gets `satisfied-leaves / non-abandoned-leaves`, rolled up the
containment tree → progress bars at every level, treemap view. A composite
whose every leaf is abandoned has no denominator: it displays "—" with the
abandoned badge (its rollup state is `finished` + `has_abandoned`, per the
§2.1 table — never a division by zero, never a silent 100%).

### 3.6 History & time-travel (free with the event log)
Because the store is an append-only log, every analytic can be answered
**as of any past moment** by replaying to that point: "state of the workspace
last Tuesday", "what changed this week", per-thing and per-resource
timelines. This is a projection parameter, not a feature to build. Cursors
resolve to **complete batches only**: an `as_of` timestamp or seq snaps to
the last batch committed at or before it — no view can ever expose a state
that "existed" between two events of one atomic operation, and all
status-entry timestamps (§3.3) are batch commit timestamps for the same
reason. If as-of
scrubbing ever stutters (years × allocation churn), the reserved fix is
periodic projection snapshots — a pure *derived* cache, rebuildable from the
log at will, never a second source of truth.

---

## 4. Visualizations

1. **Graph view** (per project) — DAG rendered left-to-right in dependency
   order; nodes colored by derived status (blocked / ready /
   resource-blocked / in-progress / done); composites collapsible to a single
   rollup node with a progress ring. Click → details panel; hover → highlight
   upstream/downstream cone.
2. **Ready board** — columns: Ready / Resource-blocked / In progress / Recently
   done. Each card shows requirements and the recommendation score. This is
   the daily driver.
3. **Resource board** — one row per resource (pools show `used/capacity`):
   current allocations, availability toggle, and the queue of ready things
   waiting on it.
4. **Bottleneck dashboard** — top contended capabilities/resources, top
   critical things, starvation list.
5. **Hierarchy/progress view** — containment tree with progress bars, or
   treemap sized by subtree leaf count, colored by completion.

Rendering: **Cytoscape.js** (vendored — closed environment, zero CDN
dependencies) with the dagre layout for DAGs. Hundreds of nodes is well within
its comfortable range.

---

## 5. Architecture

```
┌─────────────────────────── churn.exe (single Go binary) ──────────────────────────┐
│                                                                                   │
│  embed.FS ── static frontend (vanilla TS + Cytoscape.js, no build-time CDN)       │
│                                                                                   │
│  HTTP API (net/http, JSON) ── /api/v1/...                                         │
│                                                                                   │
│  Projection (in-memory, rebuilt by replay at startup):                            │
│    fold(events) → current model → validate → derive statuses → analytics          │
│                                                                                   │
│  Store: append-only event log — one INSERT-only `events` table in SQLite          │
│         (WAL; modernc.org/sqlite — pure Go, no cgo, nothing to install).          │
│                                                                                   │
└───────────────────────────────────────────────────────────────────────────────────┘
        run:  churn.exe --data ./workspace --listen 127.0.0.1:8080
   later:     same binary, --listen 0.0.0.0:8080 on a LAN host → multi-user
```

Principles:

- **The log is the truth; everything else is a projection.** The in-memory
  model is a deterministic fold over the event log, rebuilt at startup —
  replaying months of activity at this scale takes milliseconds, so there are
  no snapshots, no caches, and no invalidation logic. Analytics and derived
  statuses are pure functions over the projection.
- **Append is the only write — and validation lives inside it.** The writer
  goroutine runs *validate and apply against a candidate projection → commit
  the transaction → atomically publish the candidate* as one serialized
  step — the live projection can never fall behind durable truth. If
  publication somehow failed after commit, the server terminates and
  recovers by replay rather than continue on stale state. The two flagship human-in-the-loop flows (propose
  allocation → confirm; bulk-edit preview → commit) are **re-validated at
  commit inside that critical section**; if the world drifted in between (another
  actor took the last available resource unit, a second tab of the same user), the commit
  returns a structured conflict — a fresh proposal or a fresh diff — instead
  of committing stale facts. Batch commands additionally carry
  `expected_versions` (per written entity — §5.2) so concurrent editors
  conflict loudly instead of silently.
- **The binary is the deployment.** Copy `churn.exe` + the data directory.
  Durability and atomicity are SQLite's: a batch is a transaction, so a crash
  can never leave half an operation on disk. Backup = `churn backup` (online
  snapshot while running); transparency = `churn export-log`, streaming the
  table as canonical JSONL — greppable, diffable, git-versionable on demand
  (§5.4).

### 5.1 API surface (sketch)

```
GET/POST/PATCH/DELETE  /api/v1/projects, /things, /resources, /dependencies,
                       /requirements                    (PATCH/DELETE append
                                                        supersession/retraction events)
GET/POST/PATCH/DELETE  /api/v1/vocab/states|types|capabilities        (§5.3)
POST  /api/v1/things/{id}/transition {state}  → to an active state: proposes an
                                                allocation set, confirm to commit
POST  /api/v1/batch                           → any set of mutations as one atomic
                                                event batch (preview → commit); the
                                                substrate for all bulk UI operations
GET   /api/v1/projects/{id}/graph?as_of=…     → full graph + derived statuses;
                                                as_of replays to a past moment
GET   /api/v1/analytics/ready|bottlenecks|recommendations|resource-board
GET   /api/v1/history?entity=…&since=…        → the event log, filtered (this IS
                                                the audit trail)
```

Every event carries the acting user — attribution is the backbone of the
later multi-user story and is inherent to the log, not a bolted-on audit table.

### 5.2 The event log

Source of truth: a single **INSERT-only `events` table** (SQLite, WAL mode).
Rows are never updated or deleted — enforced by trigger, not promised by
convention — and the three verbs below are the only model of change. The
logical envelope (columns + JSON `data`; identical shape in the JSONL
export):

```json
{"seq": 1042, "id": "01J3ZK7Q8R2M5N9P4T6V8X0Y2Z",
 "origin": "ws_7c41d0f2", "batch": "01J3ZK7Q8QAB…", "causes": null,
 "ts": "2026-07-19T14:02:11Z", "actor": "daniel",
 "type": "thing.state_changed", "v": 1, "entity": "th_8fk2",
 "data": {"state": "st_04"}}
```

Envelope fields that must exist from day one because they cannot be
retrofitted onto immutable history:

- **`id`** — a ULID: globally unique, lexically time-sortable. `seq` is
  positional *per log*; only `id` survives a future merge of logs.
- **`origin`** — the id of the **writer lineage** that appended the event,
  distinct from the immutable `workspace_id` recorded by the log's mandatory
  first event, `log.initialized`. A restored or copied directory that
  resumes writing first appends `writer.started`, minting a fresh origin —
  so two independently evolving copies of one workspace can never masquerade
  as one writer, while historical events keep the origin they were written
  under. (A single conflated id could distinguish neither.)
- **`v`** — schema version of this event type's payload; readers are
  permissive across versions.
- **`batch`** — groups the events of one domain operation (see below).
- **`causes`** — reserved, null in V1: the `id` of a specific prior event a
  supersession/retraction targets, for when entity-level targeting is too
  coarse.
- **`ts`** — always assigned by the writer goroutine, never trusted from
  clients (pre-empts LAN clock-skew entirely), kept monotone with `seq`.

```sql
CREATE TABLE events (
  seq    INTEGER PRIMARY KEY,   -- position in the log
  id     TEXT UNIQUE NOT NULL,  -- ULID
  origin TEXT NOT NULL,         -- workspace UUID (see log.initialized)
  batch  TEXT NOT NULL,
  causes TEXT,                  -- id of a targeted prior event; usually NULL
  ts     TEXT NOT NULL,         -- writer-assigned, monotone with seq
  actor  TEXT NOT NULL,
  type   TEXT NOT NULL,
  v      INTEGER NOT NULL,      -- payload schema version
  entity TEXT,
  data   TEXT NOT NULL          -- canonicalized JSON payload
);
-- + triggers raising on UPDATE/DELETE: append-only is enforced, not promised
CREATE INDEX ev_entity ON events(entity);
CREATE INDEX ev_type   ON events(type);
CREATE INDEX ev_batch  ON events(batch);
CREATE INDEX ev_actor  ON events(actor);
CREATE INDEX ev_ts     ON events(ts);

-- derived, rebuildable (churn reindex); populated in the same transaction:
CREATE TABLE event_refs (event_seq INTEGER, entity_id TEXT, role TEXT);
CREATE INDEX er_entity ON event_refs(entity_id);
```

**Batches are transactions.** Domain operations are often multi-event: a
transition plus its allocations, a bulk edit of 400 rows. All events of one
operation share a `batch` id (for grouping in history views and in the
export) and are inserted in **one SQLite transaction** — a crash can never
leave half an operation on disk, so the append-time invariants below can
never be observed half-applied. No framing protocol, no commit markers, no
tail-repair code: atomicity is delegated to the storage engine, which is the
one component here that has been tested by decades of other people's crashes.

**Envelope/payload split — where the database answers queries.** Everything
the store is ever *asked about* is a real, indexed column (`entity`, `type`,
`batch`, `actor`, `ts`): the entire history API runs at the database level
for years of log. Events that touch several entities (a dependency's two
things; an allocation's thing, resource, and requirement) are covered by
**`event_refs`** — a derived, rebuildable side table populated in the same
transaction, one row per (event, referenced entity, role). It is an index,
not a truth: `churn reindex` reconstructs it from `events` at any time, so
per-entity history stays indexed without the envelope needing more than one
`entity` column. Payloads stay opaque JSON — normalizing them into per-type
tables would break the uniform envelope that replay, export, and merge all
depend on, and would reintroduce migrations. If a specific payload field
ever becomes a hot history query ("all transitions into state X"), SQLite
generated columns index a JSON path (`data->>'state'`) without touching
stored bytes or the envelope contract — a per-query escape hatch, not a
schema commitment.

**Current-state lookup stays in the projection — the one-query-engine
rule.** At this scale the in-memory projection *is* the index (map lookups,
no round-trips), but the binding reason is correctness: the *meaning* of the
data — state semantics, composite rollup, readiness-as-matching — lives in
the fold and cannot be expressed in SQL. A second query path against raw
events would return plausible, subtly wrong answers (a SQL "ready" is
exactly the greedy check review finding I2 killed). One store, one fold, one
place where meaning is computed. Metadata filtering is an in-projection scan;
a metadata key queried routinely is the §5.3 signal to graduate it into the
vocabulary, not into an index. Sanctioned exception (phase 4
reporting/BI): *derived* projection tables written by the fold itself —
rebuildable caches downstream of the one brain, never a parallel
interpretation of the log.

**Event catalog** (typed domain events — each names an intention, validates
its payload, and reads as history):

```
                       ── log ──
log.initialized                               immutable workspace_id + first writer lineage — always seq 1
writer.started                                new writer lineage (origin) after restore/clone

                       ── vocabulary (§5.3) ──
state.defined / .superseded / .retracted      name, semantic, color, description
type.defined / .superseded / .retracted       name, color, description, metadata field shapes (§5.3)
resourcetype.defined / .superseded / .retracted   name, color, description, metadata field shapes (§5.3)
capability.defined / .superseded / .retracted name, description

                       ── domain ──
project.created / .superseded / .retracted
thing.created / .superseded / .retracted    name, type→id, parent, metadata
thing.state_changed                         state→id
dependency.asserted / .retracted            thing → thing, on_abandoned policy
requirement.asserted / .superseded / .retracted   quantity × (capability ids | pinned resource)
resource.created / .superseded / .retracted name, kind, named, capacity, type→id (optional), metadata
resource.availability_changed               available, note
capability.granted / .revoked               resource, capability→id
allocation.opened / allocation.closed       thing, resource, quantity, requirement→id
```

Three verbs cover all change without ever rewriting a byte:
**assert/create** (new fact), **supersede** (new version of an attribute —
projection keeps the latest, history keeps them all), **retract** (tombstone —
the entity stops existing *now*; that it existed remains recorded). Each event
carries its payload schema version (`v`) so the replay code can interpret old
events forever. "Permissive" has a precise boundary: unknown **payload
fields** are tolerated, but an unknown **event type** or unsupported `v`
fails closed — a read-write server halts rather than fold a
plausible-but-wrong projection.

Precision rules that keep the verbs unambiguous:

- **Everything addressable has a stable id, never reused** — including the
  relationship entities: dependencies (`dep_…`), requirements (`req_…`),
  allocations (`al_…`). Retract and close always target an exact id, never a
  pattern — "retract the dependency between A and B" is the UI's phrasing,
  `dependency.retracted {dep_31}` is the fact.
- **Supersession is full replacement** of the entity's mutable attribute
  set, never a patch. Removing a metadata key = superseding without it. One
  rule, no merge ambiguity; the UI always composes and submits the complete
  new version.
- **Preconditions are commands, not facts.** The batch *command* carries
  `expected_versions: {entity_id: seq}` for every entity it **writes**,
  checked against the pre-batch projection (an entity's version = the `seq`
  of the last event that touched it). Entities merely *read* during
  validation need no version guard — cross-entity feasibility is re-checked
  inside the writer's critical section regardless. None of this is
  persisted: the log records what happened, never what was assumed, and
  replay needs no guards because it replays only what already passed them.

**Considered and rejected:**
- *Raw EAV / datom-style facts* (`entity·attribute·value·tx±`) — maximally
  general, but typed domain events preserve *intention* (`state_changed` vs.
  an anonymous attribute write), validate at the edge, and read as a
  narrative. Generality we'd pay for daily and use never.
- *JSONL segment files as the medium* (the original beads-inspired choice) —
  plain-text truth is attractive, but it hand-rolls what SQLite provides
  battle-tested: batch atomicity (framing protocols, commit markers),
  crash safety (fsync discipline, tail-tear repair), single-writer locking,
  and cross-file continuity verification. **Append-only is a discipline, not
  a file format** — the discipline is kept (INSERT-only, trigger-enforced)
  and the text form survives as the canonical export (§5.4).

Invariants enforced in the domain core before an event batch is appended
(the log stays fact-only; invalid facts are rejected, never recorded):

- dependency graph stays acyclic **on the expanded leaf graph** (composite
  endpoints expanded, inherited edges included — §2.1); rejection shows the
  offending expanded cycle;
- containment stays a tree; no cross-project parenting;
- composites (things with children) carry no requirements and no own state;
- a transition into an active state only commits with a feasible allocation
  set, appended atomically in the same batch;
- state names in `thing.state_changed` must be defined; a state in use
  cannot be retracted, and **a state's semantic is immutable while any thing
  is in it** (name/color/description supersede freely) — rebinding a semantic
  under live things would silently break the active⇔allocations invariant;
- **retraction is rejected while inbound references exist** (dependency
  edges, pins, children, open allocations); the error enumerates them, like
  the cycle error. The UI offers cascade-as-a-batch ("retract thing + its N
  edges + subtree") committed atomically — the fold itself never cascades
  implicitly;
- parenting a child under a leaf that is not pending-semantic or that carries
  requirements is rejected (promotion rules, §2.1);
- pins reference `named` resources, which cannot lose the flag while pinned
  (§2.4); `named = true` enforces `capacity = 1`, and a pinned requirement's
  quantity is 1 (a pin names one unit — a quantity would be unsatisfiable by
  construction);
- the first event of any log is `log.initialized` (immutable
  `workspace_id`); `origin` identifies the current **writer lineage**,
  renewed by `writer.started` when a restored or cloned directory resumes
  writing.

### 5.3 The vocabulary — how the user-defined ontology is stored

The ontology the user builds — **states, thing types, resource types,
capability tags** — is not a schema file, a config, or a second store. It is **entities in the same
event log**, created and evolved with the same three verbs
(define/supersede/retract) as everything else, through the same writer.

Rules that keep vocabulary and data consistent **by construction**:

- **Declared before use.** A `thing.created` referencing an undefined type, a
  requirement referencing an undefined capability, a `state_changed` to an
  undefined state — all rejected at append time, by the same invariant
  machinery as cycles. A typo'd capability (`data-anaylsis`) cannot silently break
  matching, because it can never enter the log.
- **References are by id; names are display facts.** Domain events reference
  vocabulary entities by stable id (`st_…`, `ty_…`, `rt_…`, `cap_…`); the name,
  color, and description live on the vocabulary entity and supersede freely.
  Renaming `awaiting-approval` to `approval-pending` is one ordinary supersession — no mass
  rewrite, no name-resolution-as-of-time-T subtleties during replay, and no
  historical event ever changes meaning because a name moved.
- **Retraction blocked while referenced; semantics immutable while occupied**
  — the same uniform rules as §5.2. A vocabulary entry disappears only when
  nothing points at it, and even then its definition remains in history.
- **Metadata stays free-form — deliberately.** Metadata keys/values are the
  schema-free zone nothing computes off; declaring them would be ceremony
  with no consistency payoff. If a metadata key ever earns computed meaning,
  that is the signal it should graduate into the vocabulary as a declared
  concept — via ordinary events, no migration. One UI affordance sits on
  top without changing this: a thing type or resource type may **declare
  metadata field shapes** (key, label, kind — text/number/date/select —
  options, a required hint) so editor forms can offer proper inputs instead
  of raw JSON. The declarations live on the type entities and supersede like
  any attribute; the engine still computes nothing off metadata, and
  instance metadata is **never validated against them** — the log stays
  permissive, "required" is a form hint, and a non-conforming document is a
  fact like any other. Declaration is UI affordance, not schema enforcement.

Consistency between ontology and data is therefore **structural, not
reconciled**: one log, one writer, one fold. Vocabulary events and domain
events travel through the same serialized validate→append→apply step and the
same atomic batches. There is no second store that *could* drift.

### 5.4 File layout, backup, and the JSONL view

```
workspace/
  workspace.db        # the event log — one INSERT-only table (SQLite, WAL)
  workspace.db-wal    # SQLite write-ahead log (transient, managed by SQLite)
```

- **One file, one process, one projection.** SQLite serializes competing
  writers — it does **not** keep a second application instance out, and two
  instances would each validate against their own in-memory projection
  before committing individually-valid transactions that jointly break
  domain invariants. The single-projection assumption is therefore guarded
  explicitly: the server holds an **OS-level exclusive lock** on
  `churn.lock` in the data directory for its entire lifetime and refuses
  startup if it is held (Windows share-mode exclusivity; `flock` elsewhere).
  Division of labor: SQLite owns transaction safety, the lock owns process
  exclusivity. The segment continuity markers and hash chains of the earlier
  JSONL design remain deleted responsibilities.
- **Backup** = `churn backup <dest>` (also exposed in the UI), using
  SQLite's online-backup API: a consistent snapshot taken while the server
  runs, no shutdown needed. Hand-copying the `.db` mid-write is the one
  operational "don't" — the backup command exists so nobody has to.
- **The JSONL view.** `churn export-log` (and
  `GET /api/v1/history?format=jsonl`) streams the table as canonical JSONL —
  one envelope per line, ordered by `seq`. Byte-stability is by
  construction: payload JSON is **canonicalized at write time** (sorted
  keys, minimal whitespace), so export never re-serializes, it only copies.
  This is the grep/diff/git/archive format and the natural interchange
  format for any future merge protocol.
- **Restore and escape hatch.** `churn import-log` replays a JSONL stream
  into an **empty** data directory only — and it does not merely check
  envelope hygiene (`seq` continuity, `id` uniqueness, first-event and
  origin rules, monotone timestamps): every batch runs through **the same
  fold and validation as live writes** — payload schemas, references, domain
  invariants — because a permissive restore would launder a corrupt log into
  a plausible projection. All-or-nothing; any violation aborts with nothing
  written. Resuming writing afterwards appends `writer.started` (fresh
  origin). This is the restore path, and the guarantee that the SQLite file
  is a *medium*, never a lock-in: the design's truth remains the sequence of
  events, expressible as plain text at any moment.

---

## 6. Evolution path

| Phase | Delivers |
|---|---|
| **1 — Core** | Event log + projection (INSERT-only table, batch transactions, JSONL export/import), domain core, vocabulary registry with default states, CRUD UI with atomic bulk editing, derived statuses, ready list, blocked-by, graph view. *Already useful daily.* |
| **2 — Resources live** | Active-state transitions with allocation, resource board, contention & bottleneck dashboard, recommendations, availability toggle, per-entity history timelines. |
| **3 — Team** | Bind to LAN, lightweight login backed by **server-side sessions** — `actor` is stamped by the server from the session, never supplied by the client, or attribution is fiction. `Host`/`Origin` validation, JSON-only content types, CSRF token on mutations (browser-origin hygiene, not a permissions system). SSE live refresh, time-travel viewer (as-of slider over the log). Concurrency (`expect`) is already in the log protocol from phase 1. |
| **4 — Options** | Consumable resources (stock, decrement, shortage warnings); optional durations → real critical path & schedule suggestions; resource calendars; if distributed operation is ever needed, log merging — stated honestly: **the envelope preserves the inputs a merge protocol would require** (globally unique ids, true origin identity, `causes`), but ordering and conflict semantics for concurrent supersessions and competing allocations of one shared resource are real protocol design work, deliberately deferred, not something append-only-ness solves by itself. Everything else slots into a reserved seam — new event types, resource `kind`, the recommendation engine — with no migrations of meaning. |

### Non-goals (explicit)

- No ambition to replace broad business systems such as accounting, procurement,
  billing, payroll, or document management.
- No automatic scheduling/optimization in V1 — the tool informs; the team decides.
- No cloud, no external network dependencies, ever — closed environment.

---

## 7. Assumptions to validate during Phase 1

1. AND-only capability matching (no OR-alternatives) is expressive enough.
2. Abandoned-dependency default ("unblock with warning") matches reality.
3. The rollup rule (composites never worked directly) doesn't fight how the
   team actually talks about workstreams — the "add a final-review child step"
   pattern must feel natural in the UI (offer it as a one-click affordance).
4. One shared workspace log (all projects, one data dir) vs. log-per-project —
   the shared resource pool argues strongly for one log; revisit only if
   archival of finished projects becomes noisy.
5. **The five semantics are bundles of behaviors** (holds allocations?
   satisfies dependents? ready-eligible?) and the bundles might prove too
   coarse — e.g. an "awaiting-approval" state that should keep a dedicated
   workspace allocated but release the people. The designed escape hatch: decompose
   semantics into orthogonal behavior flags, with the five semantics becoming
   named presets. Don't build that until a real state demands it.
6. Event-type vocabulary is stable enough that permissive versioned readers
   (each event carries a schema version) can interpret the log forever —
   immutability means replay code accretes compatibility, never migrations.
