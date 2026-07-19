// router.ts — hash-based routing: #/ready, #/graph/:projectId, #/resources,
// #/bottlenecks, #/tree, #/vocab, #/history/:entityId?, #/settings.

export interface Route {
  name: string;
  arg?: string;
}

export function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  const name = parts[0] || 'ready';
  const arg = parts[1] ? decodeURIComponent(parts[1]) : undefined;
  switch (name) {
    case 'ready': case 'graph': case 'resources': case 'bottlenecks':
    case 'tree': case 'vocab': case 'history': case 'settings':
      return { name, arg };
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
