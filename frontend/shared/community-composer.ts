import type { ExperienceDependencies, CommunityState } from './experience-types';
import type { ClientSession } from './newpress-client';
import { parseCommunityDraft } from './experience-data';
import { createCommunitySubmission } from './community-submit';

export function mountCommunityComposer(deps: ExperienceDependencies): void {
  const { getSession, storage, run, createIcon, dom: { $, optional, node } } = deps;
  const composer = optional<HTMLFormElement>('[data-community-composer]');
  if (!composer) return;
  const input = $<HTMLTextAreaElement>('[data-community-content]');
  const submit = $<HTMLButtonElement>('[data-community-submit]');
  const editor = $<HTMLButtonElement>('[data-composer-editor]');
  const errorBox = node('p', 'interaction-error'); errorBox.hidden = true; errorBox.setAttribute('role', 'alert'); composer.append(errorBox);
  const state: CommunityState = { owner: null, generation: 0, busy: false, pending: null };
  const key = (id = state.owner) => `chunnel.community.${id || 0}`;
  const persist = () => {
    if (state.owner === null) return;
    if (input.value) storage.set(key(), { content: input.value, pending: state.pending, at: Date.now() });
    else storage.remove(key());
  };
  const update = (save = true) => {
    const length = [...input.value].length; $('[data-community-count]').textContent = `${length} / 500`;
    submit.disabled = state.busy || !input.value.trim() || length > 500;
    editor.disabled = state.busy || length > 500;
    if (save) persist();
  };
  const loadDraft = () => {
    const draft = parseCommunityDraft(storage.get(key(), null));
    input.value = draft.content; state.pending = draft.pending;
  };
  const reveal = () => { composer.hidden = false; $('.composer-teaser').hidden = true; };
  const conceal = () => { composer.hidden = true; $('.composer-teaser').hidden = false; };
  const syncIdentity = (session: ClientSession) => {
    const nextOwner = session.authenticated && session.id ? session.id : 0;
    if (state.owner === nextOwner) return;
    persist(); state.generation++; state.owner = nextOwner; loadDraft(); state.busy = false; input.readOnly = false; errorBox.hidden = true;
    submit.replaceChildren(createIcon('send'), document.createTextNode('发布讨论')); update(false); conceal();
  };
  const openComposer = async () => {
    const generation = state.generation;
    const session = await getSession();
    // Session notifications may already have applied this same identity.
    const nextOwner = session.authenticated && session.id ? session.id : 0;
    if (state.generation !== generation && state.owner !== nextOwner) return;
    if (state.owner !== nextOwner) syncIdentity(session); else if (!input.value) loadDraft();
    reveal(); update();
    composer.scrollIntoView({ block: 'center', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' });
    input.focus({ preventScroll: true });
  };
  const submitDiscussion = createCommunitySubmission({ ...deps, state, input, submit, errorBox, reveal, update, key });
  input.addEventListener('input', () => { errorBox.hidden = true; update(); });
  document.addEventListener('click', event => {
    if (!(event.target instanceof Element)) return;
    if (event.target.closest('[data-composer-open]')) run(openComposer);
    if (event.target.closest('[data-composer-close]')) { update(); conceal(); $('[data-composer-open]').focus({ preventScroll: true }); }
    if (event.target.closest('[data-composer-editor]')) submitDiscussion('draft');
  });
  composer.addEventListener('submit', event => { event.preventDefault(); if (composer.reportValidity()) submitDiscussion('publish'); });
  document.addEventListener('chunnel:session', event => syncIdentity(event.detail));
  window.addEventListener('pagehide', persist);
  update(false);
}
