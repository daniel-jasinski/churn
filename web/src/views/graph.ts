// views/graph.ts — per-project DAG (§4.1): dagre left-to-right, nodes colored
// by derived status, composites collapsible to rollup nodes with a progress
// pie, hover cone highlight, click details panel, and dependency editing.

import cytoscape from 'cytoscape';
import cytoscapeDagre from 'cytoscape-dagre';
import { api, Dependency, Graph, Thing } from '../api';
import { h, select, statusDot } from '../dom';
import { closeModal, openModal } from '../modal';
import { navigate } from '../router';
import { store } from '../store';
import { showError, toast } from '../toast';
import { asOfButton } from '../ui/asof';
import { badgeRow, reqChipsOf, stateChip, typeChip } from '../ui/bits';
import { openProjectEditor } from '../ui/projectEditor';
import { openThingEditor } from '../ui/thingEditor';
import { actionsFor, repropose, transitionTo } from '../ui/transition';

cytoscape.use(cytoscapeDagre);

// The status palette — keep in sync with the CSS custom properties.
const statusColors: Record<string, string> = {
  blocked: '#8b93a1',
  ready: '#22c55e',
  resource_blocked: '#f59e0b',
  working: '#3b82f6',
  finished: '#64748b',
  held: '#a855f7',
  dropped: '#9f4444',
};

interface ViewState {
  project: string | null;
  collapsed: Set<string>; // composite ids the user keeps collapsed
  collapsedInit: boolean;
  drawFrom: string | null; // draw-edge mode: chosen source
  drawing: boolean;
  selected: { kind: 'thing' | 'dep'; id: string } | null;
  zoom?: number;
  pan?: { x: number; y: number };
}

const vs: ViewState = { project: null, collapsed: new Set(), collapsedInit: false, drawFrom: null, drawing: false, selected: null };
let cy: ReturnType<typeof cytoscape> | null = null;
let lastGraph: Graph | null = null;

export function renderGraph(root: HTMLElement, projectId?: string): void {
  if (!projectId) {
    renderPicker(root);
    return;
  }
  if (vs.project !== projectId) {
    vs.project = projectId;
    vs.collapsed = new Set();
    vs.collapsedInit = false;
    vs.selected = null;
    vs.zoom = undefined;
    vs.pan = undefined;
  }

  const toolbar = h('div', { class: 'toolbar' },
    select(store.projects.map((p) => ({ value: p.id, label: p.name })), projectId,
      (v) => navigate('graph', v)),
    h('button', {
      class: 'btn mut' + (vs.drawing ? ' btn-warn' : ''),
      title: 'Click a source node, then a target node, to assert a dependency',
      onclick: () => {
        vs.drawing = !vs.drawing;
        vs.drawFrom = null;
        renderGraph(root, projectId);
      },
    }, vs.drawing ? '✎ drawing… (Esc)' : '✎ Draw dependency'),
    h('button', { class: 'btn mut', onclick: () => openThingEditor(undefined, { project: projectId }) }, '+ Thing'),
    h('span', { class: 'spacer' }),
    h('span', { class: 'legend' },
      ...Object.entries(statusColors).map(([s]) => h('span', { class: 'legend-item' }, statusDot(s), s))),
    asOfButton());

  const canvas = h('div', { class: 'cy-canvas' });
  const panel = h('aside', { class: 'side-panel' });
  root.replaceChildren(toolbar, h('div', { class: 'graph-wrap' }, canvas, panel));

  void loadAndDraw(canvas, panel, projectId, root);
}

function renderPicker(root: HTMLElement): void {
  root.replaceChildren(
    h('div', { class: 'centered' },
      h('h2', null, 'Pick a project'),
      store.projects.length === 0
        ? h('p', { class: 'empty' }, 'No projects yet — every thing lives in one.')
        : h('ul', { class: 'picker' },
          ...store.projects.map((p) => h('li', null,
            h('a', { href: `#/graph/${p.id}` }, p.name)))),
      h('p', null, h('button', {
        class: 'btn btn-primary mut',
        onclick: () => openProjectEditor(undefined, (p) => navigate('graph', p.id)),
      }, '+ New project'))));
}

