import { createHomeLoader } from './home-loader';
import { initHomeFeatures } from './home-features-loader';
import { initHomeSearch } from './home-search-loader';
import { initHomeSession } from './home-session';
import { initHomeFeedLoader } from './home-feed-loader';
import { $ } from './home-dom';

const script = document.currentScript || $<HTMLScriptElement>('[data-home-entry]');
if (script instanceof HTMLScriptElement) {
  const entry = new URL(script.src), load = createHomeLoader(entry);
  const ready = initHomeFeatures(entry, load);
  initHomeSearch(load);
  initHomeSession(load, ready);
  initHomeFeedLoader(load);
}
