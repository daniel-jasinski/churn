// ui/promotion.ts — the §2.1 one-click leaf→composite conversion affordance.
//
// Parenting a child under a leaf that carries state/requirements is rejected
// by the API (kind "containment", pointing at §2.1). This dialog offers the
// conversion: ONE atomic /batch that retracts the leaf's requirements,
// creates the "<name>-work" child step, re-asserts the requirements on it
// (via the "$N" placeholder for the child's minted id), and moves it into
// the leaf's former state — exactly the §2.1 event list. Then the original
// rejected operation is retried.

import { api, ApiError, BatchOp, Thing } from '../api';
import { h } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';
import { showError, toast } from '../toast';

/** isPromotionRejection detects the §2.1 parenting rejection for `parent`. */
export function isPromotionRejection(e: unknown, parentId: string): e is ApiError {
  return e instanceof ApiError && e.kind === 'containment'
    && e.ids.includes(parentId) && e.message.includes('§2.1');
}

/** offerPromotion shows the conversion dialog; on confirm it converts and
 * then calls `retry` (the original rejected operation). */
export function offerPromotion(parent: Thing, retry: () => Promise<void>): void {
  const reqs = store.requirementsOf(parent.id);
  const sem = store.semanticOf(parent);
  if (sem === 'active') {
    toast(`${parent.name} is being worked — pause it before converting it to a composite (§2.1).`, 'error', 8000);
    return;
  }
  const stateName = parent.state ? store.state(parent.state)?.name ?? parent.state : null;
  const workName = `${parent.name}-work`;
  const body = h('div', null,
    h('p', null,
      h('b', null, parent.name), ' is a worked leaf — composites carry no state or requirements. ',
      'Convert it by moving ',
      stateName ? h('span', null, 'its state (', h('b', null, stateName), ')') : h('span', null, 'its state'),
      reqs.length ? ` and ${reqs.length} requirement(s)` : '',
      ' onto a new child step named ', h('b', null, workName), '?'),
    h('p', { class: 'muted' },
      'The leaf’s history stays attached to it; the child step takes over the hands-on work. ',
      'This is the same pattern as the “final review child step” (§2.1).'),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          closeModal();
          try {
            await convert(parent, workName);
            toast(`${parent.name} converted: state and requirements moved onto ${workName}.`, 'ok');
            await store.refresh();
            await retry();
          } catch (e) {
            showError(e);
            void store.refresh();
          }
        },
      }, `Create ${workName}`)));
  openModal('Convert to composite', body);
}

async function convert(parent: Thing, workName: string): Promise<void> {
  const reqs = store.requirementsOf(parent.id);
  const sem = store.semanticOf(parent);

  // ONE batch (§2.1): retract the leaf's requirements, ensure it sits in a
  // pending state, create the child step, re-assert the requirements on it
  // (placeholder "$N" = the child create's index), and move the child into
  // the leaf's former state.
  const ops: BatchOp[] = reqs.map((r): BatchOp => ({ op: 'retract', kind: 'requirement', id: r.id }));
  if (parent.state && sem !== 'pending') {
    const pending = store.statesBySemantic('pending')[0];
    if (!pending) throw new Error('no pending-semantic state is defined');
    ops.push({ op: 'transition', kind: 'thing', id: parent.id, data: { state: pending.id } });
  }
  const childRef = '$' + ops.length;
  ops.push({
    op: 'create', kind: 'thing',
    data: { project: parent.project, name: workName, type: parent.type, parent: parent.id },
  });
  for (const r of reqs) {
    ops.push({
      op: 'create', kind: 'requirement',
      data: {
        thing: childRef, quantity: r.quantity,
        ...(r.resource ? { resource: r.resource } : { capabilities: r.capabilities ?? [] }),
      },
    });
  }
  if (parent.state) {
    ops.push({ op: 'transition', kind: 'thing', id: childRef, data: { state: parent.state } });
  }
  await api.batch('commit', ops);
}
