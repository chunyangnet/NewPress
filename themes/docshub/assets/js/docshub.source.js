import { initCarousel } from './modules/carousel.mjs';
import { initAppearance } from './modules/appearance.mjs';
import { initNavigation } from './modules/navigation.mjs';
import { initSearch } from './modules/search.mjs';
import { initReading } from './modules/reading.mjs';
import { initComments } from './modules/comments.mjs';
import { initRelativeTimes } from './modules/shared.mjs';
import { initLearning } from './modules/learning.mjs';
import { initHomeAccount } from './modules/home-account.mjs';

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
