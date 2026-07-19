// toast.ts — transient notifications; errors funnel through here.

import { ApiError } from './api';
import { h } from './dom';

let holder: HTMLElement | null = null;

function ensureHolder(): HTMLElement {
  if (!holder) {
    holder = h('div', { class: 'toasts' });
    document.body.appendChild(holder);
  }
  return holder;
}

export function toast(message: string, kind: 'info' | 'error' | 'ok' = 'info', ms = 5000): void {
  const el = h('div', { class: `toast toast-${kind}` },
    h('span', null, message),
    h('button', { class: 'toast-x', onclick: () => el.remove() }, '×'));
  ensureHolder().appendChild(el);
  if (ms > 0) setTimeout(() => el.remove(), ms);
}

/** showError renders any thrown value as a user-facing error toast. */
export function showError(e: unknown): void {
  if (e instanceof ApiError) toast(e.friendly(), 'error', 8000);
  else toast(String(e), 'error', 8000);
  console.error(e);
}