async function loadAndDraw(canvas: HTMLElement, panel: HTMLElement, projectId: string, root: HTMLElement): Promise<void> {
  let g: Graph;
  try {
    g = await api.graph(projectId, store.asOf ?? undefined);
  } catch (e) {
    canvas.replaceChildren(h('div', { class: 'empty' }, 'Cannot load graph: ' + String((e as Error).message)));
    return;
  }
  lastGraph = g;

  // Default: composites start collapsed (rollup nodes).
  if (!vs.collapsedInit) {
    for (const t of g.things) if (t.composite) vs.collapsed.add(t.id);
    vs.collapsedInit = true;
  }

  const byId = new Map(g.things.map((t) => [t.id, t]));
  const isCollapsed = (id: string) => vs.collapsed.has(id);

  // displayOf: the topmost collapsed ancestor (root side), else the thing.
  const displayOf = (id: string): string => {
    const chain: string[] = [];
    for (let cur: string | undefined = id; cur; cur = byId.get(cur)?.parent) chain.push(cur);
    for (let i = chain.length - 1; i >= 0; i--) {
      const cid = chain[i]!;
      const t = byId.get(cid);
      if (t?.composite && isCollapsed(cid)) return cid;
    }
    return id;
  };
  // compoundParentOf: nearest ancestor that renders as an expanded compound.
  const compoundParentOf = (id: string): string | undefined => {
    for (let cur = byId.get(id)?.parent; cur; cur = byId.get(cur)?.parent) {
      if (displayOf(cur) === cur && byId.get(cur)?.composite && !isCollapsed(cur)) return cur;
    }
    return undefined;
  };

  const visible = new Set<string>();
  for (const t of g.things) visible.add(displayOf(t.id));

  const nodes: Record<string, unknown>[] = [];
  for (const id of visible) {
    const t = byId.get(id);
    if (!t) continue;
    if (t.composite && !isCollapsed(id)) continue; // rendered as a compound parent below
    const collapsedComposite = t.composite && isCollapsed(id);
    const badges: string[] = [];
    if (t.badges.abandoned_dependency) badges.push('⚠');
    if (t.badges.finished_unsatisfied_deps) badges.push('⁉');
    if (t.badges.over_allocated) badges.push('▲');
    if (t.badges.allocations_out_of_step) badges.push('↻');
    if (t.has_abandoned) badges.push('✕');
    let label = t.name + (badges.length ? ' ' + badges.join('') : '');
    let pct = 0;
    if (collapsedComposite && t.progress) {
      label += `\n${t.progress.display}`;
      pct = t.progress.total > 0 ? (100 * t.progress.satisfied) / t.progress.total : 0;
    }
    nodes.push({
      data: {
        id, label, status: t.status, pct,
        composite: collapsedComposite ? 1 : 0,
        parent: compoundParentOf(id),
        held: t.status === 'held' ? 1 : 0,
      },
    });
  }
  // Expanded composites render as compound parents.
  for (const t of g.things) {
    if (t.composite && !isCollapsed(t.id) && displayOf(t.id) === t.id) {
      nodes.push({
        data: {
          id: t.id, label: `${t.name}  ${t.progress?.display ?? ''}`,
          status: t.status, pct: 0, composite: 2,
          parent: compoundParentOf(t.id),
        },
      });
      visible.add(t.id);
    }
  }

  // Edges: declared first (carry dependency identity), then inherited fills.
  type EdgeInfo = { from: string; to: string; dep?: Dependency; inherited: boolean };
  const edgeMap = new Map<string, EdgeInfo>();
  for (const d of g.dependencies) {
    const vf = visible.has(d.from) ? d.from : displayOf(d.from);
    const vt = visible.has(d.to) ? d.to : displayOf(d.to);
    if (!byId.has(vf) || !byId.has(vt) || vf === vt) continue;
    const key = vf + '→' + vt;
    if (!edgeMap.has(key)) edgeMap.set(key, { from: vf, to: vt, dep: d, inherited: false });
  }
  for (const e of g.edges) {
    const vf = displayOf(e.from), vt = displayOf(e.to);
    if (!byId.has(vf) || !byId.has(vt) || vf === vt) continue;
    const key = vf + '→' + vt;
    if (!edgeMap.has(key)) edgeMap.set(key, { from: vf, to: vt, inherited: true });
  }
  const edges = [...edgeMap.values()].map((e, i) => ({
    data: {
      id: 'e' + i, source: e.from, target: e.to,
      depId: e.dep?.id ?? '', inherited: e.inherited ? 1 : 0,
      satisfied: e.dep ? (e.dep.satisfied ? 1 : 0) : -1,
      tolerated: e.dep?.abandoned_tolerated ? 1 : 0,
      policy: e.dep?.on_abandoned ?? '',
    },
  }));

  const dark = matchMedia('(prefers-color-scheme: dark)').matches;
  const textColor = dark ? '#e5e7eb' : '#1f2430';
  const lineColor = dark ? '#4b5563' : '#c3c9d4';

  if (cy) { cy.destroy(); cy = null; }
  cy = cytoscape({
    container: canvas,
    elements: { nodes, edges },
    wheelSensitivity: 0.2,
    style: [
      {
        selector: 'node',
        style: {
          shape: 'round-rectangle',
          width: 'label', height: 'label',
          padding: '8px',
          label: 'data(label)',
          color: textColor,
          'font-size': 11,
          'text-wrap': 'wrap',
          'text-max-width': '140',
          'text-valign': 'center',
          'text-halign': 'center',
          'background-color': (n: { data: (k: string) => string }) => statusColors[n.data('status')] ?? '#888',
          'background-opacity': 0.22,
          'border-width': 2,
          'border-color': (n: { data: (k: string) => string }) => statusColors[n.data('status')] ?? '#888',
        },
      },
      { selector: 'node[held = 1]', style: { 'border-style': 'dashed' } },
      {
        selector: 'node[composite = 1]', // collapsed rollup node with pie
        style: {
          shape: 'ellipse',
          'pie-size': '92%',
          'pie-1-background-color': statusColors['ready']!,
          'pie-1-background-size': 'data(pct)',
          'pie-1-background-opacity': 0.35,
          'border-width': 3,
          'border-style': 'double',
        },
      },
      {
        selector: 'node[composite = 2]', // expanded compound parent
        style: {
          shape: 'round-rectangle',
          'background-opacity': 0.06,
          'border-width': 1,
          'border-style': 'dashed',
          'text-valign': 'top',
          'text-halign': 'center',
          'font-size': 10,
        },
      },
      {
        selector: 'edge',
        style: {
          width: 1.6,
          'curve-style': 'bezier',
          'target-arrow-shape': 'triangle',
          'arrow-scale': 0.9,
          'line-color': lineColor,
          'target-arrow-color': lineColor,
        },
      },
      { selector: 'edge[satisfied = 1]', style: { 'line-opacity': 0.45 } },
      { selector: 'edge[tolerated = 1]', style: { 'line-color': '#f59e0b', 'target-arrow-color': '#f59e0b', 'line-style': 'dashed' } },
      { selector: 'edge[inherited = 1]', style: { 'line-style': 'dotted', 'line-opacity': 0.55, width: 1.1 } },
      { selector: '.faded', style: { opacity: 0.15 } },
      { selector: '.draw-src', style: { 'border-width': 4, 'border-color': '#f59e0b' } },
      { selector: ':selected', style: { 'overlay-color': '#3b82f6', 'overlay-opacity': 0.15, 'overlay-padding': 4 } },
    ],
  });

  // debug hook (console poking; nothing in the app reads it)
  (window as unknown as Record<string, unknown>)['__cy'] = cy;

  const layoutTargets = cy.nodes().filter((n: { isParent(): boolean }) => !n.isParent()).union(cy.edges());
  layoutTargets.layout({ name: 'dagre', rankDir: 'LR', nodeSep: 18, rankSep: 55, edgeSep: 8 }).run();
  if (vs.zoom !== undefined && vs.pan) {
    cy.zoom(vs.zoom);
    cy.pan(vs.pan);
  } else {
    cy.fit(undefined, 30);
  }
  cy.on('zoom pan', () => { vs.zoom = cy!.zoom(); vs.pan = cy!.pan(); });

  // hover cone: upstream = what it waits on (targets), downstream = what waits on it
  cy.on('mouseover', 'node', (ev: { target: { id(): string } }) => {
    if (vs.drawing) return;
    const id = ev.target.id();
    const keep = new Set<string>([id]);
    const walk = (start: string, dir: 'out' | 'in') => {
      const stack = [start];
      while (stack.length) {
        const cur = stack.pop()!;
        for (const e of edgeMap.values()) {
          const next = dir === 'out' ? (e.from === cur ? e.to : null) : (e.to === cur ? e.from : null);
          if (next && !keep.has(next)) { keep.add(next); stack.push(next); }
        }
      }
    };
    walk(id, 'out');
    walk(id, 'in');
    cy!.elements().forEach((el: { id(): string; isNode(): boolean; data(k: string): string; addClass(c: string): void }) => {
      const inCone = el.isNode()
        ? keep.has(el.id())
        : keep.has(el.data('source')) && keep.has(el.data('target'));
      if (!inCone) el.addClass('faded');
    });
  });
  cy.on('mouseout', 'node', () => cy!.elements().removeClass('faded'));

  cy.on('tap', 'node', (ev: { target: { id(): string } }) => {
    const id = ev.target.id();
    const t = byId.get(id);
    if (!t) return;
    if (vs.drawing) {
      if (!vs.drawFrom) {
        vs.drawFrom = id;
        cy!.$('#' + cssEscape(id)).addClass('draw-src');
        toast(`Source: ${t.name} — now click the target it depends on.`, 'info', 3000);
      } else if (vs.drawFrom !== id) {
        const from = vs.drawFrom;
        vs.drawFrom = null;
        vs.drawing = false;
        askOnAbandoned(from, id, root);
      }
      return;
    }
    vs.selected = { kind: 'thing', id };
    renderThingPanel(panel, t, g, root);
  });
  cy.on('dbltap', 'node', (ev: { target: { id(): string } }) => {
    if (vs.drawing) return;
    const id = ev.target.id();
    const t = byId.get(id);
    if (!t?.composite) return;
    if (vs.collapsed.has(id)) vs.collapsed.delete(id); else vs.collapsed.add(id);
    renderGraph(root, vs.project!);
  });
  cy.on('tap', 'edge', (ev: { target: { data(k: string): string } }) => {
    if (vs.drawing) return;
    const depId = ev.target.data('depId');
    if (!depId) {
      panel.replaceChildren(h('div', { class: 'panel-pad muted' },
        'An inherited edge (composite expansion, §2.1) — retract the declared edge it comes from.'));
      return;
    }
    vs.selected = { kind: 'dep', id: depId };
    renderDepPanel(panel, depId, g, root);
  });
  cy.on('tap', (ev: { target: unknown }) => {
    if (ev.target === cy) {
      vs.selected = null;
      panel.replaceChildren(defaultPanel(g));
    }
  });

  // restore selection / default panel
  if (vs.selected?.kind === 'thing' && byId.has(vs.selected.id)) {
    renderThingPanel(panel, byId.get(vs.selected.id)!, g, root);
  } else if (vs.selected?.kind === 'dep' && g.dependencies.some((d) => d.id === vs.selected!.id)) {
    renderDepPanel(panel, vs.selected.id, g, root);
  } else {
    panel.replaceChildren(defaultPanel(g));
  }

  document.onkeydown = (e) => {
    if (e.key === 'Escape' && vs.drawing) {
      vs.drawing = false;
      vs.drawFrom = null;
      renderGraph(root, projectId);
    }
  };
}

