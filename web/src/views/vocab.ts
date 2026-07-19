// views/vocab.ts — the vocabulary manager (§5.3): states (semantic binding —
// locked while occupied), thing types, resource types, capability tags. All
// entries are ordinary entities: define / supersede / retract.

import { api, CapabilityDef, ResourceType, Semantic, StateDef, TypeDef } from '../api';
import { chip, field, h, select } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';
import { showError, toast } from '../toast';

const SEMANTICS: Semantic[] = ['pending', 'active', 'paused', 'satisfied', 'abandoned'];

export function renderVocab(root: HTMLElement): void {
  root.replaceChildren(h('div', { class: 'vocab-cols' },
    statesCol(), typesCol(), resourceTypesCol(), capsCol()));
}

function occupiedCount(stateId: string): number {
  return store.things.filter((t) => t.state === stateId).length;
}

function statesCol(): HTMLElement {
  return h('section', { class: 'vocab-col' },
    h('h2', null, 'States'),
    h('p', { class: 'muted tiny' },
      'Named states bind to one engine semantic. The semantic is locked while any thing is in the state; name, color and description change freely.'),
    h('ul', { class: 'vocab-list' }, ...store.states.map((s) => {
      const occ = occupiedCount(s.id);
      return h('li', null,
        chip(s.name, s.color, 'chip-state'),
        h('span', { class: 'muted' }, ` → ${s.semantic}`),
        occ > 0 ? h('span', { class: 'muted tiny', title: 'semantic locked while occupied' }, ` 🔒 ${occ} in it`) : null,
        h('span', { class: 'spacer' }),
        h('button', { class: 'btn btn-sm mut', onclick: () => stateEditor(s) }, 'edit'),
        h('button', { class: 'btn btn-sm btn-danger mut', onclick: () => void del(() => api.deleteState(s.id), s.name) }, '×'));
    })),
    h('button', { class: 'btn mut', onclick: () => stateEditor() }, '+ New state'));
}

function stateEditor(existing?: StateDef): void {
  const occ = existing ? occupiedCount(existing.id) : 0;
  const nameIn = h('input', { type: 'text', value: existing?.name ?? '' });
  const semSel = select(SEMANTICS.map((s) => ({ value: s, label: s })), existing?.semantic ?? 'pending');
  if (occ > 0) semSel.disabled = true;
  const colorIn = h('input', { type: 'color', value: existing?.color || '#888888' });
  const descIn = h('input', { type: 'text', value: existing?.description ?? '' });
  const body = h('div', null,
    field('Name', nameIn),
    field('Semantic', semSel, occ > 0
      ? `locked: ${occ} thing(s) are in this state — rebinding would break the active⇔allocations invariant`
      : 'what the engine does with things in this state'),
    field('Color', colorIn),
    field('Description', descIn),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          const data = {
            name: nameIn.value.trim(), semantic: semSel.value as Semantic,
            color: colorIn.value, description: descIn.value.trim() || undefined,
          };
          if (!data.name) { toast('Name is required.', 'error'); return; }
          try {
            if (existing) await api.updateState(existing.id, data, existing.version);
            else await api.createState(data);
            closeModal();
            await store.refresh();
          } catch (e) { showError(e); } // semantic_immutable gets its friendly text
        },
      }, existing ? 'Save' : 'Define')));
  openModal(existing ? `Edit state ${existing.name}` : 'New state', body);
}

function typesCol(): HTMLElement {
  return h('section', { class: 'vocab-col' },
    h('h2', null, 'Thing types'),
    h('p', { class: 'muted tiny' }, 'Types carry no engine meaning — they drive filtering, coloring and reporting.'),
    h('ul', { class: 'vocab-list' }, ...store.types.map((t) => h('li', null,
      chip(t.name, t.color, 'chip-type'),
      t.description ? h('span', { class: 'muted tiny' }, ' ' + t.description) : null,
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn btn-sm mut', onclick: () => openTypeEditor(t) }, 'edit'),
      h('button', { class: 'btn btn-sm btn-danger mut', onclick: () => void del(() => api.deleteType(t.id), t.name) }, '×')))),
    h('button', { class: 'btn mut', onclick: () => openTypeEditor() }, '+ New type'));
}

/** openTypeEditor defines or edits a thing type; onSaved runs after a
 * successful define + refresh (the thing editor uses it to resume). */
export function openTypeEditor(existing?: TypeDef, onSaved?: () => void): void {
  const nameIn = h('input', { type: 'text', value: existing?.name ?? '' });
  const colorIn = h('input', { type: 'color', value: existing?.color || '#6b7280' });
  const descIn = h('input', { type: 'text', value: existing?.description ?? '' });
  const body = h('div', null,
    field('Name', nameIn), field('Color', colorIn), field('Description', descIn),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          const data = { name: nameIn.value.trim(), color: colorIn.value, description: descIn.value.trim() || undefined };
          if (!data.name) { toast('Name is required.', 'error'); return; }
          try {
            if (existing) await api.updateType(existing.id, data, existing.version);
            else await api.createType(data);
            closeModal();
            await store.refresh();
            if (!existing && onSaved) onSaved();
          } catch (e) { showError(e); }
        },
      }, existing ? 'Save' : 'Define')));
  openModal(existing ? `Edit type ${existing.name}` : 'New type', body);
}

