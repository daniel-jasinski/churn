// views/project.ts — the project workbench: one project, three arrangements
// of the same things (graph, board, tree) behind a tab strip.
//
// The workbench owns nothing of its own — it is a header and a tab strip
// around the three existing views, each of which now takes the project from
// the route instead of reading a sticky dropdown. That is the whole point of
// the sidebar shell: project selection happens in exactly one place.

import { h } from '../dom';
import { href, navigate, PROJECT_VIEWS, projectView } from '../router';
import { store } from '../store';
import { renderOnboard } from '../ui/onboard';
import { openProjectEditor } from '../ui/projectEditor';
import { renderGraph } from './graph';
import { renderReady } from './ready';
import { renderTree } from './tree';

/** projectStats counts leaves only: composites have no state of their own,
 * so counting them would double-count their subtrees (DESIGN.md §2.1). */
export function projectStats(id: string): { leaves: number; done: number; ready: number } {
  let leaves = 0;
  let done = 0;
  let ready = 0;
  for (const t of store.things) {
    if (t.project !== id || t.composite) continue;
    leaves++;
    if (t.status === 'finished') done++;
    else if (t.status === 'ready') ready++;
  }
  return { leaves, done, ready };
}

export function renderProject(root: HTMLElement, projectId?: string, viewArg?: string): void {
  const view = projectView(viewArg);
  // No id in the route (bare #/project, or a pre-sidebar #/tree): resolve the
  // sticky selection to a concrete project and canonicalize the URL, so a
  // reload and the sidebar highlight agree on where we are.
  if (!projectId || !store.project(projectId)) {
    const concrete = store.concreteProject();
    if (!concrete) {
      renderOnboard(root);
      return;
    }
    navigate('project', concrete, view);
    return;
  }
  // Canonicalize anything that parsed to this route by a compatibility path
  // (#/graph/:id, an unknown tab): the address bar must describe what is on
  // screen, or a copied URL teaches the old shape. navigate() re-enters with
  // a hash that already matches, so this settles in one hop.
  if (location.hash !== href('project', projectId, view)) {
    navigate('project', projectId, view);
    return;
  }
  store.setSelectedProject(projectId); // the route is canonical; keep it sticky

  const p = store.project(projectId)!;
  const s = projectStats(projectId);
  const head = h('header', { class: 'proj-head' },
    h('div', { class: 'proj-title' },
      h('h2', null, p.name),
      s.leaves > 0
        ? h('span', { class: 'chip' }, `${s.done} / ${s.leaves} leaves done`)
        : h('span', { class: 'chip muted' }, 'no things yet'),
      s.ready > 0 ? h('span', { class: 'chip chip-ready' }, `${s.ready} ready`) : null,
      h('span', { class: 'spacer' }),
      h('button', {
        class: 'btn btn-sm mut',
        onclick: () => openProjectEditor(p),
      }, 'Edit project')),
    h('nav', { class: 'proj-tabs' },
      ...PROJECT_VIEWS.map(([id, label, hint]) =>
        h('a', {
          href: href('project', projectId, id),
          class: id === view ? 'active' : '',
          title: hint,
        }, label))));

  const body = h('div', { class: 'proj-body' });
  root.replaceChildren(h('div', { class: 'proj-shell' }, head, body));

  if (view === 'board') renderReady(body, projectId);
  else if (view === 'tree') renderTree(body, projectId);
  else renderGraph(body, projectId);
}
