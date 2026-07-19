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

export const HELP: Record<string, HelpTopic> = {
  ready: {
    title: 'Ready board',
    purpose: 'The daily driver: what can be worked on right now, what is waiting for resources, what is in flight, and what recently finished. Everything here is computed live from dependencies, states, and resource availability — nothing is curated by hand.',
    how: [
      'Filter by project (sticky across tabs), type, capability, or name; press / to jump to the name filter.',
      'Start a ready card: the tool proposes which concrete resources would satisfy its requirements; you confirm or cancel. If the world changed in between, you get a fresh proposal to review — nothing is committed behind your back.',
      'Finish / Pause / Abandon / Resume record state changes; Reopen puts a finished or abandoned thing back to pending.',
      'Click a score to expand exactly how it was computed, term by term.',
      'Use "Almost ready" at the bottom to see what is only a few blockers away — and what those blockers are.',
    ],
    components: [
      ['Ready', 'all its prerequisites are satisfied AND the resources it needs are free right now. Sorted by the recommendation score (highest first).'],
      ['Resource-blocked', 'prerequisites are done, but some required resource is busy or unavailable. It will flip to Ready by itself when capacity frees.'],
      ['In progress', 'currently being worked (holds resource allocations), plus paused work ("held") — paused items say whether they could resume right now.'],
      ['Recently done', 'finished work, most recently touched first.'],
      ['Score', 'a transparent ranking aid, not an order: it adds up how much this unlocks, how much waits on it downstream, chain depth, and how long it has starved for resources, minus a penalty for hogging contended resources. Expand it to see every term.'],
      ['Almost ready', 'pending things whose remaining blockers number at most N (widen with the "blockers ≤" control). Each blocker is shown with its own live status — a dropped blocker means someone must decide: redo it or remove the dependency.'],
      ['Badges', '⚠ a prerequisite was abandoned but this was allowed to proceed · ⁉ finished yet has unsatisfied prerequisites (worth a look) · ▲ holds more of a resource than is currently available · ↻ its allocations no longer match its edited requirements (use Re-propose).'],
      ['Re-propose', 'when requirements were edited while work was in flight, this swaps the old allocations for a fresh feasible set in one atomic step — work never stops holding what it needs.'],
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
      'Create and rename freely — renames never break anything, because everything references projects by a stable internal id.',
      'Retract removes a project, but only once nothing lives in it; the error tells you which things still do.',
      'Click a project name to open its dependency graph.',
    ],
    components: [
      ['Things', 'how many items of work (including containers) live in the project.'],
      ['Progress', 'finished leaves out of all non-abandoned leaves.'],
      ['Retract', 'removal with history: the project stops existing now, but every event about it stays in the log forever.'],
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
  bottlenecks: {
    title: 'Bottlenecks',
    purpose: 'Where the flow is stuck and where it would hurt most: contention for resources, structurally critical work, and work that has been starving for capacity.',
    how: [
      'Start at "unmet requirement units" — that is the one number computed rigorously enough to base decisions on.',
      'Use the signature table to see WHICH skill combinations are short and which things want them.',
      'Rank critical things by any of the three columns; they answer different questions and are never added together.',
      'Check starvation to see what has waited longest — long-starved work automatically gets scoring credit so it claims freed capacity first.',
    ],
    components: [
      ['Unmet requirement units (trustworthy)', 'how many required units of demand cannot fit onto free resources right now, computed by actually trying every assignment. This is the number to act on.'],
      ['Per-signature split (indicative)', 'the same shortfall attributed to specific skill-combinations. The split depends on assignment tie-breaks, so treat it as a strong hint, not gospel — the total above is the honest figure.'],
      ['Per-capability ratios (rough)', 'naive demand/supply per single tag. Double-counts multi-skilled resources and ignores skill combinations — at-a-glance only.'],
      ['Downstream reach', 'everything that can never finish while this is unfinished — long-term weight.'],
      ['Immediate unlock', 'how many things become startable the moment this finishes — short-term payoff. Reach does NOT imply unlock (dependents may have other blockers), which is why the numbers stay separate.'],
      ['Remaining depth', 'the longest chain of unfinished steps running through it — a schedule-length signal without time estimates.'],
      ['Starvation: current stint', 'how long it has been continuously resource-blocked right now.'],
      ['Starvation: cumulative credit', 'total time waited since it last held resources. It survives brief flips to ready, and boosts the recommendation score so long-starved work gets first claim on the unit that finally frees.'],
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
  vocab: {
    title: 'Vocabulary',
    purpose: 'Your own words for the workspace: states, thing types, resource types, and capability tags. The engine has no built-in names — it only understands the five state behaviors below; everything else is your labels. Every entry must be declared before use, so a typo can never silently break matching.',
    how: [
      'Define as many states as you like; each binds to exactly ONE of the five behaviors. Rename and recolor freely — history never changes meaning, because everything references entries by stable id.',
      'A state’s behavior is locked while anything is in that state (move the things out first); its name and color are always editable.',
      'Deleting any entry is refused while something still references it — the error lists exactly what.',
      'Declare metadata fields on thing types and resource types to get proper forms (text/number/date/choice) in the editors. They shape forms only; nothing is ever validated against them.',
    ],
    components: [
      ['pending', 'not started. Eligible for the ready board once its prerequisites are satisfied.'],
      ['active', 'being worked right now. Entering an active state checks out the resources it needs; leaving releases them.'],
      ['paused', 'deliberately on hold: holds NO resources, dependents stay blocked, excluded from ready lists.'],
      ['satisfied', 'done, successfully. Unblocks dependents and counts toward progress.'],
      ['abandoned', 'ended without success. Each dependency edge decides whether its dependent may proceed anyway (with a warning) or stays blocked.'],
      ['Thing types', 'labels and colors for work items (task, review, deliverable…) — filtering and reporting only, no engine meaning.'],
      ['Resource types', 'labels and colors for resources (person, room, tool…) — same: display only.'],
      ['Capabilities', 'the tags that actually matter for matching: requirements ask for them, resources carry them. Capabilities decide who CAN do work; types just label it.'],
      ['Metadata fields', 'per-type form declarations: key, label, input kind, choices, and a soft "required" hint. Purely form-driving.'],
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
};
