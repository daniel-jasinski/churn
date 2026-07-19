// views/resources.ts — the resource board (§4.3): one row per resource with
// capacity bar, availability toggle + note, open allocations, and the queue
// of ready/resource-blocked things wanting it.

import { api, Resource, ResourceBoardRow } from '../api';
import { chip, field, h, select, statusDot } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';
import { showError, toast } from '../toast';
import { reqText } from '../ui/bits';
import { helpButton } from '../ui/help';
import { metaForm } from '../ui/metaform';
import { openCapabilityEditor, openResourceTypeEditor } from './vocab';

// typeFilter is the board's resource-type filter ('' = all); module-level so
// it survives re-renders.
let typeFilter = '';

export function renderResources(root: HTMLElement): void {
  // A stale filter (type retracted, or a different workspace served on the
  // same origin) must reset, or the board renders empty with the dropdown
  // showing "all" and no change event left to clear it.
  if (typeFilter && typeFilter !== '__untyped__'
    && !store.resourceTypes.some((t) => t.id === typeFilter)) {
    typeFilter = '';
  }
  const toolbar = h('div', { class: 'toolbar' },
    h('h2', null, 'Resources'), helpButton('resources'),
    store.resourceTypes.length > 0
      ? select([
        { value: '', label: 'all resource types' },
        ...store.resourceTypes.map((t) => ({ value: t.id, label: t.name })),
        { value: '__untyped__', label: '(untyped)' },
      ], typeFilter, (v) => { typeFilter = v; renderResources(root); })
      : null,
    h('span', { class: 'spacer' }),
    h('button', { class: 'btn btn-primary mut', onclick: () => openResourceEditor() }, '+ New resource'));
  const body = h('div', { class: 'res-rows' }, h('div', { class: 'empty' }, 'Loading…'));
  root.replaceChildren(toolbar, body);

  void (async () => {
    let rows: ResourceBoardRow[];
    try {
      rows = await api.resourceBoard();
    } catch (e) {
      body.replaceChildren(h('div', { class: 'empty' }, String((e as Error).message)));
      return;
    }
    if (rows.length === 0) {
      body.replaceChildren(h('div', { class: 'empty' },
        h('p', null, 'No resources yet. Resources are what work is done with — people, pools, workspaces, tools.'),
        h('p', { class: 'muted' }, modelingGuidance())));
      return;
    }
    const shown = rows.filter((r) => {
      if (!typeFilter) return true;
      if (typeFilter === '__untyped__') return !r.resource.type;
      return r.resource.type === typeFilter;
    });
    body.replaceChildren(
      shown.length === 0
        ? h('div', { class: 'empty' }, 'No resources of this type.')
        : h('span'),
      ...shown.map(row));
  })();
}

function modelingGuidance(): string {
  return 'Named resources suit individuals whose skills differ, or where it matters which one did the ' +
    'work — give them shared capability tags. Pools suit genuinely interchangeable units you never ' +
    'track individually. The ? explains how to choose.';
}

