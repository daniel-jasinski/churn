// ui/metaform.ts — the shared metadata editor: declared fields of the
// selected type rendered as proper inputs (text/number/date/select — form-
// driving declarations, never engine-enforced), plus "Additional fields"
// free-form key/value rows for everything undeclared.
//
// Value handling on read(): number → JSON number, date → "yyyy-mm-dd"
// string, select → the option string, text → raw string; empty values are
// omitted. Required is a soft hint (a * on the label) — save is never
// blocked by it. Free-form values try JSON first, else stay strings.
//
// Honesty rules for values that predate (or defy) the declaration:
//   - A stored value that does not fit the declared kind is NEVER silently
//     dropped: it renders as an inline note ("doesn't fit number — Replace")
//     and an untouched save writes the ORIGINAL value through unchanged.
//   - A select value outside the current options renders as a real option
//     labeled "… (not in current options)", so display always matches what
//     a save would store.
//   - A free-form row whose key collides with a declared field is refused
//     visibly (inline error; firstError() blocks the save) — never a silent
//     last-one-wins merge.

import { MetadataField } from '../api';
import { h, select } from '../dom';

export interface MetaForm {
  /** el is the whole section (declared inputs + additional rows). */
  el: HTMLElement;
  /** setFields re-renders the declared part for a new type; values move
   * between the declared inputs and the free rows so nothing is lost. */
  setFields(fields: MetadataField[]): void;
  /** read assembles the flat metadata object; {} when everything is empty. */
  read(): Record<string, unknown>;
  /** firstError returns the blocking problem to show the user (declared-key
   * collision in the free rows), or null when the form can be saved. */
  firstError(): string | null;
}

