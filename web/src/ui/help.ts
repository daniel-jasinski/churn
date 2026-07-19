// ui/help.ts — the "?" help affordance: a small round button after a
// heading; click opens a structured help popup (Purpose / How to use /
// Components & what they mean). Copy lives in helpContent.ts.

import { h } from '../dom';
import { openModal, setModalHelpButton } from '../modal';
import { HELP } from './helpContent';

export function openHelp(topic: string): void {
  const t = HELP[topic];
  if (!t) return;
  openModal(t.title, h('div', { class: 'help-body' },
    h('p', { class: 'help-purpose' }, t.purpose),
    h('h4', null, 'How to use'),
    h('ul', null, ...t.how.map((line) => h('li', null, line))),
    h('h4', null, 'Components & what they mean'),
    h('dl', { class: 'help-dl' },
      ...t.components.flatMap(([term, def]) => [
        h('dt', null, term),
        h('dd', null, def),
      ]))), { wide: true });
}

/** helpButton renders the round "?" that opens the topic's popup. */
export function helpButton(topic: string): HTMLElement {
  return h('button', {
    class: 'help-btn',
    title: `What is this? (${HELP[topic]?.title ?? topic})`,
    onclick: (e: MouseEvent) => { e.preventDefault(); openHelp(topic); },
  }, '?');
}

// Dialogs get their "?" through openModal({help}) — injected here to keep
// modal.ts free of a help→modal→help import cycle.
setModalHelpButton(helpButton);
