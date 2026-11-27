export function mountReferrals(): void {
  const host = document.querySelector<HTMLElement>('[data-account-room][data-account-user] .account-side');
  if (!host || host.querySelector('[data-referrals]')) return;
  const section = document.createElement('section'); section.className = 'account-side-card'; section.dataset.referrals = '';
  const heading = document.createElement('h2'); heading.textContent = '邀请好友';
  const copy = document.createElement('p'), status = document.createElement('p'); status.setAttribute('role', 'status');
  const list = document.createElement('div');
  const generate = document.createElement('button'), claim = document.createElement('button');
  generate.type = claim.type = 'button'; generate.className = claim.className = 'ui-button ui-button-secondary ui-button-small'; generate.textContent = '生成邀请码'; claim.textContent = '领取邀请奖励';
  const actions = document.createElement('div'); actions.className = 'account-shortcuts'; actions.append(generate, claim);
  section.append(heading, copy, list, actions, status); host.prepend(section);
  async function api(body?: Record<string, unknown>) {
    const session = body ? await fetch('/api/v1/social/session', { credentials: 'same-origin', cache: 'no-store' }).then(response => response.json()) : null;
    const response = await fetch('/api/v1/social/referrals', { credentials: 'same-origin', cache: 'no-store', ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': String(session?.csrf_token || '') }, body: JSON.stringify(body) } : {}) });
    const data = await response.json();
    if (!response.ok) { const fields = data.error?.details?.fields; throw new Error(fields ? Object.values(fields).join('；') : data.error?.message || '暂时无法完成，请重试'); }
    return data;
  }
  function render(data: { enabled: boolean; invited: number; max_codes: number; daily_codes: number; min_age_days: number; verified_email: boolean; verified_phone: boolean; items: { id: string; code: string; url: string; used: number; max_uses: number; enabled: boolean; status: string; expires_at?: string }[] }) {
    copy.textContent = `已邀请 ${data.invited} 人。累计最多生成 ${data.max_codes} 个，每日最多 ${data.daily_codes} 个。${data.min_age_days ? `注册满 ${data.min_age_days} 天可生成。` : ''}${data.verified_email ? '需验证邮箱。' : ''}${data.verified_phone ? '需验证手机。' : ''}`;
    generate.hidden = !data.enabled;
    if (!data.enabled) copy.textContent = `邀请分享暂未开启，已有邀请 ${data.invited} 人。`;
    list.replaceChildren();
    for (const item of data.items) {
      const row = document.createElement('div'); row.className = 'account-referral-code';
      const input = document.createElement('input'); input.readOnly = true; input.value = item.url; input.setAttribute('aria-label', '邀请分享链接');
      const labels: Record<string, string> = { active: '可使用', disabled: '已停用', expired: '已过期', exhausted: '次数已用完', scheduled: '尚未生效' };
      const details = document.createElement('small'); details.textContent = `已使用 ${item.used} / ${item.max_uses || '不限'} 次 · ${labels[item.status] || '不可用'}${item.expires_at ? ' · ' + new Date(item.expires_at).toLocaleDateString() + ' 到期' : ''}`;
      const share = document.createElement('button'); share.type = 'button'; share.className = 'ui-button ui-button-secondary ui-button-small'; share.textContent = '复制链接';
      share.addEventListener('click', async () => { try { await navigator.clipboard.writeText(item.url); status.textContent = '邀请链接已复制。'; } catch { input.select(); status.textContent = '请复制已选中的链接。'; } });
      const toggle = document.createElement('button'); toggle.type = 'button'; toggle.className = share.className; toggle.textContent = item.enabled ? '停用' : '启用';
      toggle.addEventListener('click', () => { void mutate({ action: 'toggle', id: item.id, enabled: !item.enabled }); });
      row.append(input, details, share, toggle); list.append(row);
    }
  }
  async function mutate(body: Record<string, unknown>) {
    section.querySelectorAll('button').forEach(button => { button.disabled = true; }); status.textContent = '正在处理…';
    try { render(await api(body)); status.textContent = body.action === 'claim' ? '已核对符合条件的奖励；未完成验证的邀请将在符合条件后发放。' : '已更新。'; }
    catch (error) { status.textContent = error instanceof Error ? error.message : '请求失败，请重试。'; }
    finally { section.querySelectorAll('button').forEach(button => { button.disabled = false; }); }
  }
  generate.addEventListener('click', () => { void mutate({ action: 'generate' }); });
  claim.addEventListener('click', () => { void mutate({ action: 'claim' }); });
  void api().then(render).catch(error => { status.textContent = error.message; });
}
