// modal.ts — one overlay modal at a time; Escape or backdrop click closes.

import { h } from './dom';

let current: HTMLElement | null = null;

export function closeModal(): void {
  if (current) {
    current.remove();
    current = null;
  }
}

/** openModal shows `content` in an overlay and returns the body element. */
export function openModal(title: string, content: HTMLElement, opts: { wide?: boolean } = {}): HTMLElement {
  closeModal();
  const box = h('div', { class: 'modal' + (opts.wide ? ' modal-wide' : '') },
    h('div', { class: 'modal-head' },
      h('h3', null, title),
      h('button', { class: 'btn btn-ghost', onclick: closeModal, title: 'Close (Esc)' }, '×')),
    h('div', { class: 'modal-body' }, content));
  const overlay = h('div', {
    class: 'overlay',
    onclick: (e: MouseEvent) => { if (e.target === overlay) closeModal(); },
  }, box);
  overlay.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeModal(); });
  document.body.appendChild(overlay);
  current = overlay;
  box.tabIndex = -1;
  box.focus();
  return content;
}