function row(r: ResourceBoardRow): HTMLElement {
  const res = r.resource;
  const eff = res.effective_capacity;
  const usedPct = res.capacity > 0 ? Math.min(100, (100 * res.allocated) / res.capacity) : 0;
  const effPct = res.capacity > 0 ? (100 * eff) / res.capacity : 0;

  const bar = h('div', { class: 'capbar', title: `${res.allocated} used / ${eff} effective / ${res.capacity} capacity` },
    h('div', { class: 'capbar-eff', style: { width: `${effPct}%` } }),
    h('div', { class: 'capbar-used' + (res.over_allocated ? ' over' : ''), style: { width: `${usedPct}%` } }));

  const capsEl = h('div', { class: 'res-caps' },
    ...(res.capabilities ?? []).map((c) => {
      const el = chip(store.name(c), undefined, 'chip-cap');
      el.title = 'click to revoke';
      el.classList.add('mut');
      el.onclick = async () => {
        try { await api.revokeCapability(res.id, c); await store.refresh(); } catch (e) { showError(e); }
      };
      return el;
    }),
    grantButton(res));

  const availBtn = h('button', {
    class: 'btn btn-sm mut ' + (res.available ? 'btn-ok' : 'btn-warn'),
    title: res.available
      ? 'Mark unavailable: stops new starts, never kicks off current work'
      : 'Mark available',
    onclick: () => toggleAvailability(res),
  }, res.available ? 'available' : 'unavailable');

  return h('section', { class: 'res-row' + (res.available ? '' : ' res-down') },
    h('header', { class: 'res-head' },
      h('b', null, res.name),
      res.type ? chip(store.resourceType(res.type)?.name ?? res.type, store.resourceType(res.type)?.color, 'chip-type') : null,
      res.named ? chip('named', undefined, 'chip-dim') : chip(`pool ×${res.capacity}`, undefined, 'chip-dim'),
      res.over_allocated ? h('span', { class: 'badge badge-alert', title: 'more units are allocated than are currently usable — current work keeps running; consider pausing some of it' }, '▲ over-allocated') : null,
      h('span', { class: 'spacer' }),
      h('span', { class: 'muted' }, `${res.allocated}/${eff} used · cap ${res.capacity}`),
      availBtn,
      h('button', { class: 'btn btn-sm mut', onclick: () => openResourceEditor(res) }, 'Edit'),
      h('button', {
        class: 'btn btn-sm btn-danger mut',
        onclick: async () => {
          try { await api.deleteResource(res.id); toast(`${res.name} retracted.`, 'ok'); await store.refresh(); } catch (e) { showError(e); }
        },
      }, 'Retract'),
      h('a', { class: 'btn btn-sm btn-ghost', href: `#/history/${res.id}` }, 'history')),
    res.note ? h('div', { class: 'res-note' }, '📝 ' + res.note) : null,
    bar,
    capsEl,
    h('div', { class: 'res-detail' },
      h('div', { class: 'res-col' },
        h('h4', null, `Open allocations (${r.open_allocations.length})`),
        r.open_allocations.length === 0 ? h('p', { class: 'muted' }, 'idle')
          : h('ul', null, ...r.open_allocations.map((a) => h('li', null,
            h('a', { href: `#/history/${a.thing}` }, a.thing_name || a.thing),
            h('span', { class: 'muted' }, ` ×${a.quantity}`))))),
      h('div', { class: 'res-col' },
        h('h4', null, `Waiting for it (${r.queue.length})`),
        r.queue.length === 0 ? h('p', { class: 'muted' }, 'no queue')
          : h('ul', null, ...r.queue.map((q) => h('li', null,
            statusDot(q.status), ' ',
            h('a', { href: `#/history/${q.thing}` }, q.name),
            h('span', { class: 'muted' }, ` (${q.requirements.length} req)`)))))));
}

// grantButton is always visible: ungranted capabilities to pick, plus an
// inline "+ new capability…" that defines a tag (stacked dialog) and grants
// it immediately — the zero-capability workspace is never a dead end.
function grantButton(res: Resource): HTMLElement {
  const have = new Set(res.capabilities ?? []);
  const options = store.capabilities.filter((c) => !have.has(c.id));
  const grant = async (capId: string) => {
    try { await api.grantCapability(res.id, capId); await store.refresh(); } catch (e) { showError(e); }
  };
  const sel = select([
    { value: '', label: '+ grant…' },
    ...options.map((c) => ({ value: c.id, label: c.name })),
    { value: '__new__', label: '+ new capability…' },
  ], '', (v) => {
    if (!v) return;
    if (v === '__new__') {
      sel.value = '';
      openCapabilityEditor(undefined, (c) => void grant(c.id));
      return;
    }
    void grant(v);
  });
  sel.className = 'grant-sel mut';
  return sel;
}

function toggleAvailability(res: Resource): void {
  const noteIn = h('input', {
    type: 'text', value: res.note ?? '',
    placeholder: res.available ? 'why unavailable? (maintenance, on leave…)' : 'note (optional)',
  });
  const body = h('div', null,
    field('Note', noteIn),
    res.available && res.allocated > 0
      ? h('p', { class: 'notice notice-warn' },
        `${res.allocated} unit(s) are allocated right now. They stay allocated — going unavailable never kicks off current work; it just stops new starts, and the affected work gets an over-allocated badge.`)
      : null,
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          try {
            await api.setAvailability(res.id, !res.available, noteIn.value.trim() || undefined);
            closeModal();
            await store.refresh();
          } catch (e) { showError(e); }
        },
      }, res.available ? 'Mark unavailable' : 'Mark available')));
  openModal(`${res.name}: availability`, body, { help: 'availability' });
}

