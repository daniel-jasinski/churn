// main.ts — boot: layout shell, sidebar, routing, store wiring, keyboard
// shortcuts.
//
// The shell is a left sidebar plus a content pane. The sidebar is the single
// place selection happens: pick a project and the pane becomes that project's
// workbench (graph / board / tree); pick a resource and the pane becomes that
// resource. Between them sit the two screens that deliberately span every
// project — ready work and bottlenecks — because contention is only
// meaningful workspace-wide (DESIGN.md §3.3).

import './styles.css';
import './ui/help'; // registers the dialog help button before any modal opens
import { h } from './dom';
import { current, href, navigate, onRoute, projectView, Route } from './router';
import { store } from './store';
import { openAsOfPicker } from './ui/asof';
import { helpButton } from './ui/help';
import { openProjectEditor } from './ui/projectEditor';
import { renderBottlenecks } from './views/bottlenecks';
import { renderHistory } from './views/history';
import { renderProject, projectStats } from './views/project';
import { renderReady } from './views/ready';
import { openResourceEditor, renderResources } from './views/resources';
import { renderSettings } from './views/settings';

// The workspace screens: analytics that span every project, so they belong to
// no project's workbench. Ordered before Resources — they are the daily read.
const WORKSPACE: [string, string, string, string][] = [
  ['ready', 'Ready work', '◎', 'Ready work across every project (g r)'],
  ['bottlenecks', 'Bottlenecks', '◆', 'Contention, criticality, starvation (g b)'],
];

// Icon affordances at the right. History is reached far more often by
// deep-link (every thing card links its own history) than by browsing the
// whole log, and settings is configure-once — neither earns a sidebar slot.
const ICONS: [string, string, string][] = [
  ['history', '⟲', 'History — the full commit log (g h)'],
  ['settings', '⚙', 'Settings — weights (g w), vocabulary (g v)'],
];

const app = document.getElementById('app')!;
const banner = h('div', { class: 'asof-banner', id: 'asof-banner' });
const topbar = h('header', { class: 'topbar' });
const sidebar = h('nav', { class: 'sidebar', id: 'sidebar' });
const view = h('main', { class: 'view', id: 'view' });
app.replaceChildren(topbar, banner, h('div', { class: 'shell' }, sidebar, view));

function renderTopbar(): void {
  const r = current();
  topbar.replaceChildren(
    h('span', { class: 'brand' }, h('span', { class: 'brand-text' }, 'churn')),
    h('span', { class: 'spacer' }),
    h('span', {
      class: 'live ' + (store.live ? 'live-on' : 'live-off'),
      title: store.live ? 'live: SSE commit stream connected' : 'polling every 10s (SSE unavailable)',
    }, store.live ? '● live' : '○ polling'),
    store.workspace
      ? h('span', { class: 'muted tiny', title: `workspace ${store.workspace.workspace_id}` },
        `seq ${store.workspace.last_seq}`)
      : null,
    h('span', { class: 'topbar-icons' },
      ...ICONS.map(([name, glyph, title]) =>
        h('a', {
          href: `#/${name}`,
          class: 'icon-link' + (r.name === name ? ' active' : ''),
          title,
        }, glyph))));
}

/** sideLink is every sidebar row: an anchor so the browser handles
 * middle-click, focus and the address bar, never a click handler. */
function sideLink(opts: {
  to: string;
  label: string;
  active: boolean;
  glyph?: string;
  count?: string | null;
  title?: string;
}): HTMLElement {
  return h('a', {
    class: 'side-item' + (opts.active ? ' active' : ''),
    href: opts.to,
    title: opts.title ?? opts.label,
  },
  opts.glyph ? h('span', { class: 'side-glyph' }, opts.glyph) : null,
  h('span', { class: 'side-name' }, opts.label),
  opts.count ? h('span', { class: 'side-count' }, opts.count) : null);
}

function sideSection(label: string, ...body: (Node | null)[]): HTMLElement {
  return h('section', { class: 'side-sect' },
    h('div', { class: 'side-sect-head' }, h('h4', null, label)),
    ...body);
}

