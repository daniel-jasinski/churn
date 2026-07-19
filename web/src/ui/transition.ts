// ui/transition.ts — state transitions with the §2.5 propose→confirm flow.

import { api, ApiError, Proposal, Semantic, StateDef, Thing } from '../api';
import { h, select } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';
import { showError, toast } from '../toast';
import { reqText } from './bits';

/** semanticActions maps a thing's current situation to offered transitions. */
export function actionsFor(t: Thing): { label: string; semantic: Semantic; kind?: string }[] {
  if (t.composite) return [];
  const sem = store.semanticOf(t);
  switch (sem) {
    case undefined: // never started: statutes blocked/ready/resource_blocked
    case 'pending':
      return [{ label: 'Start', semantic: 'active' }];
    case 'active':
      return [
        { label: 'Finish', semantic: 'satisfied' },
        { label: 'Pause', semantic: 'paused' },
        { label: 'Abandon', semantic: 'abandoned' },
      ];
    case 'paused':
      return [
        { label: 'Resume', semantic: 'active' },
        { label: 'Abandon', semantic: 'abandoned' },
      ];
    case 'satisfied':
    case 'abandoned':
      return [{ label: 'Reopen', semantic: 'pending' }];
    default:
      return [];
  }
}

/** transitionTo runs the full flow: pick a state of the semantic (if several
 * are defined), then commit — via the proposal modal when entering active. */
export async function transitionTo(t: Thing, semantic: Semantic): Promise<void> {
  const states = store.statesBySemantic(semantic);
  if (states.length === 0) {
    toast(`No state with semantic "${semantic}" is defined — add one in the vocabulary.`, 'error');
    return;
  }
  let state = states[0]!;
  if (states.length > 1) {
    const picked = await pickState(semantic, states);
    if (!picked) return;
    state = picked;
  }
  await commitTransition(t, state);
}

function pickState(semantic: Semantic, states: StateDef[]): Promise<StateDef | null> {
  return new Promise((resolve) => {
    const sel = select(states.map((s) => ({ value: s.id, label: s.name })), states[0]!.id);
    const body = h('div', null,
      h('p', { class: 'muted' }, `Several states share the "${semantic}" semantic — pick the one to record.`),
      sel,
      h('div', { class: 'modal-actions' },
        h('button', { class: 'btn', onclick: () => { closeModal(); resolve(null); } }, 'Cancel'),
        h('button', {
          class: 'btn btn-primary',
          onclick: () => {
            const s = states.find((x) => x.id === sel.value) ?? null;
            closeModal();
            resolve(s);
          },
        }, 'Choose')));
    openModal('Choose state', body);
  });
}

async function commitTransition(t: Thing, state: StateDef): Promise<void> {
  const curSem = store.semanticOf(t);
  const entersActive = state.semantic === 'active' && curSem !== 'active';
  try {
    if (!entersActive) {
      if (curSem === 'satisfied' && state.semantic === 'pending') {
        toast(`Reopening ${t.name} — recorded as an ordinary transition.`, 'info');
      }
      const res = await api.transition(t.id, { state: state.id });
      if (res.committed) toast(`${t.name} → ${state.name}`, 'ok', 2500);
      await store.refresh();
      return;
    }
    // Propose leg.
    const res = await api.transition(t.id, { state: state.id });
    if (res.committed) { await store.refresh(); return; }
    if (res.proposal) showProposalModal(t, state, res.proposal);
  } catch (e) {
    showError(e);
  }
}

/** showProposalModal renders the assignment table; Confirm commits transition
 * + allocations as one batch. On 409 drift the fresh proposal replaces the
 * stale one with a notice (details.fresh_proposal). */
export function showProposalModal(t: Thing, state: StateDef, proposal: Proposal, notice?: string): void {
  const rows = proposal.allocations;
  const body = h('div', null,
    notice ? h('div', { class: 'notice notice-warn' }, notice) : null,
    h('p', null,
      'Starting ', h('b', null, t.name), ' as ', h('b', null, state.name),
      '. Proposed assignment (based on seq ', String(proposal.based_on_seq), '):'),
    rows.length === 0
      ? h('p', { class: 'muted' }, 'No requirements — nothing to allocate.')
      : h('table', { class: 'table' },
        h('thead', null, h('tr', null,
          h('th', null, 'Requirement'), h('th', null, 'Resource'), h('th', null, 'Units'))),
        h('tbody', null, ...rows.map((r) => {
          const req = store.requirements.find((x) => x.id === r.requirement);
          return h('tr', null,
            h('td', null, req ? reqText(req.quantity, req.capabilities, req.resource) : r.requirement),
            h('td', null, store.name(r.resource)),
            h('td', null, String(r.quantity)));
        }))),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          try {
            const res = await api.transition(t.id, { state: state.id, confirm: true, proposal: proposal.token });
            if (res.committed) {
              closeModal();
              toast(`${t.name} started (${res.opened?.length ?? 0} allocation(s) opened)`, 'ok');
              await store.refresh();
            }
          } catch (e) {
            if (e instanceof ApiError && 'fresh_proposal' in e.details) {
              const fresh = e.details['fresh_proposal'] as Proposal | null;
              if (fresh) {
                showProposalModal(t, state, fresh,
                  'The world drifted since this proposal was computed — review the fresh assignment below.');
              } else {
                closeModal();
                toast('The world drifted and no feasible assignment exists right now.', 'error', 8000);
              }
              void store.refresh();
              return;
            }
            showError(e);
          }
        },
      }, 'Confirm assignment')));
  openModal('Allocation proposal', body);
}

/** repropose runs the §2.5 one-click atomic reconciliation. */
export async function repropose(t: Thing): Promise<void> {
  try {
    const res = await api.repropose(t.id);
    if (res.committed) {
      toast(`Re-proposed: closed ${res.closed?.length ?? 0}, opened ${res.opened?.length ?? 0} allocation(s).`, 'ok');
    } else {
      toast('Nothing to reconcile.', 'info');
    }
    await store.refresh();
  } catch (e) {
    showError(e);
  }
}
