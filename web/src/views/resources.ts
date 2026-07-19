// views/resources.ts — the resource board (§4.3): one row per resource with
// capacity bar, availability toggle + note, open allocations, and the queue
// of ready/resource-blocked things wanting it.

import { api, Resource, ResourceBoardRow } from '../api';
import { chip, field, h, select, statusDot } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';
import { showError, toast } from '../toast';
import { reqText } from '../ui/bits';

export function renderResources(root: HTMLElement): void {
  const toolbar = h('div', { class: 'toolbar' },
    h('h2', null, 'Resources'),
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
    body.replaceChildren(...rows.map(row));
  })();
}

function modelingGuidance(): string {
  return 'Modeling guidance (§2.3): if individuals within a group differ in skills, or you care which ' +
    'one did the work, model them as named resources sharing capability tags — fungibility then emerges ' +
    'from the capability match. Pools are for genuinely interchangeable units you don’t track individually.';
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
    title: res.available ? 'Mark unavailable (capacity counts as 0; open allocations stay — reality wins)' : 'Mark available',
    onclick: () => toggleAvailability(res),
  }, res.available ? 'available' : 'unavailable');

  return h('section', { class: 'res-row' + (res.available ? '' : ' res-down') },
    h('header', { class: 'res-head' },
      h('b', null, res.name),
      res.named ? chip('named', undefined, 'chip-dim') : chip(`pool ×${res.capacity}`, undefined, 'chip-dim'),
      res.over_allocated ? h('span', { class: 'badge badge-alert', title: 'allocated units exceed effective capacity (§2.5)' }, '▲ over-allocated') : null,
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

function grantButton(res: Resource): HTMLElement {
  const have = new Set(res.capabilities ?? []);
  const options = store.capabilities.filter((c) => !have.has(c.id));
  if (options.length === 0) return h('span');
  const sel = select([{ value: '', label: '+ grant…' },
    ...options.map((c) => ({ value: c.id, label: c.name }))], '', async (v) => {
    if (!v) return;
    try { await api.grantCapability(res.id, v); await store.refresh(); } catch (e) { showError(e); }
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
        `${res.allocated} unit(s) are allocated right now. They stay allocated — reality wins; the affected things get an over-allocated badge (§2.5).`)
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
  openModal(`${res.name}: availability`, body);
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
  const body = h('div', null,
    field('Name', nameIn),
    field('Shape', namedSel, modelingGuidance()),
    capWrap,
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          const name = nameIn.value.trim();
          if (!name) { toast('Name is required.', 'error'); return; }
          const named = namedSel.value === 'named';
          // Supersession is full replacement (DESIGN §5.2): fields this
          // dialog does not expose must be carried over or they are cleared.
          const data = {
            name, kind: 'reusable', named,
            capacity: named ? 1 : Math.max(1, Number(capIn.value) || 1),
            ...(existing?.type ? { type: existing.type } : {}),
            ...(existing?.metadata ? { metadata: existing.metadata } : {}),
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
  openModal(existing ? `Edit ${existing.name}` : 'New resource', body);
  nameIn.focus();
}
