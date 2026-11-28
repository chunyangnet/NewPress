/* Native links and forms remain usable when enhancements are unavailable. */
import { initNavigation } from './navigation';
import { initNavigationState } from './navigation-state';
import { initSearch } from './search';
import { initCarousel } from './carousel';
import { initReading, initCommentReplies } from './reading';

document.documentElement.classList.add('js');
const navigation = initNavigation(document, window);
initNavigationState(document, window);
initSearch(document, window, navigation);
initCarousel(document);
initReading();
initCommentReplies();

// Guest account pages do not load member-only panels. Refresh after sign-in so
// the server can render the authenticated controls and their dependencies.
const accountRoom = document.querySelector<HTMLElement>('[data-account-room]');
if (accountRoom && !Number(accountRoom.dataset.accountUser)) {
  document.addEventListener('chunnel:session', event => {
    if (event.detail?.authenticated && event.detail?.id) window.location.reload();
  });
}