export function metaForm(initial: Record<string, unknown> | undefined, initialFields: MetadataField[]): MetaForm {
  let declared: MetadataField[] = [];
  // declaredValues holds raw input strings for declared keys; originals the
  // untouched initial values (for byte-for-byte write-through when a value
  // does not fit its declared kind); replacing marks keys whose
  // non-conforming value the user chose to edit away.
  const declaredValues: Record<string, string> = {};
  const originals: Record<string, unknown> = {};
  const replacing = new Set<string>();
  const declaredBody = h('div', { class: 'meta-declared' });
  const freeBody = h('div', { class: 'kv-rows' });

  const rawOf = (v: unknown): string => (typeof v === 'string' ? v : JSON.stringify(v));

  // conforms: can the stored value be faithfully shown and re-saved by the
  // declared input widget? (Empty is always fine — the key is simply unset.)
  const conforms = (f: MetadataField, key: string): boolean => {
    const raw = declaredValues[key] ?? '';
    if (raw === '') return true;
    const orig = originals[key];
    switch (f.kind) {
      case 'number':
        return Number.isFinite(Number(raw));
      case 'date':
        return (orig === undefined || typeof orig === 'string') && /^\d{4}-\d{2}-\d{2}$/.test(raw);
      case 'select':
        // strings render (ghost option when off-list); structured values can't
        return orig === undefined || typeof orig === 'string';
      default:
        return true;
    }
  };

  const addFreeRow = (k = '', v = '') => {
    const keyIn = h('input', { type: 'text', value: k, placeholder: 'key' });
    const valIn = h('input', { type: 'text', value: v, placeholder: 'value (JSON or text)' });
    const err = h('span', { class: 'kv-err tiny' });
    const row = h('div', { class: 'kv-row' },
      h('span', { class: 'kv-inputs' }, keyIn, valIn,
        h('button', { class: 'btn btn-ghost', onclick: () => { row.remove(); checkCollisions(); }, title: 'remove' }, '×')),
      err);
    keyIn.addEventListener('input', checkCollisions);
    freeBody.appendChild(row);
  };

  // checkCollisions marks free rows whose key duplicates a declared field —
  // refused visibly, never silently merged (firstError blocks the save).
  const checkCollisions = () => {
    const declaredKeys = new Set(declared.map((f) => f.key));
    for (const row of Array.from(freeBody.children)) {
      const keyIn = row.querySelector('input');
      const err = row.querySelector('.kv-err');
      const k = keyIn?.value.trim() ?? '';
      const dup = k !== '' && declaredKeys.has(k);
      row.classList.toggle('kv-dup', dup);
      if (err) err.textContent = dup ? `“${k}” is a declared field above — set it there; rename or remove this row` : '';
    }
  };

  const renderDeclared = () => {
    declaredBody.replaceChildren();
    for (const f of declared) {
      const cur = declaredValues[f.key] ?? '';
      let input: HTMLElement;
      if (!conforms(f, f.key) && !replacing.has(f.key)) {
        // Non-conforming stored value: show it honestly; an untouched save
        // preserves it byte-for-byte. "Replace" switches to a fresh input.
        input = h('span', { class: 'meta-misfit' },
          h('span', { class: 'muted tiny' },
            `current value ${rawOf(originals[f.key] ?? cur)} doesn’t fit ${f.kind} — kept as-is unless you replace it `),
          h('button', {
            class: 'btn btn-sm',
            onclick: (e: MouseEvent) => {
              e.preventDefault();
              replacing.add(f.key);
              declaredValues[f.key] = '';
              renderDeclared();
            },
          }, 'Replace'));
      } else {
        switch (f.kind) {
          case 'number':
            input = h('input', {
              type: 'number', value: cur,
              oninput: (e: Event) => { declaredValues[f.key] = (e.target as HTMLInputElement).value; },
            });
            break;
          case 'date':
            input = h('input', {
              type: 'date', value: cur,
              oninput: (e: Event) => { declaredValues[f.key] = (e.target as HTMLInputElement).value; },
            });
            break;
          case 'select': {
            const opts = [...(f.options ?? [])];
            const offList = cur !== '' && !opts.includes(cur);
            input = select([
              { value: '', label: '—' },
              // ghost option: the display always matches what a save stores
              ...(offList ? [{ value: cur, label: `${cur} (not in current options)` }] : []),
              ...opts.map((o) => ({ value: o, label: o })),
            ], cur, (v) => { declaredValues[f.key] = v; });
            break;
          }
          default:
            input = h('input', {
              type: 'text', value: cur,
              oninput: (e: Event) => { declaredValues[f.key] = (e.target as HTMLInputElement).value; },
            });
        }
      }
      declaredBody.appendChild(h('label', { class: 'field meta-field' },
        h('span', { class: 'field-label' },
          (f.label || f.key) + (f.required ? ' *' : ''),
          f.label && f.label !== f.key ? h('span', { class: 'muted tiny meta-key' }, ` (${f.key})`) : null),
        input));
    }
  };

  const freeRowEntries = (): [string, string][] => {
    const out: [string, string][] = [];
    for (const row of Array.from(freeBody.children)) {
      const [kIn, vIn] = Array.from(row.querySelectorAll('input'));
      const k = kIn?.value.trim();
      if (k) out.push([k, vIn?.value ?? '']);
    }
    return out;
  };

  const setFields = (fields: MetadataField[]) => {
    const newKeys = new Set(fields.map((f) => f.key));
    // declared keys that stop being declared fall back to free rows
    for (const f of declared) {
      if (newKeys.has(f.key)) continue;
      if (!conforms(f, f.key) && !replacing.has(f.key)) {
        // untouched misfit: the ORIGINAL moves through, not a lossy raw
        addFreeRow(f.key, rawOf(originals[f.key]));
        delete declaredValues[f.key];
      } else if ((declaredValues[f.key] ?? '') !== '') {
        addFreeRow(f.key, declaredValues[f.key]!);
        delete declaredValues[f.key];
      }
      replacing.delete(f.key);
    }
    // newly declared keys adopt a matching free row's value
    for (const f of fields) {
      if (declaredValues[f.key] !== undefined) continue;
      for (const row of Array.from(freeBody.children)) {
        const [kIn, vIn] = Array.from(row.querySelectorAll('input'));
        if (kIn?.value.trim() === f.key) {
          const raw = vIn?.value ?? '';
          declaredValues[f.key] = raw;
          try { originals[f.key] = JSON.parse(raw); } catch { originals[f.key] = raw; }
          row.remove();
          break;
        }
      }
    }
    declared = fields;
    renderDeclared();
    checkCollisions();
  };

  const read = (): Record<string, unknown> => {
    const out: Record<string, unknown> = {};
    for (const [k, raw] of freeRowEntries()) {
      try { out[k] = JSON.parse(raw); } catch { out[k] = raw; }
    }
    for (const f of declared) {
      if (!conforms(f, f.key) && !replacing.has(f.key)) {
        // untouched non-conforming value: preserved byte-for-byte
        out[f.key] = originals[f.key];
        continue;
      }
      const raw = (declaredValues[f.key] ?? '').trim();
      if (raw === '') continue; // empty declared value: omit the key
      switch (f.kind) {
        case 'number': {
          const n = Number(raw);
          if (Number.isFinite(n)) out[f.key] = n;
          break;
        }
        default:
          out[f.key] = raw; // date "yyyy-mm-dd", select option, or text
      }
    }
    return out;
  };

  const firstError = (): string | null => {
    checkCollisions();
    const declaredKeys = new Set(declared.map((f) => f.key));
    for (const [k] of freeRowEntries()) {
      if (declaredKeys.has(k)) {
        return `Metadata key “${k}” duplicates a declared field — set the value in the form field above, and rename or remove the extra row.`;
      }
    }
    return null;
  };

  // seed: declared inputs from the initial fields, the rest as free rows
  const initKeys = new Set(initialFields.map((f) => f.key));
  for (const [k, v] of Object.entries(initial ?? {})) {
    originals[k] = v;
    if (initKeys.has(k)) declaredValues[k] = rawOf(v);
    else addFreeRow(k, rawOf(v));
  }
  declared = initialFields;
  renderDeclared();

  const el = h('div', { class: 'metaform' },
    declaredBody,
    h('div', { class: 'meta-free' },
      h('span', { class: 'field-label' }, 'Additional fields'),
      freeBody,
      h('button', { class: 'btn btn-ghost', onclick: () => addFreeRow() }, '+ key')));

  return { el, setFields, read, firstError };
}
