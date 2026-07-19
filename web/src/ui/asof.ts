// ui/asof.ts — the §3.6 time-travel viewer: pick a past batch seq or
// timestamp; graph and tree render ?as_of= data read-only until exit.

import { field, h } from '../dom';
import { closeModal, openModal } from '../modal';
import { store } from '../store';

export function openAsOfPicker(): void {
  const seqIn = h('input', {
    type: 'number', min: '1', placeholder: `1 … ${store.workspace?.last_seq ?? ''}`,
  });
  const tsIn = h('input', { type: 'datetime-local' });
  const go = () => {
    const seq = seqIn.value.trim();
    if (seq) {
      store.setAsOf(seq);
    } else if (tsIn.value) {
      store.setAsOf(new Date(tsIn.value).toISOString());
    } else {
      return;
    }
    closeModal();
  };
  const body = h('div', null,
    h('p', { class: 'muted' },
      'Replay the log to a past moment. The cursor snaps down to the last complete batch ',
      'at or before it — graph and tree views then show that past state, read-only.'),
    field('Log position (seq)', seqIn, 'takes precedence if both are set'),
    field('… or a point in time', tsIn),
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: closeModal }, 'Cancel'),
      h('button', { class: 'btn btn-primary', onclick: go }, 'View the past')));
  openModal('Time travel (as-of)', body);
  seqIn.focus();
}

/** asOfButton is the toolbar entry point shown on graph/tree views. */
export function asOfButton(): HTMLElement {
  return h('button', { class: 'btn btn-ghost', onclick: openAsOfPicker, title: 'View a past state of the workspace (read-only)' }, '🕰 past…');
}
