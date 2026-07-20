// ui/inspector.ts — the one detail surface.
//
// A single right-hand column, owned by the shell rather than by any view, so
// that inspecting a resource does not cost you the project you were looking
// at. Two kinds of content share it:
//
//   view content — published by the active view (the graph's thing and edge
//     panels). Re-supplied on every render, so navigating away clears it.
//   a resource — opened from the sidebar. Sticky until closed, because the
//     whole point is to read it *against* whatever is on screen.
//
// Precedence: a resource wins, because it is what the user clicked most
// recently; closing it reveals the view's own panel again untouched. Only
// one thing is ever on screen — the panels never stack.

import { api, ResourceBoardRow } from '../api';
import { chip, h, statusDot } from '../dom';
import { modalDepth } from '../modal';
import { store } from '../store';

let host: HTMLElement | null = null;
let notifyShell: (() => void) | null = null;
let viewPanel: HTMLElement | null = null;
let resourceId: string | null = null;
// Guards the async board fetch: a second open (or a close) while the first
// is in flight must not have its result painted over the newer selection.
let fetchToken = 0;
// Last board response. The shell re-renders on every committed batch, and
// without this the allocations section would blink back to "Loading…" on
// each one; instead the known numbers stay up while the refetch runs.
let cachedRows: ResourceBoardRow[] | null = null;

/** mountInspector hands the inspector its element. `notify` re-renders the
 * shell so the sidebar highlight follows what is open. */
export function mountInspector(el: HTMLElement, notify: () => void): void {
  host = el;
  notifyShell = notify;
  document.addEventListener('keydown', (e) => {
    // Escape belongs to the topmost thing: a dialog closes before the panel
    // behind it. modal.ts handles Escape per-overlay, but the event still
    // bubbles here, so the depth check is what keeps the order right.
    if (e.key === 'Escape' && resourceId && modalDepth() === 0) {
      closeInspector();
    }
  });
}

/** setViewPanel publishes the active view's detail element. The element is
 * adopted as-is and kept, so a view that mutates it later (the graph, on
 * every node click) needs no further calls. */
export function setViewPanel(el: HTMLElement | null): void {
  viewPanel = el;
  // A pinned resource is already winning, so repainting here would only cost
  // a redundant board fetch. The shell's own renderInspector() closes the
  // render, and that is the one that repaints a pinned resource.
  if (!resourceId) renderInspector();
}

export function inspectedResource(): string | null {
  return resourceId;
}

/** toggleResource opens a resource, or closes it if it is already open —
 * clicking the same sidebar row twice is a natural "put it away". */
export function toggleResource(id: string): void {
  resourceId = resourceId === id ? null : id;
  fetchToken++;
  notifyShell?.(); // re-renders the shell, which repaints this panel
}

export function closeInspector(): void {
  if (!resourceId) return;
  resourceId = null;
  fetchToken++;
  notifyShell?.();
}

/** renderInspector paints whichever content wins. Called by the shell on
 * every render so a pinned resource stays live as the log advances. */
export function renderInspector(): void {
  if (!host) return;
  // A resource retracted while pinned (possibly by another session) stops
  // being inspectable: fall back rather than hold a panel about nothing.
  if (resourceId && !store.resources.some((r) => r.id === resourceId)) {
    resourceId = null;
  }
  // insp-resource marks the only content that can be dismissed (× and
  // Escape). Narrow layouts float the panel over the view, and floating
  // something with no way to close it would cover the canvas for good — so
  // only a resource ever floats; a view's own panel stays in flow.
  if (resourceId) {
    host.replaceChildren(resourceShell(resourceId));
    host.classList.add('on', 'insp-resource');
    host.setAttribute('aria-label', `${store.name(resourceId)} — resource details`);
    return;
  }
  host.classList.remove('insp-resource');
  if (viewPanel) {
    host.replaceChildren(viewPanel);
    host.classList.add('on');
    host.setAttribute('aria-label', 'Details');
    return;
  }
  host.replaceChildren();
  host.classList.remove('on');
}

/** resourceShell paints what the store already knows immediately, then fills
 * in allocations and the queue from the board endpoint — those are the only
 * two facts the store does not hold, and waiting on a fetch to show a name
 * would make every click feel slow. */
