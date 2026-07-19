// modal.ts — stacked overlay modals; Escape or backdrop click closes the
// topmost one. Stacking exists for the small side-dialogs (new project from
// inside the thing editor, the §2.1 conversion offer) — the dialog closes
// back to the editor underneath instead of destroying it.

import { h } from './dom';

const stack: HTMLElement[] = [];

/** closeModal removes the topmost open modal. */
export function closeModal(): void {
  stack.pop()?.remove();
}

/** openModal shows `content` in an overlay (stacked over any open modal)
 * and returns the body element. */
export function openModal(title: string, content: HTMLElement, opts: { wide?: boolean } = {}): HTMLElement {
  const box = h('div', { class: 'modal' + (opts.wide ? ' modal-wide' : '') },
    h('div', { class: 'modal-head' },
      h('h3', null, title),
      h('button', { class: 'btn btn-ghost', onclick: () => closeTop(), title: 'Close (Esc)' }, '×')),
    h('div', { class: 'modal-body' }, content));
  const overlay = h('div', {
    class: 'overlay',
    onclick: (e: MouseEvent) => { if (e.target === overlay) closeTop(); },
  }, box);
  overlay.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeTop(); });
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
