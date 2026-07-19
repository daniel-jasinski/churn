// views/tree.ts — hierarchy/progress (§4.5): containment tree with progress
// bars, plus a hand-rolled squarified treemap (SVG) sized by subtree leaf
// count and colored by completion.

import { api, Thing } from '../api';
import { h, statusDot } from '../dom';
import { store } from '../store';
import { showError } from '../toast';
import { asOfButton } from '../ui/asof';
import { badgeRow, typeChip } from '../ui/bits';
import { openBulkAdd } from '../ui/bulkAdd';
import { helpButton } from '../ui/help';
import { projectSelect } from '../ui/projectSelect';
import { openThingEditor } from '../ui/thingEditor';

export function renderTree(root: HTMLElement): void {
  const toolbar = h('div', { class: 'toolbar' },
    h('h2', null, 'Hierarchy & progress'), helpButton('tree'),
    projectSelect({ allowAll: true, onPick: () => renderTree(root) }),
    h('span', { class: 'spacer' }),
    h('button', { class: 'btn mut', onclick: () => openBulkAdd(store.selectedProject || undefined) }, 'Bulk add'),
    asOfButton());
  const content = h('div', { class: 'tree-wrap' }, h('div', { class: 'empty' }, 'Loading…'));
  root.replaceChildren(toolbar, content);

  void (async () => {
    let things: Thing[];
    if (store.asOf) {
      // Only the graph endpoint supports as_of: assemble the past picture
      // from per-project graph snapshots (projects list is the present one;
      // projects that did not exist then 404 and are skipped).
      things = [];
      for (const p of store.projects) {
        try {
          const g = await api.graph(p.id, store.asOf);
          things.push(...g.things);
        } catch (e) {
          void e; // project absent at the cursor
        }
      }
    } else {
      things = store.things;
    }
    draw(content, things);
  })().catch(showError);
}

function draw(content: HTMLElement, things: Thing[]): void {
  if (things.length === 0) {
    content.replaceChildren(h('div', { class: 'empty' },
      'Nothing here yet — add things from the ready board, or bulk add a whole outline.'));
    return;
  }
  const byId = new Map(things.map((t) => [t.id, t]));
  const childrenOf = (id: string | undefined, project: string) =>
    things.filter((t) => t.project === project && (t.parent ?? undefined) === id);

  // leafCount for treemap sizing; completion for coloring.
  const leafCount = (t: Thing): number => {
    if (!t.composite) return 1;
    let n = 0;
    for (const c of childrenOf(t.id, t.project)) n += leafCount(c);
    return Math.max(n, 1);
  };

  const cols = h('div', { class: 'tree-cols' });
  const shownProjects = store.selectedProject
    ? store.projects.filter((p) => p.id === store.selectedProject)
    : store.projects;
  for (const p of shownProjects) {
    const roots = childrenOf(undefined, p.id);
    if (roots.length === 0 && shownProjects.length > 1) continue;
    cols.append(h('section', { class: 'tree-proj' },
      h('h3', null, h('a', { href: `#/graph/${p.id}` }, p.name)),
      h('div', { class: 'treemap-holder' }, treemap(roots, leafCount)),
      h('ul', { class: 'tree' }, ...roots.map((t) => treeNode(t, childrenOf)))));
  }
  content.replaceChildren(cols);
}

function treeNode(t: Thing, childrenOf: (id: string | undefined, project: string) => Thing[]): HTMLElement {
  const kids = t.composite ? childrenOf(t.id, t.project) : [];
  const head = h('div', { class: 'tree-row' },
    statusDot(t.status),
    h('span', { class: 'tree-name', onclick: () => openThingEditor(t) }, t.name),
    typeChip(t.type),
    ...badgeRow(t),
    t.composite && t.progress ? progressBar(t) : null,
    h('a', { class: 'tiny muted', href: `#/history/${t.id}` }, 'hist'));
  if (kids.length === 0) return h('li', null, head);
  return h('li', null,
    h('details', { open: true },
      h('summary', null, head),
      h('ul', { class: 'tree' }, ...kids.map((k) => treeNode(k, childrenOf)))));
}

function progressBar(t: Thing): HTMLElement {
  const p = t.progress!;
  if (p.total === 0) {
    // abandoned-only subtree: no denominator — display "—" (§3.5)
    return h('span', { class: 'pbar-wrap', title: 'all leaves abandoned — no denominator' },
      h('span', { class: 'pbar-dash' }, p.display));
  }
  const pct = (100 * p.satisfied) / p.total;
  return h('span', { class: 'pbar-wrap', title: `${p.display} leaves satisfied${p.has_abandoned ? ' · subtree has abandoned work' : ''}` },
    h('span', { class: 'pbar' }, h('span', { class: 'pbar-fill', style: { width: `${pct}%` } })),
    h('span', { class: 'pbar-text' }, p.display));
}

