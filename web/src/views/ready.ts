// views/ready.ts — the ready board (§4.2, the daily driver): columns
// Ready / Resource-blocked / In progress / Recently done.

import { NearReadyEntry, ReadyEntry, Thing } from '../api';
import { h, select, statusDot } from '../dom';
import { store } from '../store';
import { badgeRow, projectName, reqChips, reqChipsOf, scoreBlock, starveNote, thingLink, typeChip } from '../ui/bits';
import { openBulkAdd } from '../ui/bulkAdd';
import { helpButton } from '../ui/help';
import type { HelpKey } from '../ui/helpContent';
import { openProjectEditor } from '../ui/projectEditor';
import { projectSelect } from '../ui/projectSelect';
import { openThingEditor } from '../ui/thingEditor';
import { actionsFor, repropose, transitionTo } from '../ui/transition';

// module-level filter state survives re-renders; the project filter is the
// STICKY cross-tab selection (store.selectedProject).
const filter = { type: '', capability: '', text: '' };

export function renderReady(root: HTMLElement): void {
  if (store.projects.length === 0) {
    // Fresh workspace: point at the one action that unlocks everything else.
    root.replaceChildren(h('div', { class: 'centered onboard' },
      h('h2', null, 'Welcome to churn'),
      h('p', null, 'This workspace is empty. Work lives in ', h('b', null, 'projects'),
        ' — dependency graphs of things — worked with the shared ', h('b', null, 'resources'), '.'),
      h('p', null, h('button', {
        class: 'btn btn-primary mut',
        onclick: () => openProjectEditor(),
      }, 'Create your first project')),
      h('p', { class: 'muted' },
        'Then add things here (single or ', h('b', null, 'Bulk add'), '), declare resources on the ',
        h('a', { href: '#/resources' }, 'resource board'),
        ', and tune the vocabulary of states, types and capabilities under ',
        h('a', { href: '#/vocab' }, 'Vocab'), '. Sensible default states are already in place.')));
    return;
  }
  const projFilter = () => store.selectedProject;
  const toolbar = h('div', { class: 'toolbar' },
    projectSelect({ allowAll: true, onPick: () => renderReady(root) }),
    select([{ value: '', label: 'all types' },
      ...store.types.map((t) => ({ value: t.id, label: t.name }))],
    filter.type, (v) => { filter.type = v; renderReady(root); }),
    select([{ value: '', label: 'any capability' },
      ...store.capabilities.map((c) => ({ value: c.id, label: c.name }))],
    filter.capability, (v) => { filter.capability = v; renderReady(root); }),
    textFilter(root),
    h('span', { class: 'spacer' }),
    h('button', { class: 'btn mut', onclick: () => openBulkAdd(projFilter() || undefined) }, 'Bulk add'),
    h('button', { class: 'btn btn-primary mut', onclick: () => openThingEditor(undefined, { project: projFilter() || undefined }) }, '+ New thing'),
    helpButton('ready'));

  const matchThing = (t: Thing): boolean => {
    if (projFilter() && t.project !== projFilter()) return false;
    if (filter.type && t.type !== filter.type) return false;
    if (filter.text && !t.name.toLowerCase().includes(filter.text.toLowerCase())) return false;
    if (filter.capability) {
      const reqs = store.requirementsOf(t.id);
      if (!reqs.some((r) => r.capabilities?.includes(filter.capability))) return false;
    }
    return true;
  };
  const matchEntry = (e: ReadyEntry): boolean => {
    if (projFilter() && e.project !== projFilter()) return false;
    if (filter.type && e.type !== filter.type) return false;
    if (filter.capability && !e.requirements.some((r) => r.capabilities?.includes(filter.capability))) return false;
    const t = store.thing(e.thing);
    if (filter.text && t && !t.name.toLowerCase().includes(filter.text.toLowerCase())) return false;
    return true;
  };

  const ready = store.ready.filter(matchEntry); // API order: score desc, id
  const leaves = store.things.filter((t) => !t.composite);
  const resBlocked = leaves.filter((t) => t.status === 'resource_blocked' && matchThing(t));
  const working = leaves.filter((t) => t.status === 'working' && matchThing(t));
  const held = leaves.filter((t) => t.status === 'held' && matchThing(t));
  // "recently done": finished leaves, most recently touched first (version =
  // seq of last touching event — the recency key the log gives us).
  const done = leaves.filter((t) => t.status === 'finished' && matchThing(t))
    .sort((a, b) => b.version - a.version).slice(0, 15);

  // Almost ready (§3.2 inverse view): pending leaves whose minimal frontier
  // has at most nearN declared blockers. Shown as a full-width strip under
  // the four columns; the columns stay intact.
  const near = store.nearReady.filter((e) => {
    if (projFilter() && e.project !== projFilter()) return false;
    if (filter.type && e.type !== filter.type) return false;
    const t = store.thing(e.thing);
    if (filter.text && t && !t.name.toLowerCase().includes(filter.text.toLowerCase())) return false;
    return true;
  });
  const nearStrip = h('section', { class: 'near-strip' },
    h('header', { class: 'col-head' },
      h('h2', null, 'Almost ready', helpButton('almostReady')),
      h('span', { class: 'count' }, String(near.length)),
      h('span', { class: 'muted tiny' }, ' — pending things a few blockers away'),
      h('span', { class: 'spacer' }),
      h('label', { class: 'muted tiny near-ctl' }, 'blockers ≤ ',
        select([2, 3, 4, 5].map((n) => ({ value: String(n), label: String(n) })),
          String(store.nearN), (v) => {
            store.nearN = Number(v);
            void store.refresh();
          }))),
    near.length === 0
      ? h('div', { class: 'empty' }, `Nothing is within ${store.nearN} blocker(s) of ready.`)
      : h('div', { class: 'near-cards' }, ...near.map(nearCard)));

  const board = h('div', { class: 'board' },
    column('Ready', ready.length,
      ready.length === 0 ? readyEmptyState(leaves) : ready.map((e) => readyCard(e)),
      'col-ready', 'readyList'),
    column('Resource-blocked', resBlocked.length,
      resBlocked.length === 0 ? emptyNote('Nothing is waiting on resources.')
        : resBlocked.map((t) => thingCard(t, { showReqs: true })), 'col-rblocked', 'resourceBlocked'),
    column('In progress', working.length + held.length,
      working.length + held.length === 0 ? emptyNote('Nothing is being worked right now.')
        : [...working.map((t) => thingCard(t, { showAllocs: true })),
          ...held.map((t) => thingCard(t, { heldNote: true }))], 'col-working', 'inProgress'),
    column('Recently done', done.length,
      done.length === 0 ? emptyNote('Nothing finished yet.')
        : done.map((t) => thingCard(t, {})), 'col-done', 'recentlyDone'));

  root.replaceChildren(toolbar, board, nearStrip);
}

