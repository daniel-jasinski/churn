// ui/bulkAdd.ts — multi-row table editor over /batch: all creates AND their
// dependencies commit as ONE atomic batch, with dependencies on other rows
// expressed via the server's "$N" placeholder syntax.
//
// Dep syntax per row: comma-separated names/ids of EXISTING things or of
// OTHER ROWS in this editor (by row name).

import { api, BatchOp } from '../api';
import { field, h, select } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';
import { showError, toast } from '../toast';
import { openProjectEditor } from './projectEditor';
import { openTypeEditor } from '../views/vocab';

interface Row {
  name: string;
  type: string;
  parent: string; // existing thing id or ''
  deps: string;   // comma-separated names/ids
  error?: string;
}

export function openBulkAdd(presetProject?: string): void {
  if (store.projects.length === 0) {
    toast('Every thing lives in a project — create one first.', 'info', 4000);
    openProjectEditor(undefined, (p) => openBulkAdd(p.id));
    return;
  }
  if (store.types.length === 0) {
    toast('Things need a declared type — define your first one.', 'info', 4000);
    openTypeEditor(undefined, () => openBulkAdd(presetProject));
    return;
  }
  const projectSel = select(store.projects.map((p) => ({ value: p.id, label: p.name })),
    presetProject ?? store.projects[0]!.id);
  const rows: Row[] = [{ name: '', type: store.types[0]!.id, parent: '', deps: '' }];
  const tbody = h('tbody', null);
  const status = h('div', { class: 'muted' });

  const parentOpts = () => [{ value: '', label: '(top)' },
    ...store.things.filter((t) => t.project === projectSel.value)
      .map((t) => ({ value: t.id, label: t.name }))];

  const render = () => {
    tbody.replaceChildren();
    rows.forEach((row, i) => {
      const nameIn = h('input', { type: 'text', value: row.name, placeholder: 'name', oninput: () => { row.name = nameIn.value; } });
      const typeSel = select(store.types.map((t) => ({ value: t.id, label: t.name })), row.type, (v) => { row.type = v; });
      const parSel = select(parentOpts(), row.parent, (v) => { row.parent = v; });
      const depsIn = h('input', {
        type: 'text', value: row.deps, placeholder: 'dep names, comma-separated',
        oninput: () => { row.deps = depsIn.value; },
      });
      tbody.appendChild(h('tr', row.error ? { class: 'row-error' } : null,
        h('td', null, String(i + 1)),
        h('td', null, nameIn),
        h('td', null, typeSel),
        h('td', null, parSel),
        h('td', null, depsIn),
        h('td', null, h('button', {
          class: 'btn btn-ghost', title: 'remove row',
          onclick: () => { rows.splice(i, 1); render(); },
        }, '×'))));
      if (row.error) {
        tbody.appendChild(h('tr', { class: 'row-error-msg' },
          h('td', null), h('td', { colspan: 5 }, '⚠ ' + row.error)));
      }
    });
  };
  render();

  const activeRows = () => rows.filter((r) => r.name.trim() !== '');

  // resolveDep maps a dep token to an existing thing id, a row index, or null.
  const resolveDep = (token: string, self: number): { id?: string; row?: number } | null => {
    const t = token.trim();
    if (!t) return null;
    const rowIdx = activeRows().findIndex((r, i) => i !== self && r.name.trim() === t);
    if (rowIdx >= 0) return { row: rowIdx };
    const th = store.things.find((x) => x.id === t)
      ?? store.things.filter((x) => x.project === projectSel.value).find((x) => x.name === t)
      ?? store.things.find((x) => x.name === t);
    return th ? { id: th.id } : null;
  };

  // buildOps composes ONE batch: row creates first (op index = row index),
  // then dependency asserts referencing new rows via "$rowIndex".
  const buildOps = (): BatchOp[] | null => {
    const act = activeRows();
    if (act.length === 0) { toast('Nothing to add.', 'error'); return null; }
    let bad = false;
    act.forEach((row, i) => {
      row.error = undefined;
      for (const tok of row.deps.split(',')) {
        if (tok.trim() && !resolveDep(tok, i)) {
          row.error = `dependency "${tok.trim()}" matches no existing thing and no other row`;
          bad = true;
        }
      }
    });
    render();
    if (bad) return null;
    const ops: BatchOp[] = act.map((row): BatchOp => ({
      op: 'create', kind: 'thing',
      data: {
        project: projectSel.value, name: row.name.trim(), type: row.type,
        ...(row.parent ? { parent: row.parent } : {}),
      },
    }));
    act.forEach((row, i) => {
      for (const tok of row.deps.split(',')) {
        const r = resolveDep(tok, i);
        if (!r) continue;
        const to = r.id ?? '$' + r.row!;
        ops.push({ op: 'create', kind: 'dependency', data: { from: '$' + i, to } });
      }
    });
    return ops;
  };

  const markOpError = (msg: string) => {
    const m = /operation (\d+): (.*)/s.exec(msg);
    if (m) {
      const idx = Number(m[1]);
      const act = activeRows();
      if (act[idx]) { act[idx].error = m[2]; render(); return; }
    }
    status.textContent = msg;
  };

  const preview = async () => {
    const ops = buildOps();
    if (!ops) return;
    const things = activeRows().length;
    try {
      await api.batch('preview', ops);
      status.textContent = `Preview OK: ${things} thing(s) and ${ops.length - things} dependency(ies) would commit as one batch.`;
      activeRows().forEach((r) => { r.error = undefined; });
      render();
    } catch (e) {
      if (e instanceof Error) markOpError(e.message);
      else showError(e);
    }
  };

  const commit = async () => {
    const ops = buildOps();
    if (!ops) return;
    const things = activeRows().length;
    try {
      await api.batch('commit', ops);
      closeModal();
      toast(`Added ${things} thing(s)` + (ops.length > things ? ` + ${ops.length - things} dependency(ies)` : '') + ' in one batch.', 'ok');
      await store.refresh();
    } catch (e) {
      if (e instanceof Error) markOpError(e.message);
      else showError(e);
      void store.refresh();
    }
  };

  const body = h('div', null,
    field('Project', projectSel),
    h('table', { class: 'table bulk-table' },
      h('thead', null, h('tr', null,
        h('th', null, '#'), h('th', null, 'Name'), h('th', null, 'Type'),
        h('th', null, 'Parent'), h('th', null, 'Depends on'), h('th', null, ''))),
      tbody),
    h('div', { class: 'bulk-actions' },
      h('button', {
        class: 'btn btn-ghost',
        onclick: () => { rows.push({ name: '', type: store.types[0]!.id, parent: '', deps: '' }); render(); },
      }, '+ row')),
    h('p', { class: 'muted' },
      'Everything — new things and their dependencies, including dependencies between rows — commits as one atomic batch.'),
    status,
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', { class: 'btn', onclick: () => void preview() }, 'Preview'),
      h('button', { class: 'btn btn-primary', onclick: () => void commit() }, 'Commit batch')));

  openModal('Bulk add things', body, { wide: true });
}
