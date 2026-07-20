// views/settings.ts — the home for configure-once screens. These are workspace
// settings, not facts: nothing here is a log entry, and the sections are
// visited rarely enough that they do not earn a top-level nav slot.

import { h } from '../dom';
import { renderVocab } from './vocab';
import { renderWeights } from './weights';

const SECTIONS: [string, string, string][] = [
  ['weights', 'Weights', 'how the ready board ranks work'],
  ['vocab', 'Vocabulary', 'states, thing types, resource types, capabilities'],
];

/** sectionOf normalizes the route arg to a known section id. */
function sectionOf(arg?: string): string {
  return SECTIONS.some(([id]) => id === arg) ? arg! : 'weights';
}

export function renderSettings(root: HTMLElement, arg?: string): void {
  const active = sectionOf(arg);
  const body = h('div', { class: 'settings-body' });
  if (active === 'vocab') renderVocab(body);
  else renderWeights(body);

  root.replaceChildren(h('div', { class: 'settings-shell' },
    h('nav', { class: 'settings-nav' },
      h('h2', null, 'Settings'),
      ...SECTIONS.map(([id, label, hint]) =>
        h('a', {
          href: id === 'weights' ? '#/settings' : `#/settings/${id}`,
          class: id === active ? 'active' : '',
          title: hint,
        }, label))),
    body));
}