function resourceTypesCol(): HTMLElement {
  return h('section', { class: 'vocab-col' },
    h('h2', null, 'Resource types'),
    h('p', { class: 'muted tiny' },
      'Categorize resources (person, room, tool…) — display and filtering only; the engine matches on capabilities, never on type.'),
    h('ul', { class: 'vocab-list' }, ...store.resourceTypes.map((t) => h('li', null,
      chip(t.name, t.color, 'chip-type'),
      t.description ? h('span', { class: 'muted tiny' }, ' ' + t.description) : null,
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn btn-sm mut', onclick: () => openResourceTypeEditor(t) }, 'edit'),
      h('button', { class: 'btn btn-sm btn-danger mut', onclick: () => void del(() => api.deleteResourceType(t.id), t.name) }, '×')))),
    h('button', { class: 'btn mut', onclick: () => openResourceTypeEditor() }, '+ New resource type'));
}

/** openResourceTypeEditor defines or edits a resource type; onSaved runs
 * after a successful define + refresh (the resource dialog uses it). */
export function openResourceTypeEditor(existing?: ResourceType, onSaved?: (rt: ResourceType) => void): void {
  const nameIn = h('input', { type: 'text', value: existing?.name ?? '' });
  const colorIn = h('input', { type: 'color', value: existing?.color || '#6b7280' });
  const descIn = h('input', { type: 'text', value: existing?.description ?? '' });
  const body = h('div', null,
    field('Name', nameIn), field('Color', colorIn), field('Description', descIn),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          const data = { name: nameIn.value.trim(), color: colorIn.value, description: descIn.value.trim() || undefined };
          if (!data.name) { toast('Name is required.', 'error'); return; }
          try {
            let rt: ResourceType;
            if (existing) rt = await api.updateResourceType(existing.id, data, existing.version);
            else rt = await api.createResourceType(data);
            closeModal();
            await store.refresh();
            if (!existing && onSaved) onSaved(rt);
          } catch (e) { showError(e); }
        },
      }, existing ? 'Save' : 'Define')));
  openModal(existing ? `Edit resource type ${existing.name}` : 'New resource type', body);
  nameIn.focus();
}

function capsCol(): HTMLElement {
  return h('section', { class: 'vocab-col' },
    h('h2', null, 'Capabilities'),
    h('p', { class: 'muted tiny' },
      'Declared tags matched between requirements and resources — declared-before-use, so a typo can never silently break matching.'),
    h('ul', { class: 'vocab-list' }, ...store.capabilities.map((c) => h('li', null,
      chip(c.name, undefined, 'chip-cap'),
      c.description ? h('span', { class: 'muted tiny' }, ' ' + c.description) : null,
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn btn-sm mut', onclick: () => openCapabilityEditor(c) }, 'edit'),
      h('button', { class: 'btn btn-sm btn-danger mut', onclick: () => void del(() => api.deleteCapability(c.id), c.name) }, '×')))),
    h('button', { class: 'btn mut', onclick: () => openCapabilityEditor() }, '+ New capability'));
}

/** openCapabilityEditor defines or edits a capability tag; onSaved runs
 * after a successful define + refresh (the requirement editor and the
 * resource grant flow use it to pick up the fresh tag). */
export function openCapabilityEditor(existing?: CapabilityDef, onSaved?: (c: CapabilityDef) => void): void {
  const nameIn = h('input', { type: 'text', value: existing?.name ?? '' });
  const descIn = h('input', { type: 'text', value: existing?.description ?? '' });
  const body = h('div', null,
    field('Name', nameIn), field('Description', descIn),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          const data = { name: nameIn.value.trim(), description: descIn.value.trim() || undefined };
          if (!data.name) { toast('Name is required.', 'error'); return; }
          try {
            let c: CapabilityDef;
            if (existing) c = await api.updateCapability(existing.id, data, existing.version);
            else c = await api.createCapability(data);
            closeModal();
            await store.refresh();
            if (!existing && onSaved) onSaved(c);
          } catch (e) { showError(e); }
        },
      }, existing ? 'Save' : 'Define')));
  openModal(existing ? `Edit capability ${existing.name}` : 'New capability', body);
  nameIn.focus();
}

async function del(fn: () => Promise<unknown>, name: string): Promise<void> {
  try {
    await fn();
    toast(`${name} retracted.`, 'ok', 2500);
    await store.refresh();
  } catch (e) {
    showError(e); // retraction_blocked lists the referencing ids
  }
}
