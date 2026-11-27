(() => {
  'use strict';

  // One request at a time. Hidden pages and paused controls invalidate in-flight
  // responses, including transports which finish after receiving an abort.
  function createStatusPoller(options) {
    const setTimer = options.setTimer || setTimeout;
    const clearTimer = options.clearTimer || clearTimeout;
    const visible = options.visible || (() => true);
    let timer = null;
    let flight = null;
    let enabled = true;
    let suspended = false;
    let destroyed = false;
    let failures = 0;
    let authStatus = 0;

    function notify() {
      options.onState?.({ enabled, busy: !!flight, suspended, failures, authStatus });
    }
    function clear() {
      if (timer !== null) clearTimer(timer);
      timer = null;
    }
    function cancel() {
      const previous = flight;
      flight = null;
      if (previous) {
        clearTimer(previous.timeout);
        previous.controller.abort();
      }
    }
    function schedule(delay = Math.min(60000, 15000 * (1 + failures))) {
      clear();
      if (!destroyed && enabled && !suspended && visible() && !flight) {
        timer = setTimer(() => { timer = null; refresh(); }, delay);
      }
    }
    async function refresh(manual = false) {
      if (destroyed || suspended || !visible() || flight) return;
      clear();
      const current = { controller: new AbortController(), timeout: null, timedOut: false };
      flight = current;
      notify();
      current.timeout = setTimer(() => {
        current.timedOut = true;
        current.controller.abort();
      }, 10000);
      try {
        const result = await options.load(current.controller.signal);
        if (flight !== current || destroyed || suspended || !visible()) return;
        if (current.timedOut) throw new Error('timeout');
        options.onData(result, manual);
        failures = 0;
        authStatus = 0;
      } catch (error) {
        if (flight !== current || destroyed || suspended || !visible()) return;
        failures++;
        if (error?.status === 401 || error?.status === 403) {
          authStatus = error.status;
          enabled = false;
        }
        options.onError?.(error, current.timedOut);
      } finally {
        clearTimer(current.timeout);
        if (flight === current) {
          flight = null;
          notify();
          schedule();
        }
      }
    }
    return {
      start() { notify(); schedule(2200); },
      refresh,
      setEnabled(next) {
        enabled = !!next;
        clear();
        if (!enabled) cancel();
        notify();
        if (enabled) refresh();
      },
      suspend() { suspended = true; clear(); cancel(); notify(); },
      resume() { suspended = false; notify(); if (enabled) refresh(); },
      destroy() { destroyed = true; clear(); cancel(); notify(); },
    };
  }

  function validSnapshot(value) {
    const text = item => typeof item === 'string';
    const fields = (item, keys) => item && keys.every(key => text(item[key]));
    return !!(value && Number.isFinite(Date.parse(value.collected_at)) &&
      fields(value, ['collected_label', 'status_label', 'status_detail', 'uptime', 'started_at', 'version', 'hostname', 'platform', 'timezone']) &&
      ['pass', 'error'].includes(value.status) && Number.isInteger(value.healthy) && Number.isInteger(value.total) && value.healthy >= 0 && value.healthy <= value.total &&
      Array.isArray(value.metrics) && value.metrics.length === 4 &&
      value.metrics.every(item => fields(item, ['id', 'value', 'unit', 'detail', 'note', 'tone']) &&
        ['normal', 'muted', 'warning', 'error'].includes(item.tone) && Number.isFinite(item.percent) && item.percent >= 0 && item.percent <= 100) &&
      Array.isArray(value.checks) && value.checks.every(item => fields(item, ['id', 'detail', 'status_label', 'latency']) && ['pass', 'error', 'disabled'].includes(item.status)) &&
      Array.isArray(value.groups) && value.groups.every(group => group && Array.isArray(group.rows) && group.rows.every(row => fields(row, ['id', 'value']))));
  }

  if (typeof module !== 'undefined' && module.exports) module.exports = { createStatusPoller, validSnapshot };
  if (typeof document === 'undefined') return;
  const root = document.querySelector('[data-system-status]');
  if (!root || typeof fetch !== 'function' || typeof AbortController !== 'function') return;
  const navigation = document.querySelector('.admin-nav');
  const activeLink = navigation?.querySelector('[aria-current="page"]');
  if (navigation && activeLink) {
    const navRect = navigation.getBoundingClientRect();
    const linkRect = activeLink.getBoundingClientRect();
    if (navRect.height > 0 && linkRect.bottom > navRect.bottom) navigation.scrollTop += linkRect.bottom - navRect.bottom + 12;
  }
  const refreshButton = root.querySelector('[data-system-refresh]');
  const autoToggle = root.querySelector('[data-system-auto]');
  const feedback = root.querySelector('[data-system-feedback]');
  const badge = root.querySelector('.system-live-label');
  const metrics = new Map([...root.querySelectorAll('[data-system-metric]')].map(node => [node.dataset.systemMetric, node]));
  const checks = new Map([...root.querySelectorAll('[data-system-check]')].map(node => [node.dataset.systemCheck, node]));
  const rows = new Map([...root.querySelectorAll('[data-system-row]')].map(node => [node.dataset.systemRow, node]));
  let automatic = true;
  let lastCollectedAt = Date.parse(root.dataset.collectedAt) || 0;
  let interactionTimer = null;

  const setText = (scope, selector, value) => scope.querySelectorAll(selector).forEach(node => { node.textContent = value; });
  function message(text, tone = 'warning') {
    if (interactionTimer !== null) clearTimeout(interactionTimer);
    interactionTimer = null;
    feedback.textContent = text;
    feedback.dataset.tone = tone;
    feedback.hidden = !text;
    if (tone === 'success') interactionTimer = setTimeout(() => { feedback.hidden = true; }, 4500);
  }
  function render(report, manual) {
    if (!validSnapshot(report)) throw new Error('invalid snapshot');
    const collectedAt = Date.parse(report.collected_at);
    if (collectedAt < lastCollectedAt) throw new Error('stale snapshot');
    lastCollectedAt = collectedAt;
    root.dataset.collectedAt = report.collected_at;
    setText(root, '[data-system-updated]', report.collected_label);
    root.querySelector('[data-system-updated]').dateTime = report.collected_at;
    setText(root, '[data-system-status-label]', report.status_label);
    setText(root, '[data-system-status-detail]', report.status_detail);
    setText(root, '[data-system-healthy]', report.healthy);
    setText(root, '[data-system-total]', report.total);
    setText(root, '[data-system-uptime]', report.uptime);
    setText(root, '[data-system-started]', report.started_at);
    setText(root, '[data-system-version]', report.version);
    setText(root, '[data-system-hostname]', report.hostname);
    setText(root, '[data-system-platform]', report.platform);
    setText(root, '[data-system-timezone]', report.timezone);
    root.querySelectorAll('[data-system-health]').forEach(node => { node.dataset.systemHealth = report.status; });
    report.metrics.forEach(metric => {
      const node = metrics.get(metric.id);
      if (!node) return;
      node.dataset.tone = metric.tone;
      setText(node, '[data-metric-value]', metric.value);
      setText(node, '[data-metric-unit]', metric.unit);
      setText(node, '[data-metric-detail]', metric.detail);
      setText(node, '[data-metric-note]', metric.note);
      node.querySelector('[data-metric-gauge]')?.setAttribute('stroke-dasharray', `${metric.percent} 100`);
    });
    report.checks.forEach(check => {
      const node = checks.get(check.id);
      if (!node) return;
      node.dataset.status = check.status;
      setText(node, '[data-check-detail]', check.detail);
      setText(node, '[data-check-status]', check.status_label);
      setText(node, '[data-check-latency]', check.latency);
    });
    report.groups.forEach(group => group.rows.forEach(row => {
      const node = rows.get(row.id);
      if (node) node.textContent = row.value;
    }));
    if (manual) message(`状态已更新 · ${report.collected_label}`, 'success');
    else if (!feedback.hidden && feedback.dataset.tone !== 'success') message('');
  }
  const poller = createStatusPoller({
    visible: () => !document.hidden,
    async load(signal) {
      const response = await fetch(root.dataset.endpoint, { credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' }, signal });
      if (!response.ok) {
        const error = new Error('status request failed');
        error.status = response.status;
        throw error;
      }
      return response.json();
    },
    onData: render,
    onState(state) {
      automatic = state.enabled;
      autoToggle.checked = automatic;
      window.ChunnelAdminUI?.syncSwitch(autoToggle);
      refreshButton.setAttribute('aria-disabled', String(state.busy));
      refreshButton.setAttribute('aria-busy', String(state.busy));
      refreshButton.querySelector('span').textContent = state.busy ? '正在刷新' : '刷新状态';
      root.classList.toggle('is-stale', state.failures > 0);
      if (badge) badge.textContent = state.failures ? 'STALE' : automatic && !state.suspended ? 'LIVE' : 'PAUSED';
    },
    onError(error, timedOut) {
      if (error?.status === 401) {
        message('登录已过期，自动刷新已暂停。重新登录后可继续查看。');
        const login = document.createElement('a');
        login.href = '/admin/login/?next=%2Fadmin%2Fsystem%2F';
        login.textContent = '重新登录';
        feedback.append(' ', login);
      } else if (error?.status === 403) {
        message('当前账号已无权读取系统状态，自动刷新已暂停。');
      } else {
        message(`${timedOut ? '本次刷新超时' : '暂时无法更新状态'}，保留上次快照。${automatic ? '稍后自动重试，也可点击刷新状态。' : '请点击刷新状态重试。'}`);
      }
    },
  });
  root.querySelector('[data-system-auto-control]').hidden = false;
  refreshButton.addEventListener('click', event => {
    if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button > 0) return;
    event.preventDefault();
    poller.refresh(true);
  });
  autoToggle.addEventListener('change', () => { poller.setEnabled(autoToggle.checked); });
  document.addEventListener('visibilitychange', () => { document.hidden ? poller.suspend() : poller.resume(); });
  window.addEventListener('pagehide', () => { poller.suspend(); });
  window.addEventListener('pageshow', event => { if (event.persisted && !document.hidden) poller.resume(); });
  poller.start();
})();
