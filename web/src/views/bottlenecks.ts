// views/bottlenecks.ts — the §3.3 dashboard: contention (authoritative unmet
// units prominent, attribution labeled indicative, tag ratios labeled
// heuristic), criticality (three separated numbers, never summed), and the
// starvation list (current stint + cumulative credit).

import { api, Bottlenecks, Criticality } from '../api';
import { h } from '../dom';
import { duration, num } from '../fmt';
import { store } from '../store';
import { thingLink } from '../ui/bits';
import { helpButton } from '../ui/help';

type CritKey = 'downstream_reach' | 'immediate_unlock' | 'remaining_depth';
let critSort: CritKey = 'downstream_reach';

export function renderBottlenecks(root: HTMLElement): void {
  root.replaceChildren(h('div', { class: 'empty' }, 'Loading…'));
  void (async () => {
    let b: Bottlenecks;
    try {
      b = await api.bottlenecks();
    } catch (e) {
      root.replaceChildren(h('div', { class: 'empty' }, String((e as Error).message)));
      return;
    }
    root.replaceChildren(
      h('div', { class: 'dash' },
        contentionSection(b),
        criticalitySection(b, root),
        starvationSection(b)));
  })();
}

function contentionSection(b: Bottlenecks): HTMLElement {
  const c = b.contention;
  return h('section', { class: 'dash-sec' },
    h('h2', null, 'Resource contention', helpButton('contention')),
    h('div', { class: 'stat-row' },
      stat(String(c.unmet), 'unmet requirement units', 'trustworthy — computed by actually trying every assignment of demand onto free units', c.unmet > 0 ? 'stat-bad' : 'stat-ok'),
      stat(String(c.demand), 'units demanded', 'ready + frontier requirement units'),
      stat(String(c.matched), 'units matchable now', '')),
    c.signatures.length === 0
      ? h('p', { class: 'empty' }, 'No open demand — nothing is contending for resources.')
      : h('div', null,
        h('h3', null, 'By requirement signature ',
          h('span', { class: 'label-indicative' }, 'attribution: indicative'),
          h('span', { class: 'muted tiny' }, ' — per-signature split depends on matching tie-breaks; the total above is the honest number')),
        h('table', { class: 'table' },
          h('thead', null, h('tr', null,
            h('th', null, 'signature'), h('th', null, 'demand'), h('th', null, 'matched'),
            h('th', null, 'unmet'), h('th', null, 'pressure'), h('th', null, 'wanted by'))),
          h('tbody', null, ...c.signatures.map((s) => h('tr', s.unmet > 0 ? { class: 'row-hot' } : null,
            h('td', null, sigLabel(s.signature)),
            h('td', null, String(s.demand)),
            h('td', null, String(s.matched)),
            h('td', null, h('b', null, String(s.unmet))),
            h('td', null, num(s.pressure)),
            h('td', { class: 'muted' }, s.things.map((t) => store.name(t)).join(', '))))))),
    c.tag_ratios.length > 0
      ? h('details', { class: 'sub' },
        h('summary', null, 'Per-capability demand/free ratios ',
          h('span', { class: 'label-heuristic' }, 'heuristic'),
          h('span', { class: 'muted tiny' }, ' — double-counts multi-capability units and ignores conjunctions')),
        h('table', { class: 'table' },
          h('thead', null, h('tr', null,
            h('th', null, 'capability'), h('th', null, 'demand'), h('th', null, 'free'), h('th', null, 'ratio'))),
          h('tbody', null, ...c.tag_ratios.map((t) => h('tr', null,
            h('td', null, store.name(t.capability)),
            h('td', null, String(t.demand_units)),
            h('td', null, String(t.free_units)),
            h('td', null, t.ratio === null ? '∞ (no free units)' : num(t.ratio)))))))
      : null);
}

function sigLabel(sig: string): string {
  // Signatures are capability-id sets or pins; resolve ids to names.
  return sig.split('+').map((part) => store.name(part.replace(/^pin:/, '')))
    .join('+') + (sig.startsWith('pin:') ? ' (pin)' : '');
}

function criticalitySection(b: Bottlenecks, root: HTMLElement): HTMLElement {
  const active = b.criticality.filter((c) =>
    c.downstream_reach > 0 || c.immediate_unlock > 0 || c.remaining_depth > 1);
  const sorted = [...active].sort((a, x) => x[critSort] - a[critSort] || a.thing.localeCompare(x.thing));
  const th = (key: CritKey, label: string, title: string) => h('th', {
    class: 'sortable' + (critSort === key ? ' sorted' : ''), title,
    onclick: () => { critSort = key; renderCrit(); },
  }, label + (critSort === key ? ' ▾' : ''));

  const holder = h('section', { class: 'dash-sec' });
  const renderCrit = () => {
    holder.replaceChildren(
      h('h2', null, 'Critical things', helpButton('criticality')),
      h('p', { class: 'muted tiny' },
        'Three separate structural numbers — reach does not mean immediate unblocking, so they are never summed. Click a column to rank by it.'),
      sorted.length === 0
        ? h('p', { class: 'empty' }, 'No unfinished thing gates anything downstream.')
        : h('table', { class: 'table' },
          h('thead', null, h('tr', null,
            h('th', null, 'thing'),
            th('downstream_reach', 'downstream reach', 'transitive dependents — everything that can never finish without this'),
            th('immediate_unlock', 'immediate unlock', 'dependents that become dependency-ready if this finishes'),
            th('remaining_depth', 'remaining depth', 'longest chain of unfinished things through it, in steps'))),
          h('tbody', null, ...sorted.slice(0, 25).map((c: Criticality) => {
            const t = store.thing(c.thing);
            return h('tr', null,
              h('td', null, t ? thingLink(t) : c.thing),
              h('td', null, String(c.downstream_reach)),
              h('td', null, String(c.immediate_unlock)),
              h('td', null, String(c.remaining_depth)));
          }))));
    void root;
  };
  renderCrit();
  return holder;
}

function starvationSection(b: Bottlenecks): HTMLElement {
  return h('section', { class: 'dash-sec' },
    h('h2', null, 'Starvation', helpButton('starvation')),
    h('p', { class: 'muted tiny' },
      'Things resource-blocked for a long uninterrupted stint. Credit is total time waited since the thing last held resources — it survives the flip to ready and boosts its recommendation score, so long-starved work gets first claim on freed capacity.'),
    b.starvation.length === 0
      ? h('p', { class: 'empty' }, 'Nothing is starving.')
      : h('table', { class: 'table' },
        h('thead', null, h('tr', null,
          h('th', null, 'thing'), h('th', null, 'current stint'), h('th', null, 'cumulative credit'))),
        h('tbody', null, ...b.starvation.map((s) => {
          const t = store.thing(s.thing);
          return h('tr', null,
            h('td', null, t ? thingLink(t) : s.thing),
            h('td', null, s.current_stint_seconds > 0 ? duration(s.current_stint_seconds) : h('span', { class: 'muted' }, '— (not blocked right now)')),
            h('td', null, duration(s.credit_seconds)));
        }))));
}

function stat(value: string, label: string, title: string, cls = ''): HTMLElement {
  return h('div', { class: 'stat ' + cls, title },
    h('div', { class: 'stat-value' }, value),
    h('div', { class: 'stat-label' }, label),
    title ? h('div', { class: 'stat-sub' }, title) : null);
}
