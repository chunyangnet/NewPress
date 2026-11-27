import type {} from './account-types';
import { required, input } from './account-dom';
import { confirmed, errorMessage } from './account-data';
import { parsePreferences, preferenceFields as fields } from './notification-data';
import type { NotificationPreferences, Preferences } from './notification-data';

export function mountNotificationSettings(): void {
  const root = document.querySelector<HTMLElement>('[data-notification-settings]');
  if (!root || !window.ChunnelInteractions) return;
  const owner = Number(root.closest<HTMLElement>('[data-account-room]')?.dataset.accountUser);
  if (!Number.isSafeInteger(owner) || owner <= 0) return;
  const { api } = window.ChunnelInteractions;
  const $ = <T extends Element = HTMLElement>(selector: string): T => required<T>(root, selector);
  const form = $<HTMLFormElement>('form'), result = $('[data-notification-settings-result]');
  const submit = required<HTMLButtonElement>(form, '[type=submit]'), refresh = $<HTMLButtonElement>('[data-notification-settings-refresh]');
  const retry = $<HTMLButtonElement>('[data-notification-mail-retry]'), discard = $<HTMLButtonElement>('[data-notification-settings-discard]'), unsaved = $('[data-notification-unsaved]');
  let version: number | null = null, busy = false, available = false, savedEmail = false, savedWeekly = false;
  let baseline: Preferences | null = null, changed = false, operation: symbol | null = null;
  const dirty = (): boolean => baseline !== null && fields.some(name => input(form, name).checked !== baseline?.[name]);
  function showChanges(): void { discard.hidden = unsaved.hidden = !dirty(); discard.disabled = busy || changed; }
  function lock(value: boolean): void {
    busy = value;
    form.querySelectorAll<HTMLFieldSetElement>('fieldset').forEach(el => { el.disabled = value || changed || version === null; });
    submit.disabled = value || changed || version === null; refresh.disabled = retry.disabled = value || changed;
    input(form, 'email').disabled = !available && !savedEmail && !input(form, 'email').checked;
    input(form, 'weekly').disabled = !available && !savedWeekly && !input(form, 'weekly').checked;
    showChanges();
  }
  function begin(message: string): symbol | null {
    if (busy || changed) return null;
    const token = Symbol(); operation = token; lock(true); result.textContent = message; return token;
  }
  const active = (token: symbol): boolean => operation === token && !changed;
  function finish(token: symbol): void { if (active(token)) { operation = null; lock(false); } }
  function paint(data: NotificationPreferences, preserve = true): boolean {
    const keep = preserve && dirty(), changedElsewhere = keep && data.preferences.version !== version;
    available = data.email_available; savedEmail = data.preferences.email; savedWeekly = data.preferences.weekly;
    if (!keep) {
      version = data.preferences.version; baseline = data.preferences;
      for (const name of fields) input(form, name).checked = baseline[name];
    }
    $('[data-notification-email-hint]').textContent = available ? '仅发送到你的已验证邮箱。邮件内有独立退订入口。' : '站点需开启邮件服务，且账号邮箱已验证，才能开始订阅。已开启的订阅仍可关闭。';
    $('[data-notification-weekly-hint]').textContent = `每周一上午九点（${data.timezone}），回顾上一周最新的十篇公开文章。没有新文章时不发信。`;
    const counts = data.mail_status;
    $('[data-notification-delivery-status]').textContent = counts.pending || counts.failed || counts.uncertain ? `等待投递 ${counts.pending} · 发送失败 ${counts.failed} · 送达待确认 ${counts.uncertain}` : '投递队列已处理，没有待重试的邮件。';
    retry.hidden = !counts.failed; $('[data-notification-uncertain]').hidden = !counts.uncertain; return changedElsewhere;
  }
  async function load({ discardChanges = false }: { discardChanges?: boolean } = {}): Promise<void> {
    const token = begin('正在读取通知偏好…'); if (!token) return;
    try {
      const response = await api('/api/v1/social/notification-preferences'); if (!active(token)) return;
      const changedElsewhere = paint(parsePreferences(response), !discardChanges);
      result.textContent = changedElsewhere ? '其他窗口已更新通知偏好，当前修改已保留。可放弃修改并重新载入后核对。' : dirty() ? '状态已刷新，未保存的修改已保留。' : '';
    } catch (error) { if (active(token)) result.textContent = errorMessage(error); }
    finally { finish(token); }
  }
  refresh.addEventListener('click', () => { void load(); });
  discard.addEventListener('click', () => { void load({ discardChanges: true }); });
  form.addEventListener('change', showChanges);
  form.addEventListener('submit', async event => {
    event.preventDefault(); if (busy || changed || version === null) return;
    const body = { version, ...Object.fromEntries(fields.map(name => [name, input(form, name).checked])) };
    const token = begin('正在保存…'); if (!token) return;
    try {
      const response = await api('/api/v1/social/notification-preferences', { method: 'POST', body }); if (!active(token)) return;
      paint(parsePreferences(response), false); result.textContent = '通知偏好已保存。';
    } catch (error) { if (active(token)) result.textContent = errorMessage(error); }
    finally { finish(token); }
  });
  retry.addEventListener('click', async () => {
    const token = begin('正在重新安排投递…'); if (!token) return;
    let requeued = false;
    try {
      const response = await api('/api/v1/social/notification-mail/retry', { method: 'POST', body: {} }); if (!active(token)) return;
      confirmed(response); requeued = true; retry.hidden = true;
      const preferences = await api('/api/v1/social/notification-preferences'); if (!active(token)) return;
      const changedElsewhere = paint(parsePreferences(preferences));
      result.textContent = '发送失败的邮件已重新排队。' + (changedElsewhere ? '其他窗口已更新偏好，当前修改已保留。' : dirty() ? '未保存的偏好修改已保留。' : '');
    } catch (error) { if (active(token)) result.textContent = requeued ? '发送失败的邮件已重新排队，暂时无法刷新状态。请点击“刷新状态”核对。' : errorMessage(error); }
    finally { finish(token); }
  });
  document.addEventListener('chunnel:session', event => {
    if (event.detail.authenticated && event.detail.id === owner) return;
    changed = true; operation = null; baseline = null; version = null; root.hidden = true;
    for (const name of fields) input(form, name).checked = false;
    result.textContent = ''; $('[data-notification-delivery-status]').textContent = ''; lock(false);
  });
  window.addEventListener('pageshow', event => { if (event.persisted && !root.hidden) void load(); });
  lock(false); window.ChunnelNotificationSettings = { load };
}
mountNotificationSettings();