// ── squarified treemap (Bruls/Huizing/van Wijk), hand-rolled, SVG ──

interface Cell { thing: Thing; value: number; x: number; y: number; w: number; hh: number }

function treemap(roots: Thing[], leafCount: (t: Thing) => number): SVGElement {
  const W = 420, H = 160;
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.setAttribute('class', 'treemap');
  const items = roots.map((t) => ({ thing: t, value: leafCount(t) }))
    .filter((i) => i.value > 0)
    .sort((a, b) => b.value - a.value || a.thing.id.localeCompare(b.thing.id));
  if (items.length === 0) return svg;
  for (const c of squarify(items, 0, 0, W, H)) {
    const t = c.thing;
    const pr = t.progress;
    const ratio = t.composite
      ? (pr && pr.total > 0 ? pr.satisfied / pr.total : null)
      : (t.status === 'finished' ? 1 : t.status === 'dropped' ? null : 0);
    const rect = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
    rect.setAttribute('x', String(c.x + 1));
    rect.setAttribute('y', String(c.y + 1));
    rect.setAttribute('width', String(Math.max(0, c.w - 2)));
    rect.setAttribute('height', String(Math.max(0, c.hh - 2)));
    rect.setAttribute('rx', '3');
    rect.setAttribute('fill', ratio === null ? 'var(--tm-none)' : mix(ratio));
    rect.setAttribute('class', 'tm-cell');
    const title = document.createElementNS('http://www.w3.org/2000/svg', 'title');
    title.textContent = `${t.name}: ${t.composite ? (pr?.display ?? '') : t.status}` +
      `${ratio === null ? ' (—: abandoned only)' : ''} · ${c.value} leaf(s)`;
    rect.appendChild(title);
    svg.appendChild(rect);
    if (c.w > 46 && c.hh > 16) {
      const text = document.createElementNS('http://www.w3.org/2000/svg', 'text');
      text.setAttribute('x', String(c.x + 5));
      text.setAttribute('y', String(c.y + 13));
      text.setAttribute('class', 'tm-label');
      text.textContent = t.name.length > Math.floor(c.w / 6.5) ? t.name.slice(0, Math.floor(c.w / 6.5)) + '…' : t.name;
      svg.appendChild(text);
    }
  }
  return svg;
}

/** mix interpolates the completion color: 0 → neutral, 1 → green. */
function mix(ratio: number): string {
  const r = Math.round(148 + (34 - 148) * ratio);
  const g = Math.round(163 + (197 - 163) * ratio);
  const b = Math.round(184 + (94 - 184) * ratio);
  return `rgb(${r} ${g} ${b})`;
}

function squarify(items: { thing: Thing; value: number }[], x: number, y: number, w: number, hh: number): Cell[] {
  const total = items.reduce((s, i) => s + i.value, 0);
  const cells: Cell[] = [];
  let rest = [...items];
  let rx = x, ry = y, rw = w, rh = hh;
  let scale = (rw * rh) / total;

  while (rest.length > 0) {
    const vertical = rw < rh; // lay the row along the shorter side
    const side = vertical ? rw : rh;
    let row: { thing: Thing; value: number }[] = [];
    let rowSum = 0;
    let best = Infinity;
    for (let i = 0; i < rest.length; i++) {
      const cand = rest[i]!;
      const next = rowSum + cand.value;
      const worst = worstRatio(row.concat(cand), next, side, scale);
      if (worst > best && row.length > 0) break;
      row.push(cand);
      rowSum = next;
      best = worst;
    }
    const thick = (rowSum * scale) / side;
    let off = 0;
    for (const it of row) {
      const len = (it.value * scale) / thick;
      cells.push(vertical
        ? { thing: it.thing, value: it.value, x: rx + off, y: ry, w: len, hh: thick }
        : { thing: it.thing, value: it.value, x: rx, y: ry + off, w: thick, hh: len });
      off += len;
    }
    if (vertical) { ry += thick; rh -= thick; } else { rx += thick; rw -= thick; }
    rest = rest.slice(row.length);
    if (rest.length > 0 && rw > 0 && rh > 0) {
      scale = (rw * rh) / rest.reduce((s, i) => s + i.value, 0);
    }
  }
  return cells;
}

function worstRatio(row: { value: number }[], sum: number, side: number, scale: number): number {
  const thick = (sum * scale) / side;
  let worst = 0;
  for (const it of row) {
    const len = (it.value * scale) / thick;
    const ratio = Math.max(len / thick, thick / len);
    if (ratio > worst) worst = ratio;
  }
  return worst;
}
