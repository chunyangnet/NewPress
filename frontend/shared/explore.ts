import { experienceContext } from './experience-context';
import { mountGuestPreferences } from './guest-preferences';
import { createDiscovery } from './discovery';

// Discovery pages do not render article comments or the community composer.
// Keep bookmarks, sharing and quick actions from their existing implementation.
const deps = experienceContext();
if (deps) {
  const { refreshPreferences } = mountGuestPreferences(deps);
  createDiscovery(deps.dom.optional('[data-discovery]'), deps.dom, refreshPreferences).mount();
}
