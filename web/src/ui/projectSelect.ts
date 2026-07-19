// ui/projectSelect.ts — the ONE project-selection dropdown, shared by the
// ready board, graph view, and tree view. Same ordering everywhere (the
// API's deterministic id order), the same "+ New project…" affordance, and
// sticky: the choice persists across tabs via store.selectedProject.

import { Project } from '../api';
import { h, select } from '../dom';
import { store } from '../store';
import { openProjectEditor } from './projectEditor';

const NEW_PROJECT = '__new__';

/** projectSelect builds the shared dropdown bound to the sticky selection.
 *
 * allowAll: offer "all projects" (''). Views that need a concrete project
 * (the graph) pass false and give `value` explicitly.
 * onPick: runs after the sticky selection is updated — including for a
 * project created through "+ New project…". */
export function projectSelect(opts: {
  allowAll: boolean;
  value?: string;
  onPick: (id: string) => void;
}): HTMLSelectElement {
  const options = () => [
    ...(opts.allowAll ? [{ value: '', label: 'all projects' }] : []),
    ...store.projects.map((p: Project) => ({ value: p.id, label: p.name })),
    { value: NEW_PROJECT, label: '+ New project…' },
  ];
  const current = opts.value !== undefined ? opts.value : store.selectedProject;
  const sel = select(options(), current);
  // A stale sticky id (project since retracted, or a different workspace on
  // the same origin) falls back visibly AND effectively: the store must
  // follow, or views filtering on store.selectedProject render empty with
  // no change event left to fire.
  if (sel.value !== current) {
    sel.value = opts.allowAll ? '' : sel.value;
    if (store.selectedProject && !store.projects.some((p) => p.id === store.selectedProject)) {
      store.setSelectedProject(sel.value === NEW_PROJECT ? '' : sel.value);
    }
  }
  let last = sel.value;
  sel.addEventListener('change', () => {
    if (sel.value !== NEW_PROJECT) {
      last = sel.value;
      store.setSelectedProject(sel.value);
      opts.onPick(sel.value);
      return;
    }
    sel.value = last; // revert until the dialog succeeds
    openProjectEditor(undefined, (p) => {
      sel.replaceChildren(...options().map((o) =>
        h('option', { value: o.value, selected: o.value === p.id }, o.label)));
      sel.value = p.id;
      last = p.id;
      store.setSelectedProject(p.id);
      opts.onPick(p.id);
    });
  });
  return sel;
}
