// ui/fieldsEditor.ts — compact list editor for a type's declared metadata
// fields (§5.3): add/remove/reorder rows of key, label, kind, options (for
// select), required. Used by the thing-type and resource-type dialogs.
// Supersession is full replacement: zero rows = the payload omits `fields`,
// dropping all declarations.

import { MetadataField } from '../api';
import { h, select } from '../dom';

interface Row {
  key: string;
  label: string;
  kind: MetadataField['kind'];
  options: string; // comma-separated while editing
  required: boolean;
  error?: string;
}

/** FieldsRead is the validated result: `error` set (and shown inline) when
 * the declarations cannot be submitted — duplicate keys, or a select with no
 * usable options. The server's 400 remains the backstop. */
export interface FieldsRead {
  fields?: MetadataField[];
  error?: string;
}

export function fieldsEditor(initial: MetadataField[]): { el: HTMLElement; read(): FieldsRead } {
  const rows: Row[] = initial.map((f) => ({
    key: f.key, label: f.label ?? '', kind: f.kind,
    options: (f.options ?? []).join(', '), required: f.required ?? false,
  }));
  const body = h('div', { class: 'fields-rows' });

  const render = () => {
    body.replaceChildren();
    rows.forEach((row, i) => {
      const keyIn = h('input', {
        type: 'text', value: row.key, placeholder: 'key', class: 'in-fkey',
        oninput: () => { row.key = keyIn.value; },
      });
      const labelIn = h('input', {
        type: 'text', value: row.label, placeholder: 'label (optional)', class: 'in-flabel',
        oninput: () => { row.label = labelIn.value; },
      });
      const optsIn = h('input', {
        type: 'text', value: row.options, placeholder: 'options, comma-separated', class: 'in-fopts',
        oninput: () => { row.options = optsIn.value; },
      });
      const kindSel = select(
        (['text', 'number', 'date', 'select'] as const).map((k) => ({ value: k, label: k })),
        row.kind,
        (v) => { row.kind = v as Row['kind']; render(); });
      const reqCb = h('input', {
        type: 'checkbox', checked: row.required, title: 'required — a form hint, never enforced',
        onchange: () => { row.required = reqCb.checked; },
      });
      body.appendChild(h('div', { class: 'field-row' + (row.error ? ' field-row-err' : '') },
        h('span', { class: 'field-move' },
          h('button', {
            class: 'btn btn-ghost btn-sm', title: 'move up', disabled: i === 0,
            onclick: () => { [rows[i - 1], rows[i]] = [rows[i]!, rows[i - 1]!]; render(); },
          }, '↑'),
          h('button', {
            class: 'btn btn-ghost btn-sm', title: 'move down', disabled: i === rows.length - 1,
            onclick: () => { [rows[i], rows[i + 1]] = [rows[i + 1]!, rows[i]!]; render(); },
          }, '↓')),
        keyIn, labelIn, kindSel,
        row.kind === 'select' ? optsIn : null,
        h('label', { class: 'field-req', title: 'required — a form hint, never enforced' }, reqCb, '*'),
        h('button', { class: 'btn btn-ghost', title: 'remove field', onclick: () => { rows.splice(i, 1); render(); } }, '×')));
      if (row.error) {
        body.appendChild(h('div', { class: 'field-row-errmsg tiny' }, '⚠ ' + row.error));
      }
    });
  };
  render();

  // validate marks broken rows inline and returns the first problem: keys
  // must be unique, a select needs at least one non-blank option.
  const validate = (): string | undefined => {
    let first: string | undefined;
    const seen = new Set<string>();
    for (const row of rows) {
      row.error = undefined;
      const key = row.key.trim();
      if (!key) continue; // blank rows are dropped, not errors
      if (seen.has(key)) {
        row.error = `duplicate key “${key}” — field keys must be unique`;
      } else if (row.kind === 'select'
        && row.options.split(',').map((s) => s.trim()).filter(Boolean).length === 0) {
        row.error = 'a select field needs at least one option (comma-separated)';
      }
      seen.add(key);
      first = first ?? row.error;
    }
    render();
    return first;
  };

  const el = h('div', { class: 'fields-editor' },
    h('span', { class: 'field-label' }, 'Metadata fields ',
      h('span', { class: 'muted tiny' }, '(form-driving declarations — instances are never validated against them)')),
    body,
    h('button', {
      class: 'btn btn-ghost',
      onclick: () => { rows.push({ key: '', label: '', kind: 'text', options: '', required: false }); render(); },
    }, '+ field'));

  return {
    el,
    read: (): FieldsRead => {
      const err = validate();
      if (err) return { error: err };
      const out: MetadataField[] = [];
      for (const row of rows) {
        const key = row.key.trim();
        if (!key) continue; // blank rows are dropped, not errors
        const f: MetadataField = { key, kind: row.kind };
        if (row.label.trim()) f.label = row.label.trim();
        if (row.required) f.required = true;
        if (row.kind === 'select') {
          f.options = row.options.split(',').map((s) => s.trim()).filter(Boolean);
        }
        out.push(f);
      }
      return { fields: out.length > 0 ? out : undefined };
    },
  };
}