function cssEscape(s: string): string {
  return s.replace(/([^\w-])/g, '\\$1');
}

function defaultPanel(g: Graph): HTMLElement {
  return h('div', { class: 'panel-pad' },
    h('h3', null, g.project.name),
    g.as_of ? h('p', { class: 'notice' }, `As of seq ${g.as_of.seq} (${g.as_of.ts})`) : null,
    h('p', { class: 'muted' },
      `${g.things.length} things, ${g.dependencies.length} declared dependencies, ${g.edges.length} expanded leaf edges.`),
    h('p', { class: 'muted' }, 'Click a node or edge for details. Hover highlights the upstream/downstream cone. Double-click a composite to expand or collapse it — or use the button in its panel.'));
}

function renderThingPanel(panel: HTMLElement, t: Thing, g: Graph, root: HTMLElement): void {
  const byId = new Map(g.things.map((x) => [x.id, x]));
  const reqs = store.requirementsOf(t.id);

  // Blocked-by frontier: unsatisfied declared deps of t or its ancestors
  // (edges from an ancestor bind the whole subtree, §2.1).
  const ancestors = new Set<string>([t.id]);
  for (let cur = t.parent; cur; cur = byId.get(cur)?.parent) ancestors.add(cur);
  const frontier = g.dependencies.filter((d) => ancestors.has(d.from) && !d.satisfied);

  const chainList = (dep: Dependency, depth: number): HTMLElement => {
    const target = byId.get(dep.to) ?? store.thing(dep.to);
    const nested = depth < 6
      ? g.dependencies.filter((d) => d.from === dep.to && !d.satisfied)
      : [];
    const label = h('span', null,
      target ? statusDot(target.status) : null, ' ',
      target?.name ?? dep.to,
      dep.on_abandoned === 'block' ? h('span', { class: 'muted' }, ' [blocks on abandon]') : null);
    if (nested.length === 0) return h('li', null, label);
    return h('li', null,
      h('details', null,
        h('summary', null, label, h('span', { class: 'muted' }, ` — ${nested.length} deeper`)),
        h('ul', { class: 'frontier' }, ...nested.map((d) => chainList(d, depth + 1)))));
  };

  // "if this finishes, N become ready" — indicative, computed in the UI over
  // the expanded leaf edges (finished targets count as satisfied).
  const subtreeLeaves = new Set<string>();
  const collect = (id: string) => {
    const x = byId.get(id);
    if (!x) return;
    if (!x.composite) { subtreeLeaves.add(id); return; }
    for (const c of x.children ?? []) collect(c);
  };
  collect(t.id);
  const unlocked: Thing[] = [];
  if (t.status !== 'finished' && subtreeLeaves.size > 0) {
    const byFrom = new Map<string, string[]>();
    for (const e of g.edges) {
      const arr = byFrom.get(e.from) ?? [];
      arr.push(e.to);
      byFrom.set(e.from, arr);
    }
    for (const x of g.things) {
      if (x.composite || x.status !== 'blocked') continue;
      const targets = byFrom.get(x.id) ?? [];
      const unsat = targets.filter((to) => {
        const tt = byId.get(to);
        return tt && tt.status !== 'finished';
      });
      if (unsat.length > 0 && unsat.every((u) => subtreeLeaves.has(u))) unlocked.push(x);
    }
  }

  const actions = h('div', { class: 'panel-actions mut' });
  if (!t.composite) {
    for (const a of actionsFor(t)) {
      actions.append(h('button', {
        class: 'btn btn-sm', onclick: () => void transitionTo(t, a.semantic),
      }, a.label));
    }
    if (t.badges.allocations_out_of_step) {
      actions.append(h('button', { class: 'btn btn-sm btn-warn', onclick: () => void repropose(t) }, 'Re-propose'));
    }
  } else {
    actions.append(h('button', {
      class: 'btn btn-sm',
      onclick: () => {
        if (vs.collapsed.has(t.id)) vs.collapsed.delete(t.id); else vs.collapsed.add(t.id);
        renderGraph(root, vs.project!);
      },
    }, vs.collapsed.has(t.id) ? 'Expand' : 'Collapse'));
  }
  actions.append(
    h('button', { class: 'btn btn-sm', onclick: () => openThingEditor(t) }, 'Edit'),
    h('button', {
      class: 'btn btn-sm',
      title: 'Add a child step (offers the §2.1 conversion when this is a worked leaf)',
      onclick: () => openThingEditor(undefined, { project: t.project, parent: t.id }),
    }, '+ child'),
    h('button', {
      class: 'btn btn-sm btn-danger',
      onclick: async () => {
        try {
          await api.deleteThing(t.id);
          toast(`${t.name} retracted.`, 'ok');
          vs.selected = null;
          await store.refresh();
        } catch (e) { showError(e); }
      },
    }, 'Retract'));

  panel.replaceChildren(h('div', { class: 'panel-pad' },
    h('h3', null, t.name, ' ', ...badgeRow(t)),
    h('div', { class: 'panel-row' },
      statusDot(t.status), h('b', null, t.status.replaceAll('_', ' ')),
      t.composite ? h('span', { class: 'muted' }, ` rollup · ${t.progress?.display ?? ''}`) : null,
      t.status === 'held' ? h('span', { class: 'muted' }, t.resumable_now ? ' · resumable now' : ' · not resumable now') : null),
    h('div', { class: 'panel-row' }, typeChip(t.type), t.state ? stateChip(t.state) : null),
    t.metadata && Object.keys(t.metadata).length > 0
      ? h('table', { class: 'kv' }, h('tbody', null, ...Object.entries(t.metadata).map(([k, v]) =>
        h('tr', null, h('td', { class: 'muted' }, k), h('td', null, typeof v === 'string' ? v : JSON.stringify(v))))))
      : null,
    reqs.length > 0 ? h('div', { class: 'panel-row' }, h('span', { class: 'muted' }, 'needs '), ...reqChipsOf(reqs)) : null,
    actions,
    frontier.length > 0
      ? h('div', { class: 'panel-sec' },
        h('h4', null, 'Blocked by (nearest frontier)'),
        h('ul', { class: 'frontier' }, ...frontier.map((d) => chainList(d, 0))))
      : null,
    unlocked.length > 0
      ? h('div', { class: 'panel-sec' },
        h('h4', null, `If this finishes, ${unlocked.length} become dependency-ready`),
        h('p', { class: 'muted tiny' }, 'indicative — computed in the UI from the expanded leaf graph'),
        h('ul', null, ...unlocked.slice(0, 12).map((x) => h('li', null, x.name)),
          unlocked.length > 12 ? h('li', { class: 'muted' }, `… and ${unlocked.length - 12} more`) : null))
      : null,
    h('p', null, h('a', { href: `#/history/${t.id}` }, 'Full history →'))));
}