export function openResourceEditor(existing?: Resource): void {
  const nameIn = h('input', { type: 'text', value: existing?.name ?? '', placeholder: 'name' });
  // Capacity applies to pools only: named ⇒ capacity 1 by invariant (§2.3,
  // §5.2 — the server enforces it; the payload below always sends 1 for
  // named regardless of field state). The pool value is remembered across
  // toggles so flipping to named and back loses nothing.
  let poolCapacity = existing && !existing.named ? existing.capacity : 1;
  const capIn = h('input', { type: 'number', min: '1', value: String(poolCapacity) });
  const capWrap = h('div');
  const renderCapacity = (named: boolean) => {
    capWrap.replaceChildren(named
      ? field('Capacity',
        h('input', { type: 'number', value: '1', disabled: true }),
        'named resources are a single unit — capacity is fixed at 1')
      : field('Capacity', capIn));
  };
  const namedSel = select([
    { value: 'pool', label: 'pool — interchangeable units (capacity N)' },
    { value: 'named', label: 'named — one specific unit (capacity 1, pinnable)' },
  ], existing?.named ? 'named' : 'pool', (v) => {
    if (v === 'named') {
      poolCapacity = Math.max(1, Number(capIn.value) || 1); // remember
    } else {
      capIn.value = String(poolCapacity); // restore
    }
    renderCapacity(v === 'named');
  });
  renderCapacity(existing?.named ?? false);

  // Optional resource type (categorization only), with the stacked "+ New
  // type…" dialog; the created type is selected here on success.
  const NEW_RT = '__new__';
  const rtOpts = () => [
    { value: '', label: '— none —' },
    ...store.resourceTypes.map((t) => ({ value: t.id, label: t.name })),
    { value: NEW_RT, label: '+ New type…' },
  ];
  const typeSel = select(rtOpts(), existing?.type ?? '');
  let lastType = typeSel.value;
  typeSel.addEventListener('change', () => {
    if (typeSel.value !== NEW_RT) { lastType = typeSel.value; return; }
    typeSel.value = lastType; // revert until the dialog succeeds
    openResourceTypeEditor(undefined, (rt) => {
      typeSel.replaceChildren(...rtOpts().map((o) =>
        h('option', { value: o.value, selected: o.value === rt.id }, o.label)));
      typeSel.value = rt.id;
      lastType = rt.id;
      typeSel.dispatchEvent(new Event('change')); // refresh declared fields
    });
  });

  // metadata: the resource TYPE's declared fields + free-form rows (untyped
  // resource → free-form only). Runs after the NEW_RT handler, which has
  // already reverted/settled typeSel.value.
  const rtFieldsOf = (rtId: string) => (rtId ? store.resourceType(rtId)?.fields ?? [] : []);
  const mf = metaForm(existing?.metadata, rtFieldsOf(typeSel.value));
  typeSel.addEventListener('change', () => {
    if (typeSel.value !== NEW_RT) mf.setFields(rtFieldsOf(typeSel.value));
  });

  const body = h('div', null,
    field('Name', nameIn),
    field('Shape', h('span', { class: 'row-help' }, namedSel, helpButton('poolVsNamed')), modelingGuidance()),
    capWrap,
    field('Type', typeSel, 'optional categorization (person, room, tool…) — the engine matches on capabilities, never on type'),
    h('details', { class: 'sub', open: Object.keys(existing?.metadata ?? {}).length > 0 || rtFieldsOf(typeSel.value).length > 0 },
      h('summary', null, 'Metadata'),
      mf.el),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          const name = nameIn.value.trim();
          if (!name) { toast('Name is required.', 'error'); return; }
          const named = namedSel.value === 'named';
          const metaErr = mf.firstError();
          if (metaErr) { toast(metaErr, 'error', 7000); return; }
          const md = mf.read();
          // Supersession is full replacement (DESIGN §5.2). The dialog now
          // EDITS metadata (declared fields + free-form rows show all of
          // it), so what it submits is the complete new version.
          const data = {
            name, kind: 'reusable', named,
            capacity: named ? 1 : Math.max(1, Number(capIn.value) || 1),
            ...(typeSel.value ? { type: typeSel.value } : {}),
            ...(Object.keys(md).length > 0 ? { metadata: md } : {}),
          };
          try {
            if (existing) await api.updateResource(existing.id, data, existing.version);
            else await api.createResource(data);
            closeModal();
            toast(existing ? `${name} updated` : `${name} created`, 'ok', 2500);
            await store.refresh();
          } catch (e) { showError(e); }
        },
      }, existing ? 'Save' : 'Create')));
  openModal(existing ? `Edit ${existing.name}` : 'New resource', body, { wide: true, help: 'resourceEditor' });
  nameIn.focus();
}