// nearCard renders one almost-ready entry: the thing plus its frontier
// members with their own derived statuses — a dropped blocker behind a
// block-policy edge renders exactly as dropped (the §2.2 warning case).
function nearCard(e: NearReadyEntry): HTMLElement {
  const t = store.thing(e.thing);
  if (!t) return h('span');
  return h('article', { class: 'card card-near' },
    h('div', { class: 'card-title' }, thingLink(t), ...badgeRow(t)),
    h('div', { class: 'card-meta' },
      typeChip(t.type),
      h('a', { class: 'muted proj-link', href: `#/graph/${t.project}` }, projectName(t.project))),
    h('div', { class: 'near-frontier' },
      h('span', { class: 'muted tiny' }, `waiting on ${e.count}: `),
      ...e.frontier.map((b) => {
        const bt = store.thing(b.thing);
        return h('span', { class: 'near-blocker', title: `${bt?.name ?? b.thing}: ${b.status.replaceAll('_', ' ')}` },
          statusDot(b.status),
          h('a', { href: `#/history/${b.thing}` }, bt?.name ?? b.thing),
          h('span', { class: 'muted tiny' }, ` (${b.status.replaceAll('_', ' ')})`));
      })));
}

function textFilter(root: HTMLElement): HTMLElement {
  const input = h('input', {
    type: 'search', placeholder: 'filter names ( / )', value: filter.text, class: 'search',
    oninput: () => { filter.text = input.value; rerenderColumnsOnly(root); },
  });
  input.dataset['hotkey'] = 'slash';
  return input;
}

// Re-render but keep focus in the search box.
function rerenderColumnsOnly(root: HTMLElement): void {
  const active = document.activeElement as HTMLInputElement | null;
  const pos = active?.selectionStart ?? null;
  renderReady(root);
  if (active?.dataset['hotkey'] === 'slash') {
    const fresh = root.querySelector<HTMLInputElement>('input[data-hotkey="slash"]');
    if (fresh) {
      fresh.focus();
      if (pos !== null) fresh.setSelectionRange(pos, pos);
    }
  }
}

