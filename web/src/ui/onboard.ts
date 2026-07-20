// ui/onboard.ts — the empty-workspace screen. Shared by the two routes that
// can be reached before any project exists (the default #/ready and the
// project workbench), so there is one wording of "start here", not two.

import { h } from '../dom';
import { navigate } from '../router';
import { openProjectEditor } from './projectEditor';

export function renderOnboard(root: HTMLElement): void {
  root.replaceChildren(h('div', { class: 'centered onboard' },
    h('h2', null, 'Welcome to churn'),
    h('p', null, 'This workspace is empty. Work lives in ', h('b', null, 'projects'),
      ' — dependency graphs of things — worked with the shared ', h('b', null, 'resources'), '.'),
    h('p', null, h('button', {
      class: 'btn btn-primary mut',
      onclick: () => openProjectEditor(undefined, (p) => navigate('project', p.id, 'graph')),
    }, 'Create your first project')),
    h('p', { class: 'muted' },
      'Then add things to it (single or ', h('b', null, 'Bulk add'), '), declare resources on the ',
      h('a', { href: '#/resources' }, 'resource board'),
      ', and tune the vocabulary of states, types and capabilities under ',
      h('a', { href: '#/settings/vocab' }, 'Settings → Vocabulary'),
      '. Sensible default states are already in place.')));
}