function renderDepPanel(panel: HTMLElement, depId: string, g: Graph, root: HTMLElement): void {
  const d = g.dependencies.find((x) => x.id === depId);
  if (!d) return;
  const from = store.thing(d.from), to = store.thing(d.to);
  panel.replaceChildren(h('div', { class: 'panel-pad' },
    h('h3', null, 'Dependency'),
    h('p', null, h('b', null, from?.name ?? d.from), ' depends on ', h('b', null, to?.name ?? d.to)),
    h('p', { class: 'muted' },
      `on_abandoned: ${d.on_abandoned} · ${d.satisfied ? 'satisfied' : 'unsatisfied'}`,
      d.abandoned_tolerated ? ' · satisfied only because abandoned work is tolerated (⚠)' : ''),
    h('div', { class: 'panel-actions mut' },
      h('button', {
        class: 'btn btn-sm btn-danger',
        onclick: async () => {
          try {
            await api.deleteDependency(d.id);
            toast('Dependency retracted.', 'ok');
            vs.selected = null;
            await store.refresh();
          } catch (e) { showError(e); }
        },
      }, 'Retract edge'))));
  void g; void root;
}

function askOnAbandoned(from: string, to: string, root: HTMLElement): void {
  const fromT = store.thing(from), toT = store.thing(to);
  const sel = select([
    { value: 'ignore', label: 'ignore — unblock with a warning badge (default)' },
    { value: 'block', label: 'block — stay blocked while abandoned' },
  ], 'ignore');
  const body = h('div', null,
    h('p', null, h('b', null, fromT?.name ?? from), ' will depend on ', h('b', null, toT?.name ?? to), '.'),
    h('p', { class: 'muted' }, 'If the target is abandoned, should this stay blocked?'),
    sel,
    h('div', { class: 'modal-actions' },
      h('button', { class: 'btn', onclick: () => { closeModal(); renderGraph(root, vs.project!); } }, 'Cancel'),
      h('button', {
        class: 'btn btn-primary',
        onclick: async () => {
          try {
            await api.createDependency({ from, to, on_abandoned: sel.value as 'block' | 'ignore' });
            closeModal();
            toast('Dependency asserted.', 'ok');
            await store.refresh();
          } catch (e) {
            closeModal();
            showError(e); // cycle errors render the offending expanded cycle ids
            renderGraph(root, vs.project!);
          }
        },
      }, 'Assert dependency')));
  openModal('New dependency', body);
}
