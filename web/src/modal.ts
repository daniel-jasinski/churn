// modal.ts — stacked overlay modals; Escape or backdrop click closes the
// topmost one. Stacking exists for the small side-dialogs (new project from
// inside the thing editor, the §2.1 conversion offer) — the dialog closes
// back to the editor underneath instead of destroying it.

import { h } from './dom';
import type { HelpKey } from './ui/helpContent';

const stack: HTMLElement[] = [];

/** closeModal removes the topmost open modal and refocuses the one it
 * reveals — the Escape handler is per-overlay keydown, so without refocus
 * the revealed dialog would ignore Escape until clicked. */
export function closeModal(): void {
  stack.pop()?.remove();
  const top = stack[stack.length - 1];
  top?.querySelector<HTMLElement>('.modal')?.focus();
}

/** openModal shows `content` in an overlay (stacked over any open modal)
 * and returns the body element. `help` names a help topic: a "?" appears in
 * the title bar and opens the topic stacked over this dialog. The help
 * button is injected lazily (modalHelpButton) to avoid an import cycle. */
export let modalHelpButton: ((topic: HelpKey) => HTMLElement) | null = null;
export function setModalHelpButton(fn: (topic: HelpKey) => HTMLElement): void {
  modalHelpButton = fn;
}

export function openModal(title: string, content: HTMLElement, opts: { wide?: boolean; help?: HelpKey } = {}): HTMLElement {
  const box = h('div', { class: 'modal' + (opts.wide ? ' modal-wide' : '') },
    h('div', { class: 'modal-head' },
      h('h3', null, title, opts.help && modalHelpButton ? modalHelpButton(opts.help) : null),
      h('button', { class: 'btn btn-ghost', onclick: () => closeTop(), title: 'Close (Esc)' }, '×')),
    h('div', { class: 'modal-body' }, content));
  const overlay = h('div', {
    class: 'overlay',
    onclick: (e: MouseEvent) => { if (e.target === overlay) closeTop(); },
  }, box);
  // Escape is consumed, not just handled: it dismisses the dialog and
  // nothing else. Without this it bubbles on to document, where the graph's
  // cancel-edge-drawing listener sits — so closing a dialog opened while
  // draw mode was armed would silently cancel the draw too, one keypress
  // dismissing two things. A modal is modal; the key belongs to it.
  //
  // It closes the TOP of the stack, not necessarily this overlay — unlike
  // the backdrop click below, which must only close the dialog it belongs
  // to. Nothing traps focus, so Shift+Tab out of a stacked dialog's box
  // lands on the overlay underneath (they are siblings under body, and the
  // box is only tabIndex -1); Escape from there is still "close the dialog
  // I am looking at". Deferring to closeTop would swallow the key and
  // dismiss nothing.
  overlay.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    e.stopPropagation();
    closeModal();
  });
  document.body.appendChild(overlay);
  stack.push(overlay);
  box.tabIndex = -1;
  box.focus();
  return content;

  // closeTop closes this overlay only while it is the top of the stack — a
  // click on a backdrop underneath must never close the dialog above it.
  function closeTop(): void {
    if (stack[stack.length - 1] !== overlay) return;
    closeModal();
  }
}
