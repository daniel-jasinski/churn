// views/projects.ts — project management (#/projects): list with thing
// counts, create, rename (supersede), retract. Retraction while things
// still reference the project surfaces the structured retraction_blocked
// error with the blockers named.

import { api, ApiError, Project } from '../api';
import { h } from '../dom';
import { store } from '../store';
import { showError, toast } from '../toast';
import { helpButton } from '../ui/help';
import { openProjectEditor } from '../ui/projectEditor';

export function renderProjects(root: HTMLElement): void {
  const toolbar = h('div', { class: 'toolbar' },
    h('h2', null, 'Projects'), helpButton('projects'),
    h('span', { class: 'spacer' }),
    h('button', { class: 'btn btn-primary mut', onclick: () => openProjectEditor() }, '+ New project'));

  if (store.projects.length === 0) {
    root.replaceChildren(toolbar, h('div', { class: 'empty' },
      h('p', null, 'No projects yet. Every thing lives in a project — create one to get started.'),
      h('p', null, h('button', { class: 'btn btn-primary mut', onclick: () => openProjectEditor() }, 'Create your first project'))));
    return;
  }

  const rows = store.projects.map((p) => {
    const things = store.things.filter((t) => t.project === p.id);
    const leaves = things.filter((t) => !t.composite);
    const done = leaves.filter((t) => t.status === 'finished').length;
    return h('tr', null,
      h('td', null, h('a', { href: `#/graph/${p.id}` }, p.name)),
      h('td', null, String(things.length)),
      h('td', { class: 'muted' }, leaves.length > 0 ? `${done}/${leaves.length} leaves done` : '—'),
      h('td', null,
        h('div', { class: 'card-actions mut' },
          h('button', { class: 'btn btn-sm', onclick: () => openProjectEditor(p) }, 'Rename'),
          h('button', {
            class: 'btn btn-sm btn-danger',
            onclick: () => void retractProject(p),
          }, 'Retract'))));
  });

  root.replaceChildren(toolbar,
    h('table', { class: 'table projects-table' },
      h('thead', null, h('tr', null,
        h('th', null, 'project'), h('th', null, 'things'), h('th', null, 'progress'), h('th', null, ''))),
      h('tbody', null, ...rows)),
    h('p', { class: 'muted tiny' },
      'Retraction is refused while things still live in a project — retract or move the things first.'));
}

async function retractProject(p: Project): Promise<void> {
  try {
    await api.deleteProject(p.id);
    toast(`Project ${p.name} retracted.`, 'ok');
    await store.refresh();
  } catch (e) {
    if (e instanceof ApiError && e.kind === 'retraction_blocked') {
      const names = e.ids.map((id) => store.name(id));
      const shown = names.slice(0, 8).join(', ');
      toast(`Cannot retract ${p.name}: ${names.length} thing(s) still live in it` +
        (shown ? ` — ${shown}${names.length > 8 ? ', …' : ''}` : '') +
        '. Retract them first (the graph view can cascade).', 'error', 10000);
      return;
    }
    showError(e);
  }
}
