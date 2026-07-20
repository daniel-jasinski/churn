// ui/helpContent.ts — ALL in-app help copy in one place, written for a user
// who has never read the design document. Structure per topic: Purpose (one
// paragraph), How to use (bullets), Components & what they mean (term →
// definition). Keep it plain: no section numbers, no internal jargon.

export interface HelpTopic {
  title: string;
  purpose: string;
  how: string[];
  components: [string, string][];
}

export const HELP = {
  ready: {
    title: 'Ready board',
    purpose: 'The daily driver: four live columns — what can start right now, what waits for resources, what is in flight, what recently finished — plus an "Almost ready" strip underneath. Everything is computed from dependencies, states, and resource availability; nothing is curated by hand. Each column header has its own ? with the details of that column.',
    how: [
      'Filter by type, capability, or name; press / to jump to the name filter. The project is the sidebar’s job: this screen spans every project, and a project’s own Board tab is the same board scoped to it.',
      '"+ New thing" creates a single work item; "Bulk add" commits a whole table of items and their dependencies in one atomic step.',
      'Cards move between columns on their own as dependencies finish and resources free — you only record state changes; the sorting into columns is never done by hand.',
    ],
    components: [
      ['Card', 'one work item: name, type badge, project link, and the actions its current state allows. Only leaf items appear — containers roll up from their children and are never worked directly.'],
      ['Deps', 'a shortcut into the item’s editor with the dependency section focused — the quickest way to rewire what waits for what.'],
      ['Badges', '⚠ a prerequisite was abandoned but this was allowed to proceed · ⁉ finished yet has unsatisfied prerequisites (worth a look) · ▲ holds more of a resource than is currently available · ↻ its allocations no longer match its edited requirements (use Re-propose).'],
    ],
  },
  readyList: {
    title: 'Ready column',
    purpose: 'Things that can be started this moment: every prerequisite is satisfied AND the resources they require are free right now. Sorted by the recommendation score, highest first.',
    how: [
      'Start: the tool proposes which concrete resources would satisfy the requirements; nothing is committed until you confirm. If someone took a resource in between, you simply review a fresh proposal.',
      'Click a score to expand exactly how it was computed, term by term; tune the weights on the Settings tab.',
      'An empty column tells you why it is empty — how much is dependency-blocked vs. resource-blocked — and points at the bottleneck dashboard.',
    ],
    components: [
      ['Score', 'a transparent ranking aid, not an order you must obey: it adds up how much this unlocks, how much waits on it downstream, chain depth, and how long it has starved for resources, minus a penalty for hogging contended resources.'],
      ['Requirement chips', 'what the item will check out when started: "2× editing" means two units carrying the editing capability; a name means that specific pinned resource.'],
      ['Start / Edit / Deps', 'begin work (via a confirmable resource proposal), open the full editor, or jump straight to the dependency section.'],
    ],
  },
  resourceBlocked: {
    title: 'Resource-blocked column',
    purpose: 'Prerequisites are all done — the only missing ingredient is capacity: some required resource is currently busy or unavailable.',
    how: [
      'No action is needed here: a card flips to Ready by itself the moment capacity frees.',
      'To make that happen sooner, open the Bottlenecks tab — it shows which capability combinations are short and what is holding them.',
    ],
    components: [
      ['Requirement chips', 'the needs that cannot be satisfied right now — the reason the item sits here.'],
      ['Starvation credit', 'the longer an item waits here, the more recommendation-score credit it accrues, so long-waiting work gets first claim on the unit that finally frees.'],
    ],
  },
  inProgress: {
    title: 'In progress column',
    purpose: 'Work that is active right now — holding its resource allocations — plus deliberately paused ("held") work, which holds nothing.',
    how: [
      'Finish releases the resources and unblocks dependents; Pause releases them too but keeps dependents blocked; Abandon ends the work without success.',
      'A held card says whether it could resume right now — whether the resources it needs are free at this moment.',
      'Re-propose appears when requirements were edited mid-flight (↻ badge): it swaps the outdated allocations for a fresh feasible set in one atomic step — the work never stops holding what it needs.',
    ],
    components: [
      ['Working (blue)', 'active and holding concrete resource allocations, visible on the resource board.'],
      ['Held (purple)', 'deliberately on hold: holds NO resources, dependents stay blocked, excluded from ready lists.'],
      ['▲ badge', 'holding more of a resource than is currently usable — normal after capacity drops mid-work; pausing is suggested, never forced.'],
    ],
  },
  recentlyDone: {
    title: 'Recently done column',
    purpose: 'The most recently finished work, newest first — a lightweight "what just happened" feed (the last 15 items).',
    how: [
      'Reopen puts a finished item back to pending if it turns out not to be done after all — recorded as an ordinary state change; history keeps everything.',
      'For the complete record beyond 15 items, use the History tab or any item’s "hist" link.',
    ],
    components: [
      ['⁉ badge', 'finished, yet has unsatisfied prerequisites — usually a sign it was closed out of order and worth a second look.'],
    ],
  },
  almostReady: {
    title: 'Almost ready',
    purpose: 'Pending things whose remaining blockers number at most N — the near-term pipeline. Watch this strip to see what to clear next so new work keeps becoming available.',
    how: [
      'Widen or narrow the horizon with the "blockers ≤" control.',
      'Each blocker is listed with its own live status; click one to open its history.',
      'A dropped (abandoned) blocker never resolves on its own — someone must decide: redo that work, or remove the dependency.',
    ],
    components: [
      ['waiting on N', 'how many blockers stand between this item and Ready.'],
      ['Blocker list', 'only the NEAREST blockers — what must resolve next, not every transitive prerequisite behind them.'],
      ['Container blockers', 'a dependency you declared on a container shows as that container with its rolled-up status, not as its individual children.'],
    ],
  },
  graph: {
    title: 'Graph view',
    purpose: 'The dependency picture of one project: every thing as a node, every "must wait for" as an arrow, laid out left to right in work order. Colors are live statuses, so the frontier of possible work is visible at a glance.',
    how: [
      'Click a node for details and actions; hover to highlight everything upstream and downstream of it.',
      'Double-click a container (composite) to open or collapse it; collapsed containers show a progress ring.',
      'Add dependency: click the DEPENDENT thing first (the one that must wait), then the thing it waits for. Escape cancels.',
      'Click an arrow to inspect or remove that dependency.',
      'Use "past…" to view the whole graph as it stood at any earlier moment (read-only).',
    ],
    components: [
      ['Green (ready)', 'can start now — prerequisites done, resources free.'],
      ['Amber (resource-blocked)', 'prerequisites done, waiting on busy or unavailable resources.'],
      ['Grey (blocked)', 'waiting on unfinished prerequisites.'],
      ['Blue (working)', 'currently active and holding resources.'],
      ['Dim grey (finished) / purple dashed (held) / dark red (dropped)', 'terminal success, deliberately paused, and abandoned respectively.'],
      ['Compound boxes / ring nodes', 'containers of child work. A container is never worked directly: its state and progress roll up from its children.'],
      ['Solid arrows', 'dependencies you declared. Dotted arrows are implied ones: an edge to or from a container silently binds every current and future child inside it.'],
      ['Orange dashed arrow', 'satisfied only because an abandoned prerequisite was tolerated ("unblock + warn" policy).'],
      ['On-abandon policy', 'each edge chooses what happens if its prerequisite is abandoned: "unblock + warn" lets the dependent proceed with a warning badge (default); "stay blocked" holds it until the work is redone or the edge removed.'],
    ],
  },
  projects: {
    title: 'Projects',
    purpose: 'Projects partition the workspace for display and filtering: every thing lives in exactly one project. Resources are NOT per-project — they are shared across the whole workspace, which is exactly how cross-project contention becomes visible.',
    how: [
      'Pick a project to open its workbench — the graph, board and tree are three arrangements of that project’s things, and switching project keeps whichever one you are on.',
      'Create with +. Rename and retract live in the project’s own dialog, behind "Edit project" in the workbench header.',
      'Renaming never breaks anything: everything references projects by a stable internal id, not by name.',
    ],
    components: [
      ['Count', 'how many leaves in the project are ready to start right now — the number worth acting on. Blank means none are.'],
      ['Hairline', 'finished leaves out of all non-abandoned leaves, as a progress rule under the name.'],
    ],
  },
  resources: {
    title: 'Resource board',
    purpose: 'Everything work is done WITH — people, pools, rooms, instruments — shared by all projects. This board shows who or what is busy, with what, and who is waiting.',
    how: [
      'Create resources as pools (interchangeable units) or named (a specific person/machine that requirements can pin). The ? on the dialog explains how to choose.',
      'Grant capability tags — they are what requirements match on. Click a tag chip to revoke it.',
      'Toggle availability with a note ("maintenance", "on leave"). Marking a resource unavailable never kicks off current work — it just stops new starts and flags the overage.',
      'Filter by resource type; types are labels and colors only.',
    ],
    components: [
      ['Capacity bar', 'blue = units allocated to active work, green backdrop = units usable right now, full width = declared capacity.'],
      ['used / effective / cap', 'allocated units, units currently usable (0 while unavailable), and declared capacity.'],
      ['▲ over-allocated', 'more units are allocated than are currently usable — normal after capacity drops or availability goes off mid-work. The affected work is flagged too; pausing it is suggested, never forced.'],
      ['Open allocations', 'the active things holding units of this resource right now.'],
      ['Waiting for it', 'ready or resource-blocked things with at least one requirement this resource could satisfy — the queue that forms behind it.'],
      ['Capabilities vs type', 'capabilities decide who CAN do work (requirements match on them); the type just labels and colors the resource.'],
    ],
  },
  poolVsNamed: {
    title: 'Pool or named?',
    purpose: 'Two ways to model a resource, and the choice decides what the tool can tell you later.',
    how: [
      'Pick NAMED for individuals: people or machines that differ in skills, or where you care which one did the work. Give each their capability tags — interchangeability then emerges wherever tags overlap, and requirements can pin exactly that resource.',
      'Pick POOL for genuinely interchangeable units you never track individually ("4 licenses", "3 loaner laptops"): one row with capacity N and one shared tag set.',
      'Rule of thumb: if you might ever ask "which one?", use named resources with shared tags instead of a pool.',
    ],
    components: [
      ['Named resource', 'capacity fixed at 1, can be pinned by a requirement ("specifically Maria"), carries its own tag set and work history.'],
      ['Pool', 'capacity N interchangeable units; every unit carries the pool’s tags; requirements can never single out one unit.'],
    ],
  },
  contention: {
    title: 'Resource contention',
    purpose: 'How much open demand for resources cannot be satisfied right now — and which capability combinations are short. This is where "we are waiting on people/equipment" becomes a number.',
    how: [
      'Start at "unmet requirement units" — the one figure computed rigorously enough to base decisions on.',
      'Use the signature table to see WHICH capability combinations are short and which things want them.',
      'Treat the per-capability ratios as an at-a-glance heuristic only — expand them when you want a rough single-tag view.',
    ],
    components: [
      ['Unmet requirement units (trustworthy)', 'how many required units of demand cannot fit onto free resources right now, computed by actually trying every assignment. This is the number to act on.'],
      ['Per-signature split (indicative)', 'the same shortfall attributed to specific capability combinations. The split depends on assignment tie-breaks, so treat it as a strong hint, not gospel — the total above is the honest figure.'],
      ['Per-capability ratios (rough)', 'naive demand/supply per single tag. Double-counts multi-skilled resources and ignores combinations — at-a-glance only.'],
      ['Pressure', 'demand divided by matchable supply for that signature — above 1 means the signature is oversubscribed.'],
    ],
  },
  criticality: {
    title: 'Critical things',
    purpose: 'Structurally important unfinished work: what gates the most work downstream, what unlocks the most immediately, and what sits on the longest remaining chain.',
    how: [
      'Click a column header to rank by it — the three numbers answer different questions and are never added together.',
      'High reach + low unlock means finishing it helps eventually but frees nothing today (its dependents have other blockers too); high unlock is the quick win.',
    ],
    components: [
      ['Downstream reach', 'everything that can never finish while this is unfinished — long-term weight.'],
      ['Immediate unlock', 'how many things become startable the moment this finishes — short-term payoff. Reach does NOT imply unlock, which is why the numbers stay separate.'],
      ['Remaining depth', 'the longest chain of unfinished steps running through it — a schedule-length signal without time estimates.'],
    ],
  },
  starvation: {
    title: 'Starvation',
    purpose: 'Work that has been waiting on resources the longest. Long-starved work automatically accrues scoring credit, so it claims freed capacity first instead of being starved forever by newer, flashier items.',
    how: [
      'Use the list to spot chronic waiters — repeated long stints on the same items usually mean a capability is undersupplied (check Resource contention above).',
      'No action is required for the credit itself; it is applied to the recommendation score automatically.',
    ],
    components: [
      ['Current stint', 'how long the item has been continuously resource-blocked right now ("—" means it is not blocked at this moment).'],
      ['Cumulative credit', 'total time waited since the item last held resources. It survives brief flips to ready, and boosts the recommendation score so long-starved work gets first claim on the unit that finally frees.'],
    ],
  },
  tree: {
    title: 'Hierarchy & progress',
    purpose: 'The containment structure: which work lives inside which, with progress rolled up at every level, plus a treemap for proportions at a glance.',
    how: [
      'Expand and collapse branches; click a name to edit it; "hist" opens its full history.',
      'Containers are never worked directly — hands-on work always happens at the leaves; containers compute their state and progress from them.',
      'The treemap sizes each top-level branch by how many leaves it contains and colors it by completion.',
    ],
    components: [
      ['Progress bar (3/5)', 'finished leaves over all non-abandoned leaves in the subtree.'],
      ['—', 'every leaf in the subtree was abandoned: there is nothing left to measure, so no percentage is shown (never a fake 100%).'],
      ['✕ badge', 'the subtree contains abandoned work.'],
      ['Status dots', 'the same live statuses as everywhere else: green ready, amber resource-blocked, grey blocked, blue working, purple held, dim finished, red dropped.'],
    ],
  },
  states: {
    title: 'States',
    purpose: 'Your names for work situations ("queued", "awaiting sign-off"). The engine has no built-in state names — it only understands the five behaviors below, and every rule in the tool reads only the behavior a state is bound to.',
    how: [
      'Define as many states as you like; each binds to exactly ONE behavior. Rename and recolor freely — history never changes meaning, because everything references states by stable id.',
      'A state’s behavior is locked while anything is in that state (🔒 shows the count) — move the things out first; name and color stay editable.',
      'Deleting a state is refused while something still references it — the error lists exactly what.',
    ],
    components: [
      ['pending', 'not started. Eligible for the ready board once its prerequisites are satisfied.'],
      ['active', 'being worked right now. Entering an active state checks out the resources it needs; leaving releases them.'],
      ['paused', 'deliberately on hold: holds NO resources, dependents stay blocked, excluded from ready lists.'],
      ['satisfied', 'done, successfully. Unblocks dependents and counts toward progress.'],
      ['abandoned', 'ended without success. Each dependency edge decides whether its dependent may proceed anyway (with a warning) or stays blocked.'],
    ],
  },
  thingTypes: {
    title: 'Thing types',
    purpose: 'Labels and colors for work items (task, review, deliverable…). Types are for filtering and reporting only — the engine attaches no meaning to them. Their one superpower: declared metadata fields, which turn an item’s free-form metadata into a proper form.',
    how: [
      'Rename and recolor freely — things reference their type by stable id, so nothing breaks.',
      'Declare metadata fields to give every item of this type real form inputs (text/number/date/choice) in its editor. Fields shape the form only; nothing is ever validated against them.',
      'Deleting a type is refused while any thing still uses it.',
    ],
    components: [
      ['Color', 'the chip color shown on cards, in the graph, and in the tree.'],
      ['Metadata fields', 'per-type form declarations: key, label, input kind, choices for a choice list, and a soft "required" hint.'],
    ],
  },
  resourceTypes: {
    title: 'Resource types',
    purpose: 'Labels and colors for resources (person, room, tool…) — the resource-side twin of thing types. Display and filtering only: a resource type never affects which work a resource can satisfy.',
    how: [
      'Use types to group the resource board visually and filter it; declare metadata fields to get proper form inputs in the resource editor.',
      'What a resource can actually DO is decided by its capability tags, not its type — grant those on the resource board.',
      'Deleting a resource type is refused while any resource still uses it.',
    ],
    components: [
      ['Color', 'the chip color shown on the resource board.'],
      ['Metadata fields', 'per-type form declarations for resource editors — form-driving only, never validated.'],
      ['Type vs. capability', 'the type says what a resource IS (label); capabilities say what it CAN DO (matching).'],
    ],
  },
  capabilities: {
    title: 'Capabilities',
    purpose: 'The tags that actually matter for matching: requirements ask for them, resources carry them. Capabilities decide who CAN do work — unlike types, which only label and color.',
    how: [
      'Keep tags meaningful and shared ("editing", "approval") — a requirement matches only resources that carry ALL its tags at once.',
      'Tags must be declared here (or inline via "+ new capability…") before use, so a typo can never silently fail to match.',
      'Grant and revoke tags on resources from the resource board; deleting a capability is refused while any requirement or resource still references it.',
    ],
    components: [
      ['On a requirement', '"1× editing+approval" = one unit of something carrying both tags at once.'],
      ['On a resource', 'the set of things it can do; a pool’s tags apply to every unit in it.'],
    ],
  },
  history: {
    title: 'History',
    purpose: 'The append-only log the whole workspace is computed from. Nothing is ever edited or deleted in place — every change is a new recorded event, so this page is a complete, trustworthy audit trail.',
    how: [
      'The workspace view shows recent activity, newest first, grouped into batches; per-entity views show one thing’s full story.',
      'Names shown are today’s names — events reference stable ids, so renames never rewrite history.',
      'One batch = one atomic operation: either all of its events happened, or none did.',
    ],
    components: [
      ['created / defined / asserted', 'a new fact: a thing, a vocabulary entry, a dependency, a requirement.'],
      ['superseded', 'a new version of something replaced the old one in full. The old version stays in history.'],
      ['retracted', 'something stopped existing from that moment on — but that it existed, and everything it did, remains recorded.'],
      ['state changes & allocations', 'the work story: transitions between states, and resources being checked out ("allocated") and released ("closed").'],
      ['batch / seq', 'the atomic group an event belongs to, and its position in the log.'],
    ],
  },
  // ── dialog topics (opened from the "?" in a dialog's title bar) ──
  thingEditor: {
    title: 'Editing a thing',
    purpose: 'One dialog for everything about a work item: what it is, where it lives, what it needs, and what it waits for. Saving commits all changes in one atomic step — either everything applies or nothing does.',
    how: [
      'Name, type and project identify it; the type also decides which metadata form fields appear.',
      'Parent puts this inside another item. The container stops being workable itself: its state and progress are computed from what is inside. Parenting under an already-worked item offers to move that work onto a child step first.',
      'Requirements say what it needs WHILE being worked; dependencies say what must finish BEFORE it can start.',
    ],
    components: [
      ['Metadata (declared)', 'proper form fields declared by the selected type — text, number, date or a choice list. A * means "expected", as a hint; saving is never blocked.'],
      ['Additional fields', 'free-form key/value pairs for anything the type does not declare. Values that parse as JSON are stored typed; everything else as text.'],
      ['Requirement — quantity × capabilities', 'needs that many units of someone/something that has ALL the selected tags at once.'],
      ['Requirement — pin', 'needs exactly this one named resource, nothing else will do.'],
      ['Dependency policy', 'if the other item gets cancelled: "stay blocked" = keep waiting until it is redone or the edge is removed; "unblock + warn" = proceed anyway, with a warning badge.'],
      ['Required by (read-only)', 'items that wait for this one — edit those from their own editors.'],
    ],
  },
  notes: {
    title: 'Notes',
    purpose: 'Free-text comments attached to a thing — a running commentary: why it stalled, what a reviewer asked for, a decision and its reason. Notes are plain annotations; they never change what is ready, blocked, or scheduled.',
    how: [
      'Type in the box and "Add note" to post. Unlike the rest of the editor, each note commits immediately — it does not wait for Save, and Cancel does not undo it.',
      'Edit or delete your own wording freely; the original author and post time are kept, and an edit is timestamped.',
      'Every note also appears in the thing’s History, interleaved with its other events, so the full story stays in one timeline.',
    ],
    components: [
      ['Author · time', 'who posted the note and when — stamped by the server from the acting user, not typed.'],
      ['edited …', 'shown once a note has been revised; the note keeps its original author and creation time.'],
      ['Posts immediately', 'notes are their own facts in the log, so they are saved the moment you add them — deleting the thing later requires clearing its notes first.'],
    ],
  },
  resourceEditor: {
    title: 'Editing a resource',
    purpose: 'A resource is something work is done WITH — a person, a pool of interchangeable units, a room, an instrument. It is shared by every project in the workspace.',
    how: [
      'Choose the shape first: pool (capacity N interchangeable units) or named (one specific unit that requirements can pin) — the ? next to the shape field explains how to choose.',
      'The type is a label and color only. What the resource can actually DO is its capability tags, granted on the resource board.',
      'Metadata fields declared by the resource type appear as proper inputs; anything else goes under Additional fields.',
    ],
    components: [
      ['Capacity', 'how many units the pool holds; a named resource is always exactly one unit.'],
      ['Type', 'labels and colors the resource for filtering — no effect on matching.'],
      ['Capabilities (granted on the board)', 'the tags requirements match on — they decide which work this resource can satisfy.'],
    ],
  },
  projectEditor: {
    title: 'Projects',
    purpose: 'A project scopes a dependency graph for display and filtering; every thing lives in exactly one. Resources stay shared across the whole workspace.',
    how: [
      'Rename freely — nothing references the name, only a stable internal id.',
      'A project can only be removed once nothing lives in it.',
    ],
    components: [
      ['Name', 'display only; changing it never breaks references or history.'],
    ],
  },
  stateEditor: {
    title: 'States & behaviors',
    purpose: 'A state is your name for a situation ("queued", "awaiting sign-off"); the BEHAVIOR you bind it to is what the engine acts on. Five behaviors exist, and every rule in the tool reads only them.',
    how: [
      'Pick the behavior that matches what the state means; name, color and description are free-form and always editable.',
      'The behavior locks while anything is in the state — rebinding it under live work would silently change what the tool does with that work.',
    ],
    components: [
      ['pending', 'not started; appears on the ready board once prerequisites are done.'],
      ['active', 'being worked; entering checks the needed resources out, leaving releases them.'],
      ['paused', 'deliberately on hold; holds no resources, keeps dependents blocked.'],
      ['satisfied', 'done; unblocks dependents, counts toward progress.'],
      ['abandoned', 'ended without success; each dependency edge decides whether dependents proceed (with a warning) or stay blocked.'],
    ],
  },
  typeEditor: {
    title: 'Types & metadata fields',
    purpose: 'Thing types label work items; resource types label resources. Both are colors and filters only — the engine attaches no meaning to them. Their one superpower: declared metadata fields, which turn the free-form metadata section into a proper form.',
    how: [
      'Declare a field per metadata key you want a proper input for; reorder with ↑↓. Instances are never validated against declarations — existing data always stays visible and editable.',
      'Removing all fields (or a field) just changes the form; values already stored keep living under Additional fields.',
    ],
    components: [
      ['text', 'a plain text input; stores text.'],
      ['number', 'a numeric input; stores a real number, so it sorts and compares properly.'],
      ['date', 'a date picker; stores yyyy-mm-dd.'],
      ['select', 'a dropdown of the choices you list; stores the chosen text.'],
      ['required *', 'a soft expectation marker on the form — saving is never blocked by it.'],
    ],
  },
  capabilityEditor: {
    title: 'Capabilities',
    purpose: 'Capability tags are the matching currency: requirements ask for them, resources carry them. They decide who CAN do work — unlike types, which only label and color.',
    how: [
      'Keep tags meaningful and shared ("editing", "approval") — a requirement matches resources that carry ALL its tags at once.',
      'Tags must be declared here (or inline via "+ new capability…") before use, so a typo can never silently fail to match.',
    ],
    components: [
      ['On a requirement', '"1× editing+approval" = one unit of something carrying both tags.'],
      ['On a resource', 'the set of things it can do; a pool’s tags apply to every unit in it.'],
    ],
  },
  dependency: {
    title: 'Declaring a dependency',
    purpose: 'A dependency says "this must FINISH before that can START" — the waiting item is blocked until the other is done. This is ordering between separate items; to put one item INSIDE another, set a parent in the item’s editor instead.',
    how: [
      'Either side may be a container: such an edge silently binds every current and future child inside it.',
      'Pick the policy for the abandoned case before asserting — it can be changed later only by retracting and re-adding the edge.',
      'Cycles are rejected outright, with the offending chain spelled out — work orders must stay acyclic.',
    ],
    components: [
      ['ignore — unblock with a warning (default)', 'if the prerequisite is abandoned, the dependent may proceed anyway and carries a ⚠ badge so the tolerance stays visible.'],
      ['block — stay blocked', 'an abandoned prerequisite keeps the dependent blocked until the work is redone or the edge is removed.'],
    ],
  },
  proposal: {
    title: 'Confirming an assignment',
    purpose: 'Starting work checks concrete resources out. The tool computes a feasible assignment — which resource satisfies which requirement — and shows it here; nothing is committed until you confirm.',
    how: [
      'Confirm to start the work and open exactly the listed allocations, atomically with the state change.',
      'Cancel to commit nothing at all.',
    ],
    components: [
      ['Assignment row', 'one requirement and the specific resource (and unit count) proposed to satisfy it.'],
      ['"The world drifted" notice', 'someone took a resource between proposing and confirming. Nothing was committed; the dialog now shows a FRESH feasible assignment — re-confirming it is safe. If nothing is feasible anymore, you are told so and nothing happens.'],
    ],
  },
  convert: {
    title: 'Converting to a container',
    purpose: 'Only leaf items are worked directly; containers compute everything from their children. To put a child inside an item that already has its own state or requirements, that work first moves onto an auto-created child step — so nothing is lost and the rule stays honest.',
    how: [
      'Confirm to move the item’s state and requirements onto a new "<name>-work" child in one atomic step, then your originally intended child is added next to it.',
      'The item’s history stays attached to it. This is the same pattern as adding a "final review" step to a workstream.',
    ],
    components: [
      ['"<name>-work"', 'the auto-created child carrying the former hands-on work: same type, same requirements, same state.'],
      ['The container', 'from now on shows rolled-up state and progress; add further child steps freely.'],
    ],
  },
  availability: {
    title: 'Availability',
    purpose: 'A simple on/off with a note ("maintenance", "on leave"). While off, the resource counts as zero usable units.',
    how: [
      'Marking a resource unavailable never kicks off current work — allocations stay open; it just stops new starts and flags the overage on both the resource and the affected work.',
      'The note shows on the resource board so everyone knows why.',
    ],
    components: [
      ['▲ over-allocated', 'open allocations exceed what is currently usable — expected mid-work; pausing the affected work is suggested, never forced.'],
    ],
  },
  bulkAdd: {
    title: 'Bulk add',
    purpose: 'Add many work items at once, dependencies included, as ONE atomic batch: either the whole table commits or none of it.',
    how: [
      'One row per item; the "Depends on" column takes comma-separated names — of existing things or of OTHER ROWS in this table.',
      'Preview validates everything without committing; row errors appear inline.',
    ],
    components: [
      ['Name / Type / Parent', 'the essentials; parent puts the row inside an existing container.'],
      ['Depends on', 'what each row must wait for before it can start (default policy: proceed with a warning if a prerequisite is cancelled).'],
    ],
  },
  weights: {
    title: 'Recommendation weights',
    purpose: 'The knobs of the ready-board score. The score is advice for "what next", not an ordering you must obey — and it is never stored, so tuning weights changes recommendations now without rewriting anything.',
    how: [
      'Raise a weight to favor that consideration; set it to 0 to ignore it. Every card’s score expansion shows each term’s contribution under the current weights.',
    ],
    components: [
      ['immediate unlock', 'how many things become startable if this finishes — quick wins.'],
      ['downstream reach', 'how much transitively waits on it — long-term weight.'],
      ['remaining depth', 'how long the unfinished chain through it is — keeps the critical path moving.'],
      ['waiting age', 'how long it has starved for resources — starvation credit, so long-waiting work gets first claim when capacity frees.'],
      ['scarcity penalty', 'subtracted when it would occupy heavily contended resources — prefer work that does not hog what everyone needs, unless it is high-impact enough to win anyway.'],
    ],
  },
} satisfies Record<string, HelpTopic>;

/** HelpKey is the closed set of topic names — a typo'd "?" wiring becomes a
 * type error (under tsc; esbuild only strips) instead of a dead button. */
export type HelpKey = keyof typeof HELP;
