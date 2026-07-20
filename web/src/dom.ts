// dom.ts — tiny DOM helpers. Never innerHTML with user data: children are
// text nodes or elements, attributes set via properties/setAttribute.

type Child = Node | string | number | null | undefined | false | Child[];

export interface Attrs {
  class?: string;
  id?: string;
  title?: string;
  href?: string;
  type?: string;
  value?: string;
  placeholder?: string;
  name?: string;
  for?: string;
  min?: string;
  max?: string;
  step?: string;
  rows?: number;
  cols?: number;
  colspan?: number;
  disabled?: boolean;
  checked?: boolean;
  selected?: boolean;
  multiple?: boolean;
  open?: boolean;
  tabindex?: number;
  style?: Partial<CSSStyleDeclaration>;
  dataset?: Record<string, string>;
  onclick?: (e: MouseEvent) => void;
  oninput?: (e: Event) => void;
  onchange?: (e: Event) => void;
  onsubmit?: (e: SubmitEvent) => void;
  onkeydown?: (e: KeyboardEvent) => void;
  // aria-* and role reach the setAttribute default branch. Declared rather
  // than left to fall through an excess-property hole: esbuild strips types
  // without checking them, so an undeclared attribute is indistinguishable
  // from a typo'd one.
  role?: string;
  [ariaAttr: `aria-${string}`]: unknown;
}

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs?: Attrs | null,
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === null || v === false) continue;
      switch (k) {
        case 'class': el.className = v as string; break;
        case 'style': Object.assign(el.style, v as object); break;
        case 'dataset': Object.assign(el.dataset, v as object); break;
        case 'for': (el as unknown as HTMLLabelElement).htmlFor = v as string; break;
        case 'colspan': (el as unknown as HTMLTableCellElement).colSpan = v as number; break;
        case 'tabindex': el.tabIndex = v as number; break;
        case 'disabled': case 'checked': case 'selected': case 'multiple': case 'open':
          (el as unknown as Record<string, unknown>)[k] = true; break;
        default:
          if (k.startsWith('on') && typeof v === 'function') {
            el.addEventListener(k.slice(2), v as EventListener);
          } else {
            el.setAttribute(k, String(v));
          }
      }
    }
  }
  append(el, children);
  return el;
}

function append(el: Node, children: Child[]): void {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
}

/** clear removes all children of el and returns it. */
export function clear<T extends Element>(el: T): T {
  el.replaceChildren();
  return el;
}

/** chip renders a small colored tag; the color may be user vocabulary data
 * (an invalid CSS color assignment is simply inert). */
export function chip(text: string, color?: string, cls = ''): HTMLElement {
  const c = h('span', { class: 'chip ' + cls }, text);
  if (color) {
    c.style.setProperty('--chip', color);
    c.classList.add('chip-colored');
  }
  return c;
}

/** statusDot renders the derived-status indicator. */
export function statusDot(status: string): HTMLElement {
  return h('span', { class: `dot dot-${status}`, title: status });
}

/** badge renders one warning badge glyph with a tooltip. */
export function badge(glyph: string, title: string, cls = 'badge-warn'): HTMLElement {
  return h('span', { class: 'badge ' + cls, title }, glyph);
}

/** field wraps a label + control for forms. */
export function field(label: string, control: HTMLElement, hint?: string): HTMLElement {
  return h('label', { class: 'field' },
    h('span', { class: 'field-label' }, label),
    control,
    hint ? h('span', { class: 'field-hint' }, hint) : null,
  );
}

/** select builds a <select> from options [{value,label}], selecting `value`. */
export function select(
  options: { value: string; label: string }[],
  value?: string,
  onchange?: (v: string) => void,
): HTMLSelectElement {
  const s = h('select', null,
    ...options.map((o) => h('option', { value: o.value, selected: o.value === value }, o.label)));
  if (onchange) s.addEventListener('change', () => onchange(s.value));
  return s;
}

/** debounce returns f delayed by ms, collapsing bursts. */
export function debounce(f: () => void, ms: number): () => void {
  let t: number | undefined;
  return () => {
    if (t !== undefined) clearTimeout(t);
    t = window.setTimeout(f, ms);
  };
}