function resourceShell(id: string): HTMLElement {
  const res = store.resource(id);
  if (!res) return h('div', { class: 'panel-pad muted' }, 'This resource no longer exists.');
  const detail = h('div', { class: 'insp-detail' });
  // `settled` is what separates "no board entry" from "not fetched yet". The
  // cache can legitimately predate a resource (another session created it,
  // store.refresh put it in the sidebar), and asserting it has no board row
  // on the strength of a stale cache would be a confident wrong answer.
  const paint = (rows: ResourceBoardRow[] | null, settled: boolean) => {
    const row = rows?.find((r) => r.resource.id === id);
    detail.replaceChildren(row
      ? h('div', null, allocationList(row), queueList(row))
      : h('p', { class: 'muted tiny' }, settled ? 'No board entry.' : 'Loading allocations…'));
  };
  paint(cachedRows, false);
  const token = ++fetchToken;

  void (async () => {
    let rows: ResourceBoardRow[];
    try {
      rows = await api.resourceBoard();
    } catch (e) {
      if (token !== fetchToken) return; // same guard as the success path
      detail.replaceChildren(h('p', { class: 'muted tiny' }, String((e as Error).message)));
      return;
    }
    if (token !== fetchToken) return; // a newer selection won
    cachedRows = rows;
    paint(rows, true);
  })();

  const pct = res.capacity > 0 ? (100 * res.allocated) / res.capacity : 0;
  const effPct = res.capacity > 0 ? (100 * res.effective_capacity) / res.capacity : 0;

  return h('div', { class: 'panel-pad' },
    h('div', { class: 'insp-head' },
      h('b', { class: 'insp-title' }, res.name),
      h('button', {
        class: 'btn btn-ghost btn-sm',
        title: 'Close (Esc)',
        onclick: () => closeInspector(),
      }, '×')),
    h('div', { class: 'panel-row' },
      res.named ? chip('named', undefined, 'chip-dim') : chip(`pool ×${res.capacity}`, undefined, 'chip-dim'),
      res.type ? chip(store.resourceType(res.type)?.name ?? res.type, store.resourceType(res.type)?.color, 'chip-type') : null,
      res.over_allocated
        ? h('span', { class: 'badge badge-alert', title: 'more units are allocated than are currently usable — current work keeps running; consider pausing some of it' }, '▲')
        : null),
    h('div', { class: 'capbar', title: `${res.allocated} used / ${res.effective_capacity} effective / ${res.capacity} capacity` },
      h('div', { class: 'capbar-eff', style: { width: `${effPct}%` } }),
      h('div', { class: 'capbar-used' + (res.over_allocated ? ' over' : ''), style: { width: `${pct}%` } })),
    h('div', { class: 'muted tiny' },
      res.available
        ? `${res.allocated} of ${res.effective_capacity} allocated`
        : `unavailable${res.note ? ` — ${res.note}` : ''}`),
    (res.capabilities ?? []).length > 0
      ? h('div', { class: 'panel-row' },
        ...(res.capabilities ?? []).map((c) => chip(store.name(c), undefined, 'chip-cap')))
      : h('p', { class: 'muted tiny' }, 'No capabilities — nothing can match it.'),
    detail,
    // Editing stays on the board. The panel is for reading a resource
    // against the work in front of you; retagging capabilities and changing
    // capacity are bulk jobs that a 320px column would only make worse.
    h('div', { class: 'panel-actions mut' },
      h('a', { class: 'btn btn-sm', href: `#/resources/${res.id}` }, 'Open in board →'),
      h('a', { class: 'btn btn-sm btn-ghost', href: `#/history/${res.id}` }, 'history')));
}

function allocationList(row: ResourceBoardRow): HTMLElement {
  return h('div', { class: 'panel-sec' },
    h('h4', null, `Open allocations (${row.open_allocations.length})`),
    row.open_allocations.length === 0
      ? h('p', { class: 'muted tiny' }, 'idle')
      : h('ul', { class: 'insp-list' }, ...row.open_allocations.map((a) => h('li', null,
        h('a', { href: `#/history/${a.thing}` }, a.thing_name || a.thing),
        h('span', { class: 'muted' }, ` ×${a.quantity}`)))));
}

function queueList(row: ResourceBoardRow): HTMLElement {
  return h('div', { class: 'panel-sec' },
    h('h4', null, `Waiting for it (${row.queue.length})`),
    row.queue.length === 0
      ? h('p', { class: 'muted tiny' }, 'no queue')
      : h('ul', { class: 'insp-list' }, ...row.queue.map((q) => h('li', null,
        statusDot(q.status), ' ',
        h('a', { href: `#/history/${q.thing}` }, q.name),
        h('span', { class: 'muted' }, ` (${q.requirements.length} req)`)))));
}
