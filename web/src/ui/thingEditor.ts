// ui/thingEditor.ts — create/edit modal for things: name, type, project,
// parent, metadata key-values, and the requirements editor.

import { api, BatchOp, Requirement, Thing } from '../api';
import { field, h, select } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';
import { showError, toast } from '../toast';
import { isPromotionRejection, offerPromotion } from './promotion';
import { openProjectEditor } from './projectEditor';
import { openTypeEditor } from '../views/vocab';

interface ReqRow {
  existing?: Requirement; // present = keep unless removed
  removed: boolean;
  quantity: number;
  pin: string; // resource id, '' = capability mode
  capabilities: string[];
}

export function openThingEditor(existing?: Thing, preset: { project?: string; parent?: string } = {}): void {
  const isEdit = !!existing;
  if (store.projects.length === 0) {
    // Every thing lives in a project: offer the project dialog first, then
    // come back here with the fresh project preselected.
    toast('Every thing lives in a project — create one first.', 'info', 4000);
    openProjectEditor(undefined, (p) => openThingEditor(undefined, { ...preset, project: p.id }));
    return;
  }
  if (store.types.length === 0) {
    // Same pattern as the missing project: things need a declared type.
    toast('Things need a declared type — define your first one.', 'info', 4000);
    openTypeEditor(undefined, () => openThingEditor(existing, preset));
    return;
  }
  const projectId = existing?.project ?? preset.project ?? store.projects[0]!.id;

  const nameIn = h('input', { type: 'text', value: existing?.name ?? '', placeholder: 'name' });
  // The "+ New project…" option opens the (stacked) project dialog and
  // selects the created project here on success. Edit mode omits it — a
  // thing's project is immutable.
  const NEW_PROJECT = '__new__';
  const projectOpts = () => [
    ...store.projects.map((p) => ({ value: p.id, label: p.name })),
    ...(isEdit ? [] : [{ value: NEW_PROJECT, label: '+ New project…' }]),
  ];
  const projectSel = select(projectOpts(), projectId);
  projectSel.disabled = isEdit; // a thing's project is immutable
  let lastProject = projectId;
  projectSel.addEventListener('change', () => {
    if (projectSel.value !== NEW_PROJECT) {
      lastProject = projectSel.value;
      return;
    }
    projectSel.value = lastProject; // revert until the dialog succeeds
    openProjectEditor(undefined, (p) => {
      projectSel.replaceChildren(...projectOpts().map((o) =>
        h('option', { value: o.value, selected: o.value === p.id }, o.label)));
      projectSel.value = p.id;
      projectSel.dispatchEvent(new Event('change')); // refresh the parent list
    });
  });
  const typeSel = select(store.types.map((t) => ({ value: t.id, label: t.name })),
    existing?.type ?? store.types[0]?.id);
  const parentOpts = () => [{ value: '', label: '(top level)' },
    ...store.things
      .filter((t) => t.project === (isEdit ? projectId : projectSel.value) && t.id !== existing?.id)
      .map((t) => ({ value: t.id, label: t.composite ? `${t.name} (composite)` : t.name }))];
  let parentSel = select(parentOpts(), existing?.parent ?? preset.parent ?? '');
  projectSel.addEventListener('change', () => {
    const fresh = select(parentOpts(), '');
    parentSel.replaceWith(fresh);
    parentSel = fresh;
  });

  // metadata rows
  const metaBody = h('div', { class: 'kv-rows' });
  const addMetaRow = (k = '', v = '') => {
    const row = h('div', { class: 'kv-row' },
      h('input', { type: 'text', value: k, placeholder: 'key' }),
      h('input', { type: 'text', value: v, placeholder: 'value (JSON or text)' }),
      h('button', { class: 'btn btn-ghost', onclick: () => row.remove(), title: 'remove' }, '×'));
    metaBody.appendChild(row);
  };
  for (const [k, v] of Object.entries(existing?.metadata ?? {})) {
    addMetaRow(k, typeof v === 'string' ? v : JSON.stringify(v));
  }

  // requirements rows (leaves only; composites carry none)
  const reqRows: ReqRow[] = (existing ? store.requirementsOf(existing.id) : []).map((r) => ({
    existing: r, removed: false, quantity: r.quantity,
    pin: r.resource ?? '', capabilities: [...(r.capabilities ?? [])],
  }));
  const reqBody = h('div', { class: 'req-rows' });
  const namedResources = store.resources.filter((r) => r.named);

  const renderReqRows = () => {
    reqBody.replaceChildren();
    for (const row of reqRows) {
      if (row.removed) continue;
      const qty = h('input', {
        type: 'number', min: '1', value: String(row.quantity), class: 'in-qty',
        oninput: () => { row.quantity = Math.max(1, Number(qty.value) || 1); if (row.pin) qty.value = '1'; },
      });
      const modeSel = select(
        [{ value: 'caps', label: 'capabilities' }, { value: 'pin', label: 'pin resource' }],
        row.pin ? 'pin' : 'caps',
        (v) => {
          if (v === 'pin') { row.pin = namedResources[0]?.id ?? ''; row.quantity = 1; }
          else { row.pin = ''; }
          renderReqRows();
        });
      let detail: HTMLElement;
      if (row.pin) {
        detail = select(namedResources.map((r) => ({ value: r.id, label: r.name })), row.pin,
          (v) => { row.pin = v; });
        if (namedResources.length === 0) detail = h('span', { class: 'muted' }, 'no named resources');
      } else {
        detail = h('span', { class: 'cap-picks' },
          ...store.capabilities.map((c) => {
            const on = row.capabilities.includes(c.id);
            return h('button', {
              class: 'chip chip-toggle' + (on ? ' on' : ''),
              onclick: (e: MouseEvent) => {
                e.preventDefault();
                const i = row.capabilities.indexOf(c.id);
                if (i >= 0) row.capabilities.splice(i, 1); else row.capabilities.push(c.id);
                renderReqRows();
              },
            }, c.name);
          }));
      }
      reqBody.appendChild(h('div', { class: 'req-row' },
        qty, modeSel, detail,
        h('button', {
          class: 'btn btn-ghost', title: 'remove requirement',
          onclick: () => { row.removed = true; renderReqRows(); },
        }, '×')));
    }
  };
  renderReqRows();

  const save = async () => {
    const name = nameIn.value.trim();
    if (!name) { toast('Name is required.', 'error'); return; }
    if (!typeSel.value) { toast('Define a thing type first (vocabulary).', 'error'); return; }
    const metadata: Record<string, unknown> = {};
    let anyMeta = false;
    for (const row of Array.from(metaBody.children)) {
      const [kIn, vIn] = Array.from(row.querySelectorAll('input'));
      const k = kIn?.value.trim();
      if (!k) continue;
      const raw = vIn?.value ?? '';
      try { metadata[k] = JSON.parse(raw); } catch { metadata[k] = raw; }
      anyMeta = true;
    }
    const base = {
      name, type: typeSel.value,
      ...(parentSel.value ? { parent: parentSel.value } : {}),
      ...(anyMeta ? { metadata } : {}),
    };
    // ONE atomic /batch: the thing create/supersession plus every
    // requirement retract/assert. A new thing's requirements reference its
    // minted id via the "$0" placeholder.
    const doSave = async () => {
      const ops: BatchOp[] = [];
      let thingRef: string;
      if (isEdit) {
        thingRef = existing.id;
        ops.push({ op: 'supersede', kind: 'thing', id: existing.id, data: base });
      } else {
        thingRef = '$0';
        ops.push({ op: 'create', kind: 'thing', data: { project: projectSel.value, ...base } });
      }
      for (const row of reqRows) {
        if (row.existing && row.removed) ops.push({ op: 'retract', kind: 'requirement', id: row.existing.id });
      }
      for (const row of reqRows) {
        if (row.existing || row.removed) continue;
        if (!row.pin && row.capabilities.length === 0) continue;
        ops.push({
          op: 'create', kind: 'requirement',
          data: {
            thing: thingRef, quantity: row.pin ? 1 : row.quantity,
            ...(row.pin ? { resource: row.pin } : { capabilities: row.capabilities }),
          },
        });
      }
      await api.batch('commit', ops, isEdit ? { [existing.id]: existing.version } : undefined);
      closeModal();
      toast(isEdit ? `${name} updated` : `${name} created`, 'ok', 2500);
      await store.refresh();
    };
    try {
      await doSave();
    } catch (e) {
      const parentId = parentSel.value;
      const parent = parentId ? store.thing(parentId) : undefined;
      if (parent && isPromotionRejection(e, parent.id)) {
        offerPromotion(parent, doSave);
        return;
      }
      showError(e);
    }
  };

  const body = h('div', null,
    field('Name', nameIn),
    h('div', { class: 'form-grid' },
      field('Project', projectSel),
      field('Type', typeSel),
      field('Parent (containment)', parentSel)),
    h('details', { class: 'sub' },
      h('summary', null, 'Metadata'),
      metaBody,
      h('button', { class: 'btn btn-ghost', onclick: () => addMetaRow() }, '+ key')),
    h('details', { class: 'sub', open: reqRows.length > 0 },
      h('summary', null, 'Requirements (leaves only)'),
      reqBody,
      h('button', {
        class: 'btn btn-ghost',
        onclick: () => { reqRows.push({ removed: false, quantity: 1, pin: '', capabilities: [] }); renderReqRows(); },
      }, '+ requirement')),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', { class: 'btn btn-primary', onclick: () => void save() }, isEdit ? 'Save' : 'Create')));

  openModal(isEdit ? `Edit ${existing.name}` : 'New thing', body, { wide: true });
  nameIn.focus();
}
