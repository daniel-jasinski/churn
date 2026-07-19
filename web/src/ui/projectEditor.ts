// ui/projectEditor.ts — the small create/rename dialog for projects, shared
// by the projects view, the graph project picker, and the thing editor's
// project dropdown.

import { api, Project } from '../api';
import { field, h } from '../dom';
import { closeModal, openModal } from '../modal';
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
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', { class: 'btn btn-primary', onclick: () => void save() }, existing ? 'Rename' : 'Create')));
  openModal(existing ? `Rename ${existing.name}` : 'New project', body, { help: 'projectEditor' });
  nameIn.focus();
  nameIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') void save(); });
}
