// views/history.ts — the event log as a narrative (§3.6): per-entity
// timelines at #/history/:entityId, workspace-wide recent activity at
// #/history. Events grouped by batch; ids resolved to current names.

import { api, EventEnvelope } from '../api';
import { h } from '../dom';
import { ts } from '../fmt';
import { store } from '../store';
import { helpButton } from '../ui/help';

export function renderHistory(root: HTMLElement, entityId?: string): void {
  const title = entityId
    ? h('h2', null, 'History of ', h('b', null, store.name(entityId)), ' ', h('span', { class: 'muted tiny' }, entityId), helpButton('history'))
    : h('h2', null, 'Recent activity', helpButton('history'));
  root.replaceChildren(h('div', { class: 'toolbar' }, title,
    h('span', { class: 'spacer' }),
    entityId ? h('a', { class: 'btn btn-ghost', href: '#/history' }, 'workspace-wide') : null),
  h('div', { class: 'hist', id: 'hist-body' }, h('div', { class: 'empty' }, 'Loading…')));

  void (async () => {
    const body = root.querySelector('#hist-body')!;
    let events: EventEnvelope[];
    try {
      if (entityId) {
        events = (await api.history({ entity: entityId })).events;
      } else {
        const last = store.workspace?.last_seq ?? 0;
        const since = Math.max(1, last - 400);
        events = (await api.history({ since_seq: since })).events;
      }
    } catch (e) {
      body.replaceChildren(h('div', { class: 'empty' }, String((e as Error).message)));
      return;
    }
    if (events.length === 0) {
      body.replaceChildren(h('div', { class: 'empty' }, 'No events.'));
      return;
    }
    // newest first, grouped by batch
    const batches: EventEnvelope[][] = [];
    let cur: EventEnvelope[] = [];
    for (const ev of events) {
      if (cur.length > 0 && cur[0]!.batch !== ev.batch) { batches.push(cur); cur = []; }
      cur.push(ev);
    }
    if (cur.length > 0) batches.push(cur);
    batches.reverse();

    body.replaceChildren(...batches.map((b) => h('section', { class: 'hist-batch' },
      h('header', { class: 'hist-head' },
        h('span', { class: 'muted' }, ts(b[0]!.ts)),
        h('b', null, ' ' + b[0]!.actor),
        h('span', { class: 'muted tiny' }, ` · batch ${b[0]!.batch.slice(0, 10)}… · seq ${b[0]!.seq}${b.length > 1 ? `–${b[b.length - 1]!.seq}` : ''}`)),
      h('ul', null, ...b.map((ev) => h('li', { class: 'hist-line' }, narrate(ev)))))));
  })();
}

function nameOf(id: unknown): string {
  return typeof id === 'string' && id ? store.name(id) : String(id ?? '');
}

/** narrate renders one event as a sentence; unknown types fall back to the
 * raw type + payload so nothing is ever hidden. */
function narrate(ev: EventEnvelope): (string | HTMLElement)[] {
  const d = ev.data ?? {};
  const entity = () => h('a', { href: `#/history/${ev.entity}` }, nameOf(ev.entity));
  const strong = (s: unknown) => h('b', null, String(s ?? ''));
  switch (ev.type) {
    case 'log.initialized': return ['workspace initialized'];
    case 'writer.started': return ['a new writer lineage started (restore/clone)'];
    case 'project.created': return ['created project ', strong(d['name'])];
    case 'project.superseded': return ['updated project ', entity()];
    case 'project.retracted': return ['retracted project ', entity()];
    case 'thing.created': return ['created ', strong(d['name']), ' in ', nameOf(d['project']),
      d['parent'] ? ` (inside ${nameOf(d['parent'])})` : ''];
    case 'thing.superseded': return ['edited ', entity(), ' (now “', String(d['name'] ?? ''), '”)'];
    case 'thing.retracted': return ['retracted thing ', entity()];
    case 'thing.state_changed': return ['moved ', entity(), ' → ', strong(nameOf(d['state']))];
    case 'dependency.asserted': return ['asserted: ', strong(nameOf(d['from'])), ' depends on ', strong(nameOf(d['to'])),
      d['on_abandoned'] === 'block' ? ' (blocks on abandon)' : ''];
    case 'dependency.retracted': return ['retracted a dependency edge'];
    case 'requirement.asserted': return ['required for ', strong(nameOf(d['thing'])), ': ',
      reqPhrase(d)];
    case 'requirement.superseded': return ['changed a requirement to: ', reqPhrase(d)];
    case 'requirement.retracted': return ['retracted a requirement'];
    case 'resource.created': return ['created resource ', strong(d['name']),
      ` (${d['named'] ? 'named' : `pool ×${d['capacity']}`})`];
    case 'resource.superseded': return ['updated resource ', entity()];
    case 'resource.retracted': return ['retracted resource ', entity()];
    case 'resource.availability_changed':
      return [entity(), d['available'] ? ' became available' : ' became unavailable',
        d['note'] ? ` — “${String(d['note'])}”` : ''];
    case 'capability.granted': return ['granted ', strong(nameOf(d['capability'])), ' to ', entity()];
    case 'capability.revoked': return ['revoked ', strong(nameOf(d['capability'])), ' from ', entity()];
    case 'capability.defined': return ['defined capability ', strong(d['name'])];
    case 'capability.superseded': return ['updated capability ', entity()];
    case 'capability.retracted': return ['retracted capability ', entity()];
    case 'state.defined': return ['defined state ', strong(d['name']), ` → ${d['semantic']}`];
    case 'state.superseded': return ['updated state ', entity()];
    case 'state.retracted': return ['retracted state ', entity()];
    case 'type.defined': return ['defined type ', strong(d['name'])];
    case 'type.superseded': return ['updated type ', entity()];
    case 'type.retracted': return ['retracted type ', entity()];
    case 'allocation.opened': return ['allocated ', strong(nameOf(d['resource'])),
      ` ×${d['quantity']}`, ' to ', strong(nameOf(d['thing']))];
    case 'allocation.closed': return ['closed an allocation'];
    default:
      return [ev.type + ' ', h('code', { class: 'tiny' }, JSON.stringify(ev.data))];
  }
}

function reqPhrase(d: Record<string, unknown>): string {
  if (d['resource']) return `pin ${nameOf(d['resource'])}`;
  const caps = Array.isArray(d['capabilities']) ? (d['capabilities'] as string[]).map(nameOf).join('+') : '';
  return `${d['quantity'] ?? 1}× ${caps}`;
}
