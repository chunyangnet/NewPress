import { createSiteTime } from './site-time';

window.ChunnelTime = createSiteTime(() => document.documentElement?.getAttribute('data-site-timezone'));
