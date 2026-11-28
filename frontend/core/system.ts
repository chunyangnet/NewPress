import {createStatusPoller,statusError} from '../shared/system-status-poller';
import {validSnapshot} from '../shared/system-status-data';
import {PluginRequestError} from '../shared/plugin-data-common';
import {required} from './ledger-admin-dom';
export {createStatusPoller,validSnapshot};
(() => {
  if (typeof document === 'undefined') return;
  const found = document.querySelector<HTMLElement>('[data-system-status]');
  if (!found || typeof fetch !== 'function' || typeof AbortController !== 'function') return;
  const root=found,eventScope=new AbortController();let locked=false;
  const $=<T extends HTMLElement=HTMLElement>(selector:string):T=>required<T>(root,selector);
  const navigation = document.querySelector<HTMLElement>('.admin-nav');
  const activeLink = navigation?.querySelector('[aria-current="page"]');
  if (navigation && activeLink) {
    const navRect = navigation.getBoundingClientRect();
    const linkRect = activeLink.getBoundingClientRect();
    if (navRect.height > 0 && linkRect.bottom > navRect.bottom) navigation.scrollTop += linkRect.bottom - navRect.bottom + 12;
  }
  const refreshButton = $<HTMLAnchorElement>('[data-system-refresh]');
  const autoToggle = $<HTMLInputElement>('[data-system-auto]');
  const feedback = $('[data-system-feedback]');
  const badge = root.querySelector('.system-live-label');
  const metrics = new Map([...root.querySelectorAll<HTMLElement>('[data-system-metric]')].map(node => [node.dataset.systemMetric, node]));
  const checks = new Map([...root.querySelectorAll<HTMLElement>('[data-system-check]')].map(node => [node.dataset.systemCheck, node]));
  const rows = new Map([...root.querySelectorAll<HTMLElement>('[data-system-row]')].map(node => [node.dataset.systemRow, node]));
  let automatic = true;
  let lastCollectedAt = Date.parse(root.dataset.collectedAt || '') || 0;
  let interactionTimer:number|null = null;

  const setText = (scope:ParentNode, selector:string, value:string|number) => scope.querySelectorAll(selector).forEach(node => { node.textContent = String(value); });
  function message(text:string, tone = 'warning') {
    if (interactionTimer !== null) clearTimeout(interactionTimer);
    interactionTimer = null;
    feedback.textContent = text;
    feedback.dataset.tone = tone;
    feedback.hidden = !text;
    if (tone === 'success') interactionTimer = setTimeout(() => { feedback.hidden = true; }, 4500);
  }
  function render(report:unknown, manual:boolean) {
    if (!validSnapshot(report)) throw new Error('invalid snapshot');
    const collectedAt = Date.parse(report.collected_at);
    if (collectedAt < lastCollectedAt) throw new Error('stale snapshot');
    lastCollectedAt = collectedAt;
    root.dataset.collectedAt = report.collected_at;
    setText(root, '[data-system-updated]', report.collected_label);
    $<HTMLTimeElement>('[data-system-updated]').dateTime = report.collected_at;
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
    root.querySelectorAll<HTMLElement>('[data-system-health]').forEach(node => { node.dataset.systemHealth = report.status; });
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
      const response = await fetch(root.dataset.endpoint || '/api/v1/admin/system-status', { credentials: 'same-origin', cache: 'no-store', redirect:'error', headers: { Accept: 'application/json' }, signal });
      if (!response.ok) {
        const error = new PluginRequestError('status request failed');
        error.status = response.status;
        throw error;
      }
      return response.json();
    },
    onData: render,
    onState(state) {
      automatic = !locked && state.enabled;
      autoToggle.checked = automatic;
      window.ChunnelAdminUI?.syncSwitch(autoToggle);
      refreshButton.setAttribute('aria-disabled', String(locked || state.busy));
      refreshButton.setAttribute('aria-busy', String(state.busy));
      required(refreshButton,'span').textContent = state.busy ? '正在刷新' : '刷新状态';
      root.classList.toggle('is-stale', state.failures > 0);
      if (badge) badge.textContent = state.failures ? 'STALE' : automatic && !state.suspended ? 'LIVE' : 'PAUSED';
    },
    onError(error, timedOut) {
      if (statusError(error).status === 401) {
        deny();
        message('登录已过期，自动刷新已暂停。重新登录后可继续查看。');
        const login = document.createElement('a');
        login.href = '/admin/login/?next=%2Fadmin%2Fsystem%2F';
        login.textContent = '重新登录';
        feedback.append(' ', login);
      } else if (statusError(error).status === 403) {
        deny();
        message('当前账号已无权读取系统状态，自动刷新已暂停。');
      } else {
        message(`${timedOut ? '本次刷新超时' : '暂时无法更新状态'}，保留上次快照。${automatic ? '稍后自动重试，也可点击刷新状态。' : '请点击刷新状态重试。'}`);
      }
    },
  });
  $('[data-system-auto-control]').hidden = false;
  refreshButton.addEventListener('click', event => {
    if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button > 0) return;
    event.preventDefault();
    if(!locked)poller.refresh(true);
  });
  autoToggle.addEventListener('change', () => { if(!locked)poller.setEnabled(autoToggle.checked); });
  const deny=()=>{
    locked=true;poller.destroy();autoToggle.disabled=true;
    refreshButton.setAttribute('aria-disabled','true');
    root.querySelectorAll<HTMLElement>('[data-system-row],[data-metric-value],[data-metric-unit],[data-metric-detail],[data-metric-note],[data-check-detail],[data-check-status],[data-check-latency],[data-system-hostname],[data-system-platform],[data-system-version],[data-system-timezone],[data-system-uptime],[data-system-started],[data-system-status-label],[data-system-status-detail],[data-system-healthy],[data-system-total],[data-system-updated]').forEach(node=>node.textContent='');
    root.querySelectorAll('[data-metric-gauge]').forEach(node=>node.setAttribute('stroke-dasharray','0 100'));
    message('登录身份或权限已变化，系统快照已清除，请重新打开页面。');
  };
  document.addEventListener('visibilitychange',()=>{if(!locked)document.hidden?poller.suspend():poller.resume();},{signal:eventScope.signal});
  window.addEventListener('pagehide',()=>poller.suspend(),{signal:eventScope.signal});
  window.addEventListener('pageshow',event=>{if(event.persisted&&!document.hidden&&!locked)poller.resume();},{signal:eventScope.signal});
  document.addEventListener('chunnel:session',deny,{signal:eventScope.signal});
  let channel:BroadcastChannel|undefined;try{channel=new BroadcastChannel('chunnel.social');channel.addEventListener('message',event=>{if(event.data?.type==='session')deny();});}catch{}
  document.addEventListener('chunnel:admin-before-replace',event=>{if(event.detail?.regions?.some(region=>region===root||region.contains(root))){locked=true;poller.destroy();if(interactionTimer!==null)clearTimeout(interactionTimer);eventScope.abort();channel?.close();}},{signal:eventScope.signal});
  poller.start();
})();
