// Public analytics bootstrap. Configuration is escaped JSON in a data attribute;
// only validated IDs and HTTPS URLs reach this file. No inline script or eval.
(() => {
  'use strict';
  const entry = document.currentScript;
  if (!entry || window.__newpressAnalyticsLoaded) return;
  const requiresConsent = entry.dataset.consentRequired === 'true';
  let config;
  try { config = JSON.parse(entry.dataset.siteIntegrations || '{}'); } catch { return; }
  if (!config.enabled) return;
  // The theme consent controller runs first. Fail closed if it is unavailable;
  // event payloads and stale storage must never bypass its validated choice.
  const start = () => {
    if (window.__newpressAnalyticsLoaded || (requiresConsent && window.NewPressCookieConsent?.allowsAnalytics() !== true)) return;
    window.__newpressAnalyticsLoaded = true;
    loadIntegrations();
  };
  const loadIntegrations = () => {
    const load = (src, attributes = {}) => {
      const script = document.createElement('script');
      script.async = true;
      script.src = src;
      for (const [key, value] of Object.entries(attributes)) script.setAttribute(key, value);
      document.head.appendChild(script);
    };
    if (config.google_id) {
      window.dataLayer = window.dataLayer || [];
      window.gtag = window.gtag || function () { window.dataLayer.push(arguments); };
      window.gtag('js', new Date());
      window.gtag('config', config.google_id);
      load('https://www.googletagmanager.com/gtag/js?id=' + encodeURIComponent(config.google_id));
    }
    if (config.google_tag_id) {
      window.dataLayer = window.dataLayer || [];
      window.dataLayer.push({ 'gtm.start': Date.now(), event: 'gtm.js' });
      load('https://www.googletagmanager.com/gtm.js?id=' + encodeURIComponent(config.google_tag_id));
    }
    if (config.baidu_id) {
      window._hmt = window._hmt || [];
      load('https://hm.baidu.com/hm.js?' + encodeURIComponent(config.baidu_id));
    }
    if (config.clarity_id) {
      window.clarity = window.clarity || function () { (window.clarity.q = window.clarity.q || []).push(arguments); };
      load('https://www.clarity.ms/tag/' + encodeURIComponent(config.clarity_id));
    }
    if (config.cloudflare_token) {
      load('https://static.cloudflareinsights.com/beacon.min.js', { 'data-cf-beacon': JSON.stringify({ token: config.cloudflare_token }) });
    }
    if (config.matomo_url && config.matomo_site_id) {
      window._paq = window._paq || [];
      window._paq.push(['setTrackerUrl', config.matomo_url + '/matomo.php']);
      window._paq.push(['setSiteId', config.matomo_site_id]);
      window._paq.push(['trackPageView']);
      window._paq.push(['enableLinkTracking']);
      load(config.matomo_url + '/matomo.js');
    }
    if (config.umami_url && config.umami_website_id) {
      load(config.umami_url, { 'data-website-id': config.umami_website_id });
    }
    if (config.plausible_url) {
      window.plausible = window.plausible || function () { (window.plausible.q = window.plausible.q || []).push(arguments); };
      window.plausible.init = window.plausible.init || function (options) { window.plausible.o = options || {}; };
      window.plausible.init();
      load(config.plausible_url);
    }
  };
  if (requiresConsent) {
    start();
    document.addEventListener('newpress:cookie-consent', start);
  } else {
    start();
  }
})();
