import { recognitionSocial, identityMutation, identityReceipt } from '../shared/recognition-data';
import type { IdentityMutation } from '../shared/recognition-data';
import { badge, medalDate, node } from './recognition-view';
import { required, field, report, busy, errorMessage, ownerKey, sessionClient } from './recognition-dom';

export function mountIdentity(root: HTMLElement): void {
  const status = required(root, '[data-identity-status]'), content = required(root, '[data-identity-content]');
  const apply = required<HTMLFormElement>(root, '[data-identity-apply]'), equip = required<HTMLFormElement>(root, '[data-identity-equip]');
  const details = required<HTMLDetailsElement>(root, 'details'), medals = required(root, '[data-identity-medals]');
  let data: ReturnType<typeof recognitionSocial> | undefined, loading = 0, serial = 0, writing = false, equipmentDirty = false;
  let pending: IdentityMutation | undefined;
  const client = sessionClient(() => {
    serial++; loading = 0; data = undefined; pending = undefined; writing = false; equipmentDirty = false;
    content.hidden = true; medals.replaceChildren(); apply.reset(); busy(equip, false); busy(apply, false);
    root.querySelector(':scope > .identity-badges')?.replaceChildren();
    document.querySelectorAll('[data-account-identity-badges]').forEach(el => el.replaceChildren());
    required(root, '[data-identity-application-state]').textContent = ''; report(status, '登录状态已变化，请重新读取身份。');
  });
  const active = (ticket: number, epoch: number) => ticket === serial && client.current(epoch) && root.isConnected;
  const selection = () => [...medals.querySelectorAll<HTMLInputElement>('input[name=medal]:checked:not(:disabled)')].map(b => b.value);
  equip.addEventListener('change', () => { equipmentDirty = true; });
  function render(): void {
    if (!data) return;
    const selected = equipmentDirty ? selection() : data.identity.equipped, previousKey = field(apply, 'key').value;
    content.hidden = false; medals.replaceChildren();
    data.identity.grants.forEach(g => {
      const b = data?.catalog.find(b => b.key === g.key); if (!b || !b.enabled || b.kind !== 'medal') return;
      const expired = !g.expires_at.startsWith('0001-') && Date.parse(g.expires_at) <= Date.now();
      const label = node('label'), box = node('input'); box.type = 'checkbox'; box.name = 'medal'; box.value = g.key; box.checked = selected.includes(g.key); box.disabled = expired;
      const text = node('span'); text.append(badge(b, { interactive: false, decorative: true }), node('span', b.name, 'identity-medal-name'), node('small', expired ? '已到期' : !g.expires_at.startsWith('0001-') ? '有效期至 ' + medalDate(g.expires_at) : '长期有效'));
      label.append(box, text); medals.append(label);
    });
    if (!medals.children.length) medals.append(node('p', '暂时还没有勋章，继续创作和交流吧。', 'recognition-help'));
    report(required(root, '[data-identity-limit]'), `最多展示 ${data.max_medals} 枚勋章，取消勾选后保留已获得的勋章。`);
    const app = data.identity.application, states = { pending: '申请审核中', approved: '已通过认证', rejected: '申请未通过', revoked: '认证已撤销' };
    const verified = data.identity.grants.some(g => data?.catalog.find(b => b.key === g.key)?.kind === 'verified' && Date.parse(g.granted_at) <= Date.now() && (g.expires_at.startsWith('0001-') || Date.parse(g.expires_at) > Date.now()));
    report(required(root, '[data-identity-application-state]'), app ? (app.status === 'approved' && !verified ? '认证已到期，可重新申请' : states[app.status]) + (app.reason ? ' · ' + app.reason : '') : verified ? '已获得站点认证，认证标记会显示在作者页与评论中。' : '完成审核后，认证标记将显示在作者页与评论中。');
    report(required(root, '[data-identity-help]'), data.verification_enabled ? data.verification_help : '站点暂未开放认证申请。');
    const options = data.catalog.filter(b => b.kind === 'verified' && b.enabled);
    field(apply, 'key').replaceChildren(...options.map(b => { const o = node('option', b.name); o.value = b.key; return o; }));
    if (options.some(b => b.key === previousKey)) field(apply, 'key').value = previousKey;
    apply.hidden = !data.verification_enabled || app?.status === 'pending' || verified || !options.length;
    let visible = root.querySelector(':scope > .identity-badges');
    if (!visible && data.badges.length) { visible = node('span', undefined, 'identity-badges'); required(root, 'header').after(visible); }
    visible?.replaceChildren(...data.badges.map(b => badge(b)));
    document.querySelectorAll('[data-account-identity-badges]').forEach(group => group.replaceChildren(...(data?.badges || []).map(b => badge(b))));
  }
  async function load(): Promise<void> {
    if (loading || writing) return;
    const ticket = ++serial, epoch = client.epoch(); loading = ticket; report(status, pending ? '正在核对上次身份操作…' : '正在读取身份…');
    try {
      const session = await client.session(); if (!active(ticket, epoch)) return;
      if (!session.authenticated || String(session.id) !== root.dataset.identityUser || !session.id) throw new Error('登录状态已变化，请刷新页面');
      const fresh = recognitionSocial(await client.request('/api/v1/social/recognition'), session.id); if (!active(ticket, epoch)) return;
      let message = '';
      if (pending) {
        const original = pending;
        try { identityReceipt({ identity: fresh.identity }, original, session.id); message = original.action === 'equip' ? '已核对：上次佩戴设置已保存。' : '已核对：上次认证申请已提交。'; }
        catch { message = fresh.identity.version === original.version ? '上次操作尚未确认生效，请检查输入后再次提交。' : '身份已变化，请核对当前状态后再操作。'; }
        pending = undefined;
      }
      data = fresh; render(); report(status, message);
    } catch (e) { if (active(ticket, epoch)) report(status, errorMessage(e), true); }
    finally { if (loading === ticket) loading = 0; }
  }
  window.NewPressIdentity = Object.freeze({ load });
  details.addEventListener('toggle', () => { if (details.open && !root.hidden && !data) void load(); });
  required(root, '[data-identity-refresh]').addEventListener('click', () => { void load(); });
  window.addEventListener('pageshow', e => { if (e.persisted && !root.hidden) void load(); });
  async function submit(form: HTMLFormElement, payload: Omit<IdentityMutation, 'user_id' | 'version'>): Promise<void> {
    if (writing || loading || !data) return;
    if (pending) { await load(); return; }
    let body: IdentityMutation;
    try { body = identityMutation({ ...payload, user_id: data.user_id, version: data.identity.version }); } catch (e) { report(status, errorMessage(e), true); return; }
    const ticket = ++serial, epoch = client.epoch(), credentials = ownerKey(client.client?.snapshot() || {}), selected = JSON.stringify(selection());
    writing = true; busy(form, true);
    try {
      const session = await client.session(); if (!active(ticket, epoch)) return;
      if (!session.authenticated || session.id !== body.user_id || ownerKey(session) !== credentials) throw new Error('登录状态已变化，请重新读取身份');
      pending = body;
      const response = await client.request('/api/v1/social/recognition', body); if (!active(ticket, epoch)) return;
      const identity = identityReceipt(response, body, session.id);
      pending = undefined; data = { ...data, identity };
      if (body.action === 'equip' && JSON.stringify(selection()) === selected) equipmentDirty = false;
      const message = body.action === 'equip' ? '佩戴设置已保存。' : '认证申请已提交，请等待审核。';
      report(status, message);
      try {
        const fresh = recognitionSocial(await client.request('/api/v1/social/recognition'), session.id); if (!active(ticket, epoch)) return;
        data = fresh; render(); report(status, message);
      } catch (e) { if (active(ticket, epoch)) report(status, message + '展示刷新未完成，可稍后刷新。', true); }
    } catch (e) { if (active(ticket, epoch)) report(status, errorMessage(e) + (pending ? '；再次提交将先核对上次结果。' : ''), true); }
    finally { if (active(ticket, epoch)) { writing = false; busy(form, false); } }
  }
  equip.addEventListener('submit', e => {
    e.preventDefault(); if (!data) return; const equipped = selection();
    if (equipped.length > data.max_medals) { report(status, `最多佩戴 ${data.max_medals} 枚勋章`, true); return; }
    void submit(equip, { action: 'equip', equipped });
  });
  apply.addEventListener('submit', e => {
    e.preventDefault(); void submit(apply, { action: 'apply', key: field(apply, 'key').value, label: field(apply, 'label').value.trim(), evidence: field(apply, 'evidence').value.trim().replace(/\r?\n/g, ' ') });
  });
}
