// router.ts — hash-based routing: #/ready, #/graph/:projectId, #/projects,
// #/resources, #/bottlenecks, #/tree, #/history/:entityId?,
// #/settings/:section?.
//
// The low-frequency configuration screens live under #/settings: the
// recommendation weights (default section) and the vocabulary manager
// (#/settings/vocab). The pre-split #/vocab URL is still honored so old
// bookmarks and links land on the right section.

export interface Route {
  name: string;
  arg?: string;
}

export function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  const name = parts[0] || 'ready';
  const arg = parts[1] ? decodeURIComponent(parts[1]) : undefined;
  switch (name) {
    case 'ready': case 'graph': case 'projects': case 'resources':
    case 'bottlenecks': case 'tree': case 'history': case 'settings':
      return { name, arg };
    case 'vocab':
      return { name: 'settings', arg: 'vocab' };
    default:
      return { name: 'ready' };
  }
}

export function current(): Route {
  return parseHash(location.hash);
}

export function navigate(name: string, arg?: string): void {
  location.hash = arg ? `#/${name}/${encodeURIComponent(arg)}` : `#/${name}`;
}

export function onRoute(fn: (r: Route) => void): void {
  window.addEventListener('hashchange', () => fn(current()));
}
