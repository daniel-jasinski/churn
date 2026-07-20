// router.ts — hash-based routing.
//
//   #/project/:id/:view    view ∈ graph | board | tree — the project workbench
//   #/ready                ready work across every project
//   #/bottlenecks          contention, criticality, starvation
//   #/resources[/:id]      the resource board, optionally focused on one
//   #/history[/:entityId]
//   #/settings[/:section]  weights (default) and vocabulary
//
// The sidebar owns project and resource selection, so a project is part of
// the URL rather than an implicit sticky filter: #/project/:id/:view is the
// canonical form and the store's sticky selection follows it, not the other
// way round.
//
// Pre-sidebar URLs still resolve, so old bookmarks and the links embedded in
// help text keep working: #/graph/:id is the workbench's graph tab, #/tree
// and #/projects fold into the workbench (the id resolving to the sticky
// selection), and #/vocab is the settings vocabulary section.

export interface Route {
  name: string;
  arg?: string;
  arg2?: string;
}

/** The workbench tabs, in display order: the same things arranged three
 * ways. Exported so the shell renders exactly the set the router accepts. */
export const PROJECT_VIEWS: [string, string, string][] = [
  ['graph', 'Graph', 'dependency DAG'],
  ['board', 'Board', 'ready / blocked / in progress'],
  ['tree', 'Tree', 'containment and progress'],
];

/** projectView normalizes a workbench tab id, defaulting to the graph. */
export function projectView(arg?: string): string {
  return PROJECT_VIEWS.some(([id]) => id === arg) ? arg! : 'graph';
}

/** decodeSegment is decodeURIComponent that cannot throw. A hand-mangled
 * escape ('%E0%A4%A') would otherwise raise a URIError out of parseHash →
 * current() → render(), blanking the whole app until the URL is fixed by
 * hand. A malformed segment is far better treated as an id that resolves to
 * nothing: the route falls back and the app stays usable. */
function decodeSegment(raw: string): string {
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}

export function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  const name = parts[0] || 'ready';
  const arg = parts[1] ? decodeSegment(parts[1]) : undefined;
  const arg2 = parts[2] ? decodeSegment(parts[2]) : undefined;
  switch (name) {
    // A project route with no id is legal on the wire; the shell resolves it
    // against the sticky selection rather than rejecting it, so that
    // #/project alone lands somewhere sensible.
    case 'project':
      return { name: 'project', arg, arg2: projectView(arg2) };
    case 'ready': case 'bottlenecks': case 'resources':
    case 'history': case 'settings':
      return { name, arg };
    // ── pre-sidebar URLs ──
    case 'graph':
      return { name: 'project', arg, arg2: 'graph' };
    case 'tree':
      return { name: 'project', arg2: 'tree' };
    case 'projects':
      return { name: 'project', arg2: 'graph' };
    case 'vocab':
      return { name: 'settings', arg: 'vocab' };
    default:
      return { name: 'ready' };
  }
}

export function current(): Route {
  return parseHash(location.hash);
}

/** href builds a canonical hash for a route. A trailing segment is dropped
 * when the one before it is absent — '#/project//tree' would parse back as a
 * different route. */
export function href(name: string, arg?: string, arg2?: string): string {
  let out = `#/${name}`;
  if (!arg) return out;
  out += `/${encodeURIComponent(arg)}`;
  if (arg2) out += `/${encodeURIComponent(arg2)}`;
  return out;
}

export function navigate(name: string, arg?: string, arg2?: string): void {
  location.hash = href(name, arg, arg2);
}

export function onRoute(fn: (r: Route) => void): void {
  window.addEventListener('hashchange', () => fn(current()));
}
