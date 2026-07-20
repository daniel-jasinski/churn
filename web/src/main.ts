// main.ts — boot: layout shell, routing, store wiring, keyboard shortcuts.

import './styles.css';
import './ui/help'; // registers the dialog help button before any modal opens
import { h } from './dom';
import { current, onRoute, navigate, Route } from './router';
import { store } from './store';
import { openAsOfPicker } from './ui/asof';
import { renderBottlenecks } from './views/bottlenecks';
import { renderGraph } from './views/graph';
import { renderProjects } from './views/projects';
import { renderHistory } from './views/history';
import { renderReady } from './views/ready';
import { renderResources } from './views/resources';
import { renderSettings } from './views/settings';
import { renderTree } from './views/tree';

// The daily boards — the screens worth a permanent labelled slot.
const NAV: [string, string, string][] = [
  ['ready', 'Ready', 'g r'],
  ['graph', 'Graph', 'g g'],
  ['projects', 'Projects', 'g p'],
  ['resources', 'Resources', 'g s'],
  ['bottlenecks', 'Bottlenecks', 'g b'],
  ['tree', 'Tree', 'g t'],
];

// Icon affordances at the right. History is reached far more often by
// deep-link (every thing card links its own history) than by browsing the
// whole log, and settings is configure-once — neither earns a nav tab.
const ICONS: [string, string, string][] = [
  ['history', '⟲', 'History — the full commit log (g h)'],
  ['settings', '⚙', 'Settings — weights (g w), vocabulary (g v)'],
];

const app = document.getElementById('app')!;
const banner = h('div', { class: 'asof-banner', id: 'asof-banner' });
const topbar = h('header', { class: 'topbar' });
const view = h('main', { class: 'view', id: 'view' });
app.replaceChildren(topbar, banner, view);

function renderTopbar(): void {
  const r = current();
  topbar.replaceChildren(
    h('span', { class: 'brand' }, 'churn'),
    h('nav', null, ...NAV.map(([name, label, key]) =>
      h('a', {
        href: `#/${name}`,
        class: r.name === name ? 'active' : '',
        title: `${label} (${key})`,
      }, label))),
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
    view.replaceChildren(h('div', { class: 'empty' }, 'Connecting to the workspace…'));
    return;
  }
  switch (r.name) {
    case 'ready': renderReady(view); break;
    case 'graph': renderGraph(view, r.arg); break;
    case 'projects': renderProjects(view); break;
    case 'resources': renderResources(view); break;
    case 'bottlenecks': renderBottlenecks(view); break;
    case 'tree': renderTree(view); break;
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
    // 'g v' and 'g w' predate the settings split; both still work, landing on
    // the section they always did.
    const map: Record<string, [string, string?]> = {
      r: ['ready'], g: ['graph'], p: ['projects'], s: ['resources'],
      b: ['bottlenecks'], t: ['tree'], h: ['history'],
      v: ['settings', 'vocab'], w: ['settings'],
    };
    const dest = map[e.key];
    if (dest) { e.preventDefault(); navigate(dest[0], dest[1]); }
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
