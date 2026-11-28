import { initCarousel } from './carousel';
import { initAppearance } from './appearance';
import { initNavigation } from './navigation';
import { initSearch } from './search';
import { initReading } from './reading';
import { initComments } from './comments';
import { initRelativeTimes } from './shared';
import { initLearning } from './learning';
import { initHomeAccount } from './home-account';

initAppearance();
initNavigation();
initSearch();
initReading();
initComments();
initRelativeTimes();
initLearning();
initHomeAccount();

const motionMedia = window.matchMedia('(prefers-reduced-motion: reduce)');
initCarousel(document, window, { media: motionMedia, allowed: () => !motionMedia.matches && document.documentElement.dataset.reduceMotion !== 'true' });
