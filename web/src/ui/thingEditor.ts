// ui/thingEditor.ts — create/edit modal for things: name, type, project,
// parent, metadata key-values, the requirements editor, and the
// dependencies editor (outbound editable, inbound shown read-only).

import { api, BatchOp, Dependency, Requirement, Thing } from '../api';
import { field, h, select } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';
import { showError, toast } from '../toast';
import { isPromotionRejection, offerPromotion } from './promotion';
import { metaForm } from './metaform';
import { openProjectEditor } from './projectEditor';
import { openCapabilityEditor, openTypeEditor } from '../views/vocab';

interface ReqRow {
  existing?: Requirement; // present = keep unless removed
  removed: boolean;
  quantity: number;
  pin: string; // resource id, '' = capability mode
  capabilities: string[];
}

interface DepRow {
  existing?: Dependency; // present = keep unless removed
  removed: boolean;
  to: string; // prerequisite thing id
  policy: 'ignore' | 'block';
}

export function openThingEditor(existing?: Thing, preset: { project?: string; parent?: string; focus?: 'deps' } = {}): void {
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
  const declaredFieldsOf = (typeId: string) => store.type(typeId)?.fields ?? [];
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

  // metadata: the TYPE's declared fields as proper inputs + free-form rows
  // for undeclared keys; the declared part follows the type dropdown, and
  // the section pops open whenever the selected type declares fields.
  const mf = metaForm(existing?.metadata, declaredFieldsOf(typeSel.value));
  let metaDetails: HTMLDetailsElement | null = null;
  typeSel.addEventListener('change', () => {
    const fields = declaredFieldsOf(typeSel.value);
    mf.setFields(fields);
    if (metaDetails && fields.length > 0) metaDetails.open = true;
  });

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
        // Capability multi-select, with the inline "+ new…" so a
        // zero-capability workspace is never a dead end: the stacked dialog
        // defines the tag and selects it on this row.
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
          }),
          h('button', {
            class: 'chip chip-toggle chip-new',
            title: 'define a new capability tag and select it here',
            onclick: (e: MouseEvent) => {
              e.preventDefault();
              openCapabilityEditor(undefined, (c) => {
                row.capabilities.push(c.id);
                renderReqRows();
              });
            },
          }, '+ new capability…'));
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

  // ── dependencies (§2.2): outbound editable, inbound read-only ──
  const depRows: DepRow[] = (existing
    ? store.dependencies.filter((d) => d.from === existing.id)
    : []).map((d) => ({ existing: d, removed: false, to: d.to, policy: d.on_abandoned }));
  const depBody = h('div', { class: 'dep-rows' });
  const depSearch = h('input', {
    type: 'search', placeholder: 'add dependency: search things by name…', class: 'dep-search',
  });
  const depMatches = h('div', { class: 'dep-matches' });

  const thingLabel = (t: Thing): string => {
    let l = t.name;
    if (t.project !== projectId) l += ` — ${store.project(t.project)?.name ?? t.project}`;
    if (t.composite) l += ' (container: waits for its whole subtree)';
    return l;
  };

  const renderDepRows = () => {
    depBody.replaceChildren();
    for (const row of depRows) {
      if (row.removed) continue;
      const target = store.thing(row.to);
      const policySel = select([
        { value: 'ignore', label: 'on abandon: unblock + warn (default)' },
        { value: 'block', label: 'on abandon: stay blocked' },
      ], row.policy, (v) => { row.policy = v as DepRow['policy']; });
      policySel.disabled = !!row.existing; // edges have no supersession (§5.2)
      if (row.existing) policySel.title = 'edges have no supersession — remove and re-add to change the policy';
      depBody.appendChild(h('div', { class: 'dep-row' },
        h('span', { class: 'dep-name' }, target ? thingLabel(target) : row.to),
        policySel,
        h('button', {
          class: 'btn btn-ghost', title: 'remove dependency',
          onclick: () => {
            if (row.existing) row.removed = true;
            else depRows.splice(depRows.indexOf(row), 1);
            renderDepRows();
          },
        }, '×')));
    }
  };
  renderDepRows();

  const renderDepMatches = () => {
    depMatches.replaceChildren();
    const q = depSearch.value.trim().toLowerCase();
    if (!q) return;
    const taken = new Set(depRows.filter((r) => !r.removed).map((r) => r.to));
    const hits = store.things
      .filter((t) => t.id !== existing?.id && !taken.has(t.id) && t.name.toLowerCase().includes(q))
      .slice(0, 8);
    if (hits.length === 0) {
      depMatches.appendChild(h('div', { class: 'muted tiny' }, 'no matching thing'));
      return;
    }
    for (const t of hits) {
      depMatches.appendChild(h('button', {
        class: 'dep-hit',
        onclick: (e: MouseEvent) => {
          e.preventDefault();
          depRows.push({ removed: false, to: t.id, policy: 'ignore' });
          depSearch.value = '';
          depMatches.replaceChildren();
          renderDepRows();
        },
      }, thingLabel(t)));
    }
  };
  depSearch.addEventListener('input', renderDepMatches);

  const save = async () => {
    const name = nameIn.value.trim();
    if (!name) { toast('Name is required.', 'error'); return; }
    if (!typeSel.value) { toast('Define a thing type first (vocabulary).', 'error'); return; }
    const metaErr = mf.firstError();
    if (metaErr) { toast(metaErr, 'error', 7000); return; }
    const metadata = mf.read();
    const base = {
      name, type: typeSel.value,
      ...(parentSel.value ? { parent: parentSel.value } : {}),
      ...(Object.keys(metadata).length > 0 ? { metadata } : {}),
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
      // dependency edits ride the same atomic batch
      for (const row of depRows) {
        if (row.existing && row.removed) ops.push({ op: 'retract', kind: 'dependency', id: row.existing.id });
      }
      for (const row of depRows) {
        if (row.existing || row.removed) continue;
        ops.push({
          op: 'create', kind: 'dependency',
          data: { from: thingRef, to: row.to, on_abandoned: row.policy },
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
    (metaDetails = h('details', {
      class: 'sub',
      open: Object.keys(existing?.metadata ?? {}).length > 0 || declaredFieldsOf(typeSel.value).length > 0,
    },
      h('summary', null, 'Metadata'),
      mf.el)),
    h('details', { class: 'sub', open: reqRows.length > 0 },
      h('summary', null, 'Requirements (leaves only)'),
      reqBody,
      h('button', {
        class: 'btn btn-ghost',
        onclick: () => { reqRows.push({ removed: false, quantity: 1, pin: '', capabilities: [] }); renderReqRows(); },
      }, '+ requirement')),
    depsDetails(),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', { class: 'btn btn-primary', onclick: () => void save() }, isEdit ? 'Save' : 'Create')));

  function depsDetails(): HTMLElement {
    const inbound = existing ? store.dependencies.filter((d) => d.to === existing.id) : [];
    const el = h('details', { class: 'sub', id: 'deps-section', open: depRows.length > 0 || preset.focus === 'deps' },
      h('summary', null, `Dependencies (${depRows.filter((r) => !r.removed).length} outbound)`),
      h('p', { class: 'muted tiny' },
        'What this thing must wait for before it can start. Policies when a prerequisite is abandoned: ',
        h('b', null, 'unblock + warn'), ' lets this proceed with a warning badge (default); ',
        h('b', null, 'stay blocked'), ' keeps it blocked until the prerequisite is redone or the edge is removed.'),
      depBody,
      h('div', { class: 'dep-add' }, depSearch, depMatches),
      inbound.length > 0
        ? h('div', { class: 'dep-inbound' },
          h('h4', null, `Required by (${inbound.length}) — read-only`),
          h('ul', null, ...inbound.map((d) => {
            const from = store.thing(d.from);
            return h('li', null,
              from ? from.name : d.from,
              h('span', { class: 'muted tiny' }, ` (${d.on_abandoned === 'block' ? 'blocks on abandon' : 'unblocks + warns'}) `),
              h('a', { class: 'tiny', href: `#/history/${d.from}` }, 'hist'));
          })))
        : null);
    return el;
  }

  openModal(isEdit ? `Edit ${existing.name}` : 'New thing', body, { wide: true, help: 'thingEditor' });
  if (preset.focus === 'deps') {
    const sec = body.querySelector('#deps-section');
    sec?.scrollIntoView({ block: 'center' });
    depSearch.focus();
  } else {
    nameIn.focus();
  }
}