function renderSidebar(r: Route): void {
  // ── projects ──
  // Switching project keeps the tab you are on: the tabs are arrangements of
  // the same data, so the arrangement is a preference, not a destination.
  const view0 = r.name === 'project' ? projectView(r.arg2) : 'graph';
  const projectRows: Node[] = [];
  for (const p of store.projects) {
    const s = projectStats(p.id);
    const pctDone = s.leaves > 0 ? Math.round((s.done / s.leaves) * 100) : 0;
    projectRows.push(sideLink({
      to: href('project', p.id, view0),
      label: p.name,
      active: r.name === 'project' && r.arg === p.id,
      count: s.ready > 0 ? String(s.ready) : null,
      title: s.leaves > 0
        ? `${p.name} — ${s.done}/${s.leaves} leaves done${s.ready > 0 ? `, ${s.ready} ready` : ''}`
        : `${p.name} — no things yet`,
    }));
    // A hairline rather than a bar: at this density a real progress bar would
    // out-shout the project name it belongs to.
    projectRows.push(h('div', { class: 'side-prog', 'aria-hidden': 'true' },
      h('i', { style: { width: `${pctDone}%` } })));
  }
  const projects = h('section', { class: 'side-sect' },
    h('div', { class: 'side-sect-head' },
      h('h4', null, 'Projects'),
      helpButton('projects'),
      h('span', { class: 'spacer' }),
      h('button', {
        class: 'side-add mut',
        title: 'New project',
        onclick: () => openProjectEditor(undefined, (p) => navigate('project', p.id, view0)),
      }, '+')),
    ...projectRows.length > 0
      ? projectRows
      : [h('p', { class: 'side-empty muted tiny' }, 'No projects yet.')]);

  // ── workspace ──
  const workspace = sideSection('Workspace',
    ...WORKSPACE.map(([name, label, glyph, title]) =>
      sideLink({ to: `#/${name}`, label, glyph, title, active: r.name === name })));

  // ── resources ──
  const resourceRows: Node[] = [sideLink({
    to: '#/resources',
    label: 'All resources',
    glyph: '▤',
    active: r.name === 'resources' && !r.arg,
    title: 'The resource board — every resource, its queue and allocations (g s)',
  })];
  for (const res of store.resources) {
    resourceRows.push(sideLink({
      to: href('resources', res.id),
      label: res.name,
      glyph: res.named ? '●' : '◍',
      active: r.name === 'resources' && r.arg === res.id,
      count: res.available ? `${res.allocated}/${res.effective_capacity}` : '—',
      title: res.available
        ? `${res.name} — ${res.allocated} of ${res.effective_capacity} allocated`
        : `${res.name} — unavailable${res.note ? `: ${res.note}` : ''}`,
    }));
  }
  const resources = h('section', { class: 'side-sect' },
    h('div', { class: 'side-sect-head' },
      h('h4', null, 'Resources'),
      helpButton('resources'),
      h('span', { class: 'spacer' }),
      h('button', {
        class: 'side-add mut',
        title: 'New resource',
        onclick: () => openResourceEditor(),
      }, '+')),
    ...resourceRows);

  sidebar.replaceChildren(projects, workspace, resources);
}

function renderBanner(): void {
  if (!store.asOf) {
    banner.replaceChildren();
    banner.classList.remove('on');
    return;
  }
  banner.classList.add('on');
  banner.replaceChildren(
    h('span', null, `🕰 Viewing the past (as of ${store.asOf}) — read-only. `,
      h('span', { class: 'muted' }, 'Graph and tree show the past; other screens show the present. All mutations are disabled.')),
    h('button', { class: 'btn btn-sm', onclick: () => { store.setAsOf(null); render(); } }, 'Return to now'));
}

function render(): void {
  const r: Route = current();
  renderTopbar();
  renderBanner();
  if (!store.loaded) {
    sidebar.replaceChildren();
    view.replaceChildren(h('div', { class: 'empty' }, 'Connecting to the workspace…'));
    return;
  }
  renderSidebar(r);
  switch (r.name) {
    case 'project': renderProject(view, r.arg, r.arg2); break;
    case 'ready': renderReady(view, null); break;
    case 'resources': renderResources(view, r.arg); break;
    case 'bottlenecks': renderBottlenecks(view); break;
    case 'history': renderHistory(view, r.arg); break;
    case 'settings': renderSettings(view, r.arg); break;
  }
}

// keyboard: '/' focuses the filter, 'g' + letter navigates, 't' time travel
let gPending = false;
document.addEventListener('keydown', (e) => {
  const el = e.target as HTMLElement;
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) return;
  if (e.key === '/') {
    const search = document.querySelector<HTMLInputElement>('input[data-hotkey="slash"]');
    if (search) { e.preventDefault(); search.focus(); }
    return;
  }
  if (gPending) {
    gPending = false;
    // The project keys ('g g', 'g p', 'g t') land on the sticky project's
    // workbench — the same resolution the sidebar and the router use, so a
    // hotkey never navigates somewhere the sidebar would not highlight.
    // 'g v' and 'g w' predate the settings split; both still work.
    const proj = store.concreteProject() ?? undefined;
    const map: Record<string, [string, string?, string?]> = {
      r: ['ready'], b: ['bottlenecks'], s: ['resources'], h: ['history'],
      g: ['project', proj, 'graph'],
      p: ['project', proj, 'board'],
      t: ['project', proj, 'tree'],
      v: ['settings', 'vocab'], w: ['settings'],
    };
    const dest = map[e.key];
    if (dest) { e.preventDefault(); navigate(dest[0], dest[1], dest[2]); }
    return;
  }
  if (e.key === 'g') { gPending = true; setTimeout(() => { gPending = false; }, 800); return; }
  if (e.key === 'T' && e.shiftKey) openAsOfPicker();
});

onRoute(render);
store.subscribe(render);
store.start();
render();

// debug hook (console poking; nothing in the app reads it)
(window as unknown as Record<string, unknown>)['__store'] = store;
