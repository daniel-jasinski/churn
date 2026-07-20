// ui/projectEditor.ts — the small create/rename/retract dialog for projects,
// shared by the sidebar's "+", the workbench header, and the thing editor's
// project dropdown.
//
// Retraction lives here rather than on a list screen because the sidebar
// replaced that screen: the dialog is now the only place a project is
// administered, so it has to carry the destructive verb too.

import { api, ApiError, Project } from '../api';
import { field, h } from '../dom';
import { closeModal, openModal } from '../modal';
import { navigate } from '../router';
import { store } from '../store';
import { showError, toast } from '../toast';

/** openProjectEditor creates (no `existing`) or renames a project. On a
 * successful create, `onCreated` runs after the store refresh — pickers use
 * it to select the new project. */
export function openProjectEditor(existing?: Project, onCreated?: (p: Project) => void): void {
  const nameIn = h('input', { type: 'text', value: existing?.name ?? '', placeholder: 'project name' });
  const save = async () => {
    const name = nameIn.value.trim();
    if (!name) { toast('Name is required.', 'error'); return; }
    try {
      let p: Project;
      if (existing) {
        p = await api.updateProject(existing.id, { name }, existing.version);
      } else {
        p = await api.createProject({ name });
      }
      closeModal();
      toast(existing ? `Project renamed to ${name}` : `Project ${name} created`, 'ok', 2500);
      await store.refresh();
      if (!existing && onCreated) onCreated(p);
    } catch (e) {
      showError(e);
    }
  };
  const body = h('div', null,
    field('Name', nameIn),
    existing ? null : h('p', { class: 'muted' },
      'A project scopes a dependency graph for display; resources stay shared across the whole workspace.'),
    existing
      ? h('p', { class: 'muted tiny' },
        'Retraction is refused while things still live in this project — retract or move the things first.')
      : null,
    h('div', { class: 'modal-actions' },
      existing
        ? h('button', {
          class: 'btn btn-danger mut',
          onclick: () => void retractProject(existing),
        }, 'Retract')
        : null,
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', { class: 'btn btn-primary', onclick: () => void save() }, existing ? 'Rename' : 'Create')));
  openModal(existing ? `Rename ${existing.name}` : 'New project', body, { help: 'projectEditor' });
  nameIn.focus();
  nameIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') void save(); });
}

/** retractProject tombstones a project. The server refuses while things
 * still reference it and names the blockers; surface them rather than a bare
 * "conflict", because the fix is to deal with those specific things. */
async function retractProject(p: Project): Promise<void> {
  try {
    await api.deleteProject(p.id);
    closeModal();
    toast(`Project ${p.name} retracted.`, 'ok');
    await store.refresh();
    // The workbench we were standing in no longer exists — land on whatever
    // project the store now resolves to, or the empty-workspace screen.
    const next = store.concreteProject();
    if (next) navigate('project', next, 'graph');
    else navigate('ready');
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
