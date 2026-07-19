// views/settings.ts — the five §3.4 recommendation weights, with the formula
// shown; PUT is full replacement.

import { api, Weights } from '../api';
import { field, h } from '../dom';
import { store } from '../store';
import { showError, toast } from '../toast';
import { helpButton } from '../ui/help';

const LABELS: [keyof Weights, string, string][] = [
  ['immediate_unlock', 'w1 · immediate unlock', 'dependents made ready if this finishes'],
  ['downstream_reach', 'w2 · downstream reach', 'everything transitively waiting on it'],
  ['remaining_depth', 'w3 · remaining depth', 'keeps the longest chain moving'],
  ['waiting_age', 'w4 · waiting age', 'starvation credit — first claim on freed capacity'],
  ['scarcity_penalty', 'w5 · resource scarcity penalty', 'subtracted: prefer work that does not hog contended resources'],
];

export function renderSettings(root: HTMLElement): void {
  const w = store.weights;
  if (!w) {
    root.replaceChildren(h('div', { class: 'empty' }, 'Loading…'));
    return;
  }
  const inputs = new Map<keyof Weights, HTMLInputElement>();
  const rows = LABELS.map(([key, label, hint]) => {
    const input = h('input', { type: 'number', step: '0.1', min: '0', value: String(w[key]) });
    inputs.set(key, input);
    return field(label, input, hint);
  });

  root.replaceChildren(h('div', { class: 'settings' },
    h('h2', null, 'Recommendation weights', helpButton('weights')),
    h('pre', { class: 'formula' },
      'score = w1·immediate_unlock\n' +
      '      + w2·downstream_reach\n' +
      '      + w3·remaining_depth\n' +
      '      + w4·waiting_age\n' +
      '      − w5·resource_scarcity_penalty'),
    h('p', { class: 'muted' },
      'Live workspace settings, not facts: the log records decisions taken, never the advice. ',
      'Every ready-board score explains its own terms.'),
    ...rows,
    h('div', { class: 'modal-actions' },
      h('button', {
        class: 'btn btn-primary mut',
        onclick: async () => {
          const next = {} as Weights;
          for (const [key] of LABELS) {
            const v = Number(inputs.get(key)!.value);
            if (!Number.isFinite(v) || v < 0) {
              toast(`${key} must be a non-negative number (the scarcity penalty is already subtracted).`, 'error');
              return;
            }
            next[key] = v;
          }
          try {
            await api.putSettings(next);
            toast('Weights saved.', 'ok', 2500);
            await store.refresh();
          } catch (e) { showError(e); }
        },
      }, 'Save weights'))));
}