function column(title: string, count: number, content: HTMLElement | HTMLElement[], cls: string, help: HelpKey): HTMLElement {
  return h('section', { class: 'col ' + cls },
    h('header', { class: 'col-head' },
      h('h2', null, title, helpButton(help)),
      h('span', { class: 'count' }, String(count))),
    h('div', { class: 'col-body' }, content));
}

function emptyNote(text: string): HTMLElement {
  return h('div', { class: 'empty' }, text);
}

function readyEmptyState(leaves: Thing[]): HTMLElement {
  const blocked = leaves.filter((t) => t.status === 'blocked').length;
  const rblocked = leaves.filter((t) => t.status === 'resource_blocked').length;
  const pendingAny = blocked + rblocked > 0;
  return h('div', { class: 'empty' },
    leaves.length === 0
      ? h('p', null, 'No work yet — create things with “+ New thing” or “Bulk add”.')
      : pendingAny
        ? h('p', null,
          'Nothing is ready: ',
          blocked > 0 ? `${blocked} thing(s) are blocked by dependencies` : null,
          blocked > 0 && rblocked > 0 ? ' and ' : null,
          rblocked > 0 ? `${rblocked} are waiting on resources` : null,
          '. See ', h('a', { href: '#/bottlenecks' }, 'bottlenecks'), ' for where the flow is stuck.')
        : h('p', null, 'All pending work is done, being worked, or on hold. 🎉'));
}

function readyCard(e: ReadyEntry): HTMLElement {
  const t = store.thing(e.thing);
  if (!t) return h('div');
  const card = baseCard(t);
  card.append(
    h('div', { class: 'card-reqs' }, reqChips(e.requirements)),
    scoreBlock(e.score),
    starveNote(e.score) ?? '',
    h('div', { class: 'card-actions mut' },
      h('button', {
        class: 'btn btn-primary btn-sm',
        onclick: () => void transitionTo(t, 'active'),
      }, 'Start'),
      h('button', { class: 'btn btn-sm', onclick: () => openThingEditor(t) }, 'Edit'),
      h('button', {
        class: 'btn btn-sm', title: 'Edit what this thing depends on',
        onclick: () => openThingEditor(t, { focus: 'deps' }),
      }, 'Deps')));
  return card;
}

function thingCard(t: Thing, opts: { showReqs?: boolean; showAllocs?: boolean; heldNote?: boolean }): HTMLElement {
  const card = baseCard(t);
  if (opts.showReqs) {
    card.append(h('div', { class: 'card-reqs' }, reqChipsOf(store.requirementsOf(t.id))));
  }
  if (opts.heldNote) {
    card.append(h('div', { class: 'held-note' },
      t.resumable_now ? '⏸ held — resumable now' : '⏸ held — resources NOT free to resume'));
  }
  const actions = h('div', { class: 'card-actions mut' });
  for (const a of actionsFor(t)) {
    actions.append(h('button', {
      class: 'btn btn-sm' + (a.label === 'Start' || a.label === 'Finish' ? ' btn-primary' : ''),
      onclick: () => void transitionTo(t, a.semantic),
    }, a.label));
  }
  if (t.badges.allocations_out_of_step) {
    actions.append(h('button', {
      class: 'btn btn-sm btn-warn', title: 'Swap outdated allocations for a fresh feasible set in one atomic step',
      onclick: () => void repropose(t),
    }, 'Re-propose'));
  }
  actions.append(
    h('button', { class: 'btn btn-sm', onclick: () => openThingEditor(t) }, 'Edit'),
    h('button', {
      class: 'btn btn-sm', title: 'Edit what this thing depends on',
      onclick: () => openThingEditor(t, { focus: 'deps' }),
    }, 'Deps'));
  card.append(actions);
  return card;
}

function baseCard(t: Thing): HTMLElement {
  return h('article', { class: `card card-${t.status}` },
    h('div', { class: 'card-title' },
      thingLink(t),
      ...badgeRow(t)),
    h('div', { class: 'card-meta' },
      typeChip(t.type),
      h('a', { class: 'muted proj-link', href: `#/graph/${t.project}` }, projectName(t.project)),
      t.state ? h('span', { class: 'muted' }, store.state(t.state)?.name ?? '') : null));
}
