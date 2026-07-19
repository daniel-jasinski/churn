// ui/bits.ts — shared presentational pieces built from store data.

import { Badges, MatchReq, Recommendation, Requirement, Thing } from '../api';
import { badge, chip, h } from '../dom';
import { num } from '../fmt';
import { store } from '../store';

/** typeChip renders a thing type as a colored chip (vocab color). */
export function typeChip(typeId: string): HTMLElement {
  const t = store.type(typeId);
  return chip(t?.name ?? typeId, t?.color, 'chip-type');
}

/** stateChip renders a state name in its vocab color. */
export function stateChip(stateId: string): HTMLElement {
  const s = store.state(stateId);
  return chip(s?.name ?? stateId, s?.color, 'chip-state');
}

/** badgeRow renders the §2.2/§2.5 warning badges of a thing. */
export function badgeRow(t: { badges: Badges; has_abandoned?: boolean }): HTMLElement[] {
  const out: HTMLElement[] = [];
  const b = t.badges;
  if (b.abandoned_dependency) out.push(badge('⚠', 'A dependency was abandoned (edge policy: unblock with warning)'));
  if (b.finished_unsatisfied_deps) out.push(badge('⁉', 'Finished, but has unsatisfied dependencies (consistency warning)'));
  if (b.over_allocated) out.push(badge('▲', 'Over-allocated / pinned resource down — consider pausing', 'badge-alert'));
  if (b.allocations_out_of_step) out.push(badge('↻', 'Allocations out of step with requirements — re-propose to reconcile', 'badge-alert'));
  if (t.has_abandoned) out.push(badge('✕', 'Subtree contains abandoned work', 'badge-dim'));
  return out;
}

/** reqText renders one requirement compactly: "2× editing+approval" or a pin. */
export function reqText(quantity: number, capabilities: string[] | undefined, pin: string | undefined): string {
  if (pin) return `pin: ${store.name(pin)}`;
  const caps = (capabilities ?? []).map((c) => store.name(c)).join('+');
  return `${quantity}× ${caps || '?'}`;
}

/** reqChips renders requirement chips for match requirements. */
export function reqChips(reqs: MatchReq[]): HTMLElement[] {
  return reqs.map((r) => chip(reqText(r.quantity, r.capabilities, r.pin), undefined, r.pin ? 'chip-pin' : 'chip-req'));
}

/** reqChipsOf renders requirement chips for stored requirement entities. */
export function reqChipsOf(reqs: Requirement[]): HTMLElement[] {
  return reqs.map((r) => chip(reqText(r.quantity, r.capabilities, r.resource), undefined, r.resource ? 'chip-pin' : 'chip-req'));
}

/** scoreBlock renders the recommendation score with an expandable per-term
 * breakdown, including the waiting-age disclosure (§3.4). */
export function scoreBlock(rec: Recommendation): HTMLElement {
  const details = h('details', { class: 'score' },
    h('summary', null,
      h('span', { class: 'score-num' }, num(rec.score)),
      h('span', { class: 'muted' }, ' score')),
    h('table', { class: 'score-terms' },
      h('tbody', null,
        ...rec.terms.map((t) => h('tr', null,
          h('td', { class: 'muted' }, t.name.replaceAll('_', ' ')),
          h('td', null, `${num(t.value)} × ${num(t.weight)}`),
          h('td', { class: t.contribution < 0 ? 'neg' : 'pos' },
            (t.contribution >= 0 ? '+' : '') + num(t.contribution)),
          h('td', { class: 'muted detail' }, t.detail))))));
  return details;
}

/** starveNote renders the waiting-age disclosure when credit exists. */
export function starveNote(rec: Recommendation): HTMLElement | null {
  const wa = rec.terms.find((t) => t.name === 'waiting_age');
  if (!wa || wa.value <= 0) return null;
  return h('div', { class: 'starve-note' }, '⏳ ' + wa.detail);
}

/** projectName resolves a project id to its display name. */
export function projectName(id: string): string {
  return store.project(id)?.name ?? id;
}

/** thingLink renders a link to the entity history of a thing. */
export function thingLink(t: Thing): HTMLElement {
  return h('a', { href: `#/history/${t.id}`, class: 'entity-link', title: `History of ${t.name}` }, t.name);
}
