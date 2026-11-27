import { experienceContext } from './experience-context';
import { mountGuestPreferences } from './guest-preferences';
import { createDiscovery } from './discovery';
import { mountCommunityList } from './community-list';
import { mountComments } from './comments';
import { mountCommunityComposer } from './community-composer';

export function mountExperience(): void {
  const deps = experienceContext();
  if (!deps) return;
  const { preferences, refreshPreferences } = mountGuestPreferences(deps);
  createDiscovery(deps.dom.optional('[data-discovery]'), deps.dom, refreshPreferences).mount();
  mountCommunityList(deps, preferences); mountComments(deps); mountCommunityComposer(deps);
}
mountExperience();
