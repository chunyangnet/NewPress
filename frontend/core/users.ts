import type {} from '../shared/account-types';
import type {} from './media-types';
import { required, control } from './ledger-admin-dom';
import type { Control } from './ledger-admin-dom';
import { failure, nonce, node as makeNode } from '../shared/ledger-data';
import { adminAccountRuntime } from './admin-account-runtime';
import { adminProgressionAccount, adminWallet } from './ledger-admin-data';
import type { AdminProgressionSnapshot, AdminWallet } from './ledger-admin-data';
import { recognitionSettings, recognitionAccount, recognitionReceipt, userCurrencies, userOperation, userReceipt } from '../shared/users-admin-data';
import type { RecognitionSettings, RecognitionAccount, UserMutation, UserOperation } from '../shared/users-admin-data';
type Field = Control | HTMLButtonElement | HTMLFieldSetElement;
type Operation = UserOperation & { controls: [Field, boolean][]; signature: string; changed?: boolean };
const formFields = (form: HTMLFormElement): Field[] => [...form.elements].filter((el): el is Field => el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement || el instanceof HTMLButtonElement || el instanceof HTMLFieldSetElement);
const drafts = (form: HTMLFormElement) => [...form.querySelectorAll<Control>('input,select,textarea')].filter(el=>el.type!=='hidden'||el.hasAttribute('data-user-value')).map(el=>({name:el.name,value:el.value,checked:el instanceof HTMLInputElement?el.checked:false,selected:el instanceof HTMLSelectElement&&el.multiple?[...el.selectedOptions].map(o=>o.value):undefined}));
const restoreDraft = (form: HTMLFormElement, values: ReturnType<typeof drafts>) => { for (const el of form.querySelectorAll<Control>('input,select,textarea')) { const saved=values.find(v=>v.name===el.name&&(!(el instanceof HTMLInputElement)||!['checkbox','radio'].includes(el.type)||v.value===el.value)); if(!saved)continue; el.value=saved.value;if(el instanceof HTMLInputElement)el.checked=saved.checked;if(el instanceof HTMLSelectElement&&saved.selected)for(const option of el.options)option.selected=saved.selected.includes(option.value); } };
(() => {
  'use strict';
  const host = document.querySelector<HTMLElement>('.admin-users-page'); if(!host||host.dataset.userWorkspaceReady==='true'||host.dataset.userWorkspaceRetired==='true')return;const page=host;
  const initial = page.querySelector<HTMLDialogElement>('[data-user-dialog]');
  if (!initial) return; let dialog: HTMLDialogElement = initial;
  let opener: HTMLAnchorElement | null = null, loading: AbortController | null = null, busy = false, generation = 0, recovered: UserOperation | null = null, pendingInvalid = false, nativeUncertain=false;
  const baselines = new Map<HTMLFormElement,string>(), operations = new Map<HTMLFormElement,Operation>(), loaded = new Set<string>();
  const node = <K extends keyof HTMLElementTagNameMap>(tag: K,text='',cls='') => makeNode(tag,cls,text);
  const signature = (form: HTMLFormElement) => JSON.stringify(drafts(form));
  const track = (form: HTMLFormElement) => { baselines.set(form, signature(form)); return form; };
  const dirty = () => [...baselines].some(([form, value]) => form.isConnected && signature(form) !== value);
  const feedback = (message = '', error = false) => { const el = required(dialog,'[data-user-feedback]'); el.textContent = message; el.dataset.tone = error ? 'error' : 'success'; };
  const dateLabel = (value: string) => window.ChunnelTime?.formatDateTime(value) || '—';
  const userQuery = () => '?username=' + encodeURIComponent(dialog.dataset.username || '');
  const button = (text: string, type: 'submit'|'button' = 'submit') => { const el = node('button', text, 'ui-button ui-button-primary'); el.type = type; return el; };
  const input = (name: string, value = '', type = 'text') => { const el = node('input'); Object.assign(el, { name, value, type }); return el; };
  const label = (text: string, control: HTMLElement, help = '') => { const el = node('label'); el.append(node('span', text), control); if (help) el.append(node('small', help)); return el; };
  const select = (name: string, items: string[][], value?: string) => { const el = node('select'); el.name = name; items.forEach(([key, text]) => el.add(new Option(text || '', key || '', false, key === value))); return el; };
  const reason = () => { const el = input('reason'); el.required = true; el.maxLength = 200; el.placeholder = '填写本次调整的原因'; return label('调整原因', el, '将保存在账户操作记录中。'); };
  const card = (title: string) => { const el = node('article', undefined, 'admin-user-account-card'); el.append(node('h3', title)); return el; };
  const footer = (form: HTMLFormElement, text: string) => { const el = node('footer', undefined, 'admin-user-form-footer'); el.append(button(text)); form.append(el); track(form); return form; };
  const facts = (items: string[][]) => { const el = node('dl', undefined, 'admin-user-facts'); items.forEach(([key, value]) => { const row = node('div'); row.append(node('dt', key), node('dd', value)); el.append(row); }); return el; };
  const owner=page.dataset.userId || '',csrf=page.dataset.csrf || '',readOnly=page.dataset.readOnly==='true';
  const controllers=new Set<AbortController>();
  const runtime=adminAccountRuntime(page,owner,csrf,readOnly,()=>{
    generation++;loading?.abort();controllers.forEach(c=>c.abort());
    dialog.querySelectorAll<HTMLInputElement>('input[type=password]').forEach(field=>{field.value='';});
    dialog.querySelectorAll<HTMLElement>('[data-user-membership],[data-user-wallets],[data-user-recognition]').forEach(holder=>holder.replaceChildren());
    formFieldsList().forEach(el=>{el.disabled=true;});feedback('登录状态或页面已变化，请重新载入后继续。',true);
  });
  const formFieldsList=()=>[...dialog.querySelectorAll<HTMLFormElement>('form')].flatMap(formFields);
  const pendingKey=()=> 'newpress.users.admin.pending.'+owner+'.'+(dialog.dataset.userId || '0');
  const nativeKey=()=>pendingKey()+'.native';
  const request=(url:string,body?:unknown)=>runtime.request(url,body);
  function keep(operation:UserOperation){const {url,body,saved,actor,target,precision,uncertain}=operation;sessionStorage.setItem(pendingKey(),JSON.stringify({url,body,saved,actor,target,precision,uncertain}));}
  function forget(){sessionStorage.removeItem(pendingKey());sessionStorage.removeItem(nativeKey());recovered=null;pendingInvalid=false;nativeUncertain=false;dialog.querySelector('[data-user-recover]')?.remove();}
  function checkAccount<T>(result:T,id:string|number,target=dialog.dataset.userId){if(String(id)!==target)throw new Error('账户信息已变化，请重新打开编辑窗口。');return result;}
  async function html(url:string,init:RequestInit={}){
    if(!runtime.alive())throw new DOMException('页面已失效','AbortError');
    const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),15000);controllers.add(controller);
    try{const response=await fetch(url,{...init,credentials:'same-origin',cache:'no-store',signal:controller.signal});if(response.status===401||response.status===403)runtime.retire();const text=await response.text();if(!runtime.alive())throw new DOMException('页面已失效','AbortError');return {response,doc:new DOMParser().parseFromString(text,'text/html')};}
    finally{clearTimeout(timer);controllers.delete(controller);}
  }
  async function sendOperation(operation:UserOperation){
    if(operation.saved)return;
    if(operation.url.endsWith('/recognition/account')&&operation.uncertain){
      const account=recognitionAccount(await request(operation.url+userQuery()),operation.target,dialog.dataset.username||'');
      try{recognitionReceipt({identity:account.identity},operation);operation.saved=true;keep(operation);return;}catch{if(account.identity.version!==Number(operation.body.version))throw new Error('身份记录已发生后续变更，请核对账户历史后重新载入；不会重复调整。');}
    }
    operation.uncertain=true;keep(operation);
    userReceipt(await request(operation.url,operation.body),operation);operation.saved=true;keep(operation);
  }
  function recovery(){
    dialog.querySelector('[data-user-recover]')?.remove();
    if(!recovered&&!pendingInvalid&&!nativeUncertain)return;
    const row=node('div',undefined,'admin-user-account-card');row.dataset.userRecover='';
    row.append(node('p',pendingInvalid?'浏览器中的待核对记录无法读取，请先核对账户操作历史。':nativeUncertain?'账户资料或密码提交结果尚未确认，请先读取账户核对后继续。':'此账户有待核对的调整，请确认原操作结果后继续。'));
    const retry=button('核对原操作','button');retry.disabled=pendingInvalid||nativeUncertain;
    retry.addEventListener('click',async()=>{
      const operation=recovered;if(!operation||busy||!runtime.alive())return;busy=true;retry.disabled=true;
      try{await sendOperation(operation);forget();loaded.clear();for(const holder of dialog.querySelectorAll<HTMLElement>('[data-user-membership],[data-user-wallets],[data-user-recognition]'))holder.replaceChildren();feedback('原操作已确认，重新选择资产页可读取当前状态。');}
      catch(error){if(runtime.alive())feedback(failure(error).message,true);}finally{busy=false;retry.disabled=pendingInvalid||!runtime.alive();}
    });row.append(retry);
    const read=button('读取当前账户核对','button'),done=button('已核对，继续编辑','button');done.disabled=true;
    read.addEventListener('click',async()=>{if(busy||!runtime.alive())return;busy=true;read.disabled=true;try{
      const target=dialog.dataset.userId||'0';
      if(target==='0'){const raw=sessionStorage.getItem(nativeKey());const value:unknown=raw?JSON.parse(raw):null;const username=value&&typeof value==='object'&&'username'in value&&typeof value.username==='string'?value.username:'';const {response,doc}=await html('/admin/users/?q='+encodeURIComponent(username));if(!response.ok)throw new Error('用户列表读取失败');const index=doc.querySelector('.admin-users-index');if(index)required(page,'.admin-users-index').replaceWith(index);row.append(node('p','已刷新用户列表，请核对刚才填写的用户名是否已存在。'));}
      else {const {response,doc}=await html('/admin/users/?edit='+encodeURIComponent(target));if(!response.ok)throw new Error('账户读取失败');const incoming=doc.querySelector<HTMLDialogElement>('[data-user-dialog]');if(!incoming||incoming.dataset.userId!==target)throw new Error('账户核对失败');const profile=required<HTMLFormElement>(incoming,'#admin-user-form');row.append(node('p','当前资料：'+control(profile,'username').value+' · '+control(profile,'display_name').value+' · 版本 '+control(profile,'version').value));
        if(recovered?.url.endsWith('/recognition/account')){const data=recognitionAccount(await request('/api/v1/admin/recognition/account'+userQuery()),target,dialog.dataset.username||'');row.append(node('p',data.identity.history.slice(-5).map(h=>h.action+' · '+h.reason+' · '+dateLabel(h.created_at)).join('\n')));}
        if(recovered?.url.includes('/progression/')){const data=await progression();row.append(node('p',data.entries.slice(0,5).map(e=>e.kind+' · '+e.delta+' · '+e.reason).join('\n')));}
        if(recovered?.url.includes('/currencies/')){const key=recovered.url.split('/')[5]||'';const data=adminWallet(await request('/api/v1/admin/currencies/'+key+'/wallet'+userQuery()),key,dialog.dataset.username||'',target);row.append(node('p',data.entries.slice(0,5).map(e=>e.delta+' · '+e.reason+' · '+dateLabel(e.created_at)).join('\n')));}
      }
      done.disabled=false;
    }catch(error){if(runtime.alive())feedback(failure(error).message,true);}finally{busy=false;read.disabled=!runtime.alive();}});
    done.addEventListener('click',()=>{if(busy||done.disabled||!runtime.alive()||!window.confirm('已核对实际账户和操作记录，结束本次结果核对？这不会再次提交刚才的操作。'))return;try{forget();operations.clear();feedback('核对记录已结束，请按当前账户状态继续编辑。');}catch{feedback('无法更新核对记录，请检查浏览器存储。',true);}});
    row.append(read,done);required(dialog,'[data-user-feedback]').after(row);
  }
  function preservePaint(form:HTMLFormElement,paint:()=>void){
    const operation=operations.get(form),holder=form.parentElement?.parentElement;
    const newer=operation&&signature(form)!==operation.signature?drafts(form):null;
    paint();if(newer){const next=holder?.querySelector('form');if(next)restoreDraft(next,newer);if(operation)operation.changed=true;}
  }
  async function perform(form:HTMLFormElement,url:string,body:UserMutation,refresh:()=>Promise<void>,precision=0){
    if(busy||readOnly||!runtime.alive())return;
    if(recovered||pendingInvalid||nativeUncertain||operations.size&&!operations.has(form)){feedback('请先核对上一项调整的结果。',true);return;}
    const prior=operations.get(form),operation:Operation=prior||{url,body:{...body,...(body.request_id!==undefined?{request_id:nonce()}:{})},saved:false,actor:owner,target:dialog.dataset.userId||'',precision,uncertain:false,controls:formFields(form).map(el=>[el,el.disabled]),signature:signature(form)};
    try{userOperation(operation,owner,operation.target);keep(operation);}catch(error){feedback('无法保存待核对操作：'+failure(error).message,true);return;}
    operations.set(form,operation);busy=true;const serial=generation,wasUncertain=operation.uncertain;form.setAttribute('aria-busy','true');
    operation.controls.forEach(([el])=>{if(el instanceof HTMLButtonElement)el.disabled=true;});feedback(operation.saved?'正在更新账户信息…':'正在保存…');
    try{
      await sendOperation(operation);if(serial!==generation||!runtime.alive())return;
      await refresh();if(serial!==generation||!runtime.alive())return;
      const newer=operation.changed||form.isConnected&&signature(form)!==operation.signature;
      forget();operations.delete(form);baselines.delete(form);if(form.isConnected){if(newer)baselines.set(form,operation.signature);else track(form);}
      feedback(newer?'本次调整已保存；后续输入仍待保存。':'已保存，账户信息已更新。');
    }catch(caught){if(!runtime.alive())return;const error=failure(caught);if(error.status&&error.status<500&&!operation.saved&&!wasUncertain){try{forget();operations.delete(form);}catch{}}feedback(operation.saved?'修改已保存，显示信息暂未更新。点击重试刷新即可。':failure(error).message,true);}
    finally{busy=false;form.removeAttribute('aria-busy');operation.controls.forEach(([el,disabled])=>{el.disabled=disabled||!runtime.alive();});if(operations.has(form))required<HTMLButtonElement>(form,'[type="submit"]').textContent='重试确认结果';}
  }
  function activate(name: string, focus = false) {
    dialog.querySelectorAll<HTMLElement>('[data-user-tab]').forEach(tab => { const active = tab.dataset.userTab === name; tab.setAttribute('aria-selected', String(active)); tab.tabIndex = active ? 0 : -1; if (active && focus) tab.focus(); });
    dialog.querySelectorAll<HTMLElement>('[data-user-panel]').forEach(panel => { panel.hidden = panel.dataset.userPanel !== name; });
    required(dialog,'.admin-user-dialog-body').scrollTop = 0;
    if (['membership', 'wallets', 'recognition'].includes(name)) loadPanel(name);
  }
  async function loadPanel(name: string) {
    if (loaded.has(name) || !runtime.alive()) return;
    const target = required(dialog,`[data-user-${name}]`), serial = generation, targetID=dialog.dataset.userId||'',username=dialog.dataset.username||'';
    if (dialog.dataset.assets !== 'true') { target.replaceChildren(node('p', '此账户的会员、资产和身份调整需要用户管理及站点设置权限。', 'admin-user-empty')); return; }
    loaded.add(name); target.replaceChildren(node('p', '正在读取账户信息…', 'admin-user-empty'));
    try {
      if (name === 'membership') {
        const data = adminProgressionAccount(await request('/api/v1/admin/progression/account' + userQuery()),username,targetID);
        checkAccount(data, data.account.user_id); if (serial !== generation) return;
        const member = node('div'), experience = node('div'); target.replaceChildren(member, experience);
        paintMembership(member, data); paintExperience(experience, data);
      } else if (name === 'wallets') {
        const data = {currencies:userCurrencies(await request('/api/v1/admin/currencies'))};
        const wallets = await Promise.all(data.currencies.map(async item => {
          const url = '/api/v1/admin/currencies/' + encodeURIComponent(item.key);
          try { const wallet = adminWallet(await request(url + '/wallet' + userQuery()),item.key,username,targetID); return { url, wallet }; }
          catch (error) { return { name: item.settings.name, error:failure(error) }; }
        }));
        if (serial !== generation) return; target.replaceChildren();
        for (const item of wallets) { const holder = node('div'); target.append(holder); if (item.error) holder.append(node('p', item.name + '：' + item.error.message, 'admin-user-empty')); else if(item.wallet)paintWallet(holder, item.url, item.wallet); }
        if (!wallets.length) target.append(node('p', '尚未配置积分或其他虚拟货币。', 'admin-user-empty'));
      } else {
        const [config, account] = await Promise.all([request('/api/v1/admin/recognition').then(recognitionSettings), request('/api/v1/admin/recognition/account' + userQuery()).then(value=>recognitionAccount(value,targetID,username))]);
        checkAccount(account, account.user_id); if (serial !== generation) return;
        paintRecognition(target, config, account);
      }
    } catch (error) {
      if (serial !== generation) return;
      loaded.delete(name); target.replaceChildren(node('p', failure(error).message, 'admin-user-empty'));
      const retry = button('重新载入', 'button'); retry.addEventListener('click', () => loadPanel(name)); target.append(retry);
    }
  }
  function localTime(value: string | Date) { return (window.ChunnelTime?.formatDateTime(value)||'').replace(' ', 'T'); }
  function instant(value: string) {
    if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) throw new Error('请填写完整的会员起止时间。');
    const wanted = Date.parse(value + ':00Z'); let guess = wanted;
    for (let i = 0; i < 4; i++) {
      const shown = localTime(new Date(guess)); if (shown === value) return new Date(guess).toISOString();
      guess += wanted - Date.parse(shown + ':00Z');
    }
    throw new Error('该时间在站点时区中不存在，请选择其他时间。');
  }
  async function progression() { const target=dialog.dataset.userId||'',username=dialog.dataset.username||'';return adminProgressionAccount(await request('/api/v1/admin/progression/account'+userQuery()),username,target); }
  function paintMembership(holder: HTMLElement, data: AdminProgressionSnapshot) {
    const member = data.account.membership, cfg = data.settings;
    const panel = card('会员权益');
    const planName = cfg.membership.plans.find(plan => plan.key === member.plan)?.name || '暂无会员';
    panel.append(facts([['当前会员', planName], ['状态', member.revoked ? '已撤销' : data.membership_active ? '生效中' : member.plan ? '未生效或已到期' : '未开通'], ['生效时间', dateLabel(member.since)], ['到期时间', dateLabel(member.until)]]));
    const form = node('form'), fields = node('fieldset', undefined, 'admin-user-fields');
    const mode = select('mode', [['set', '设置套餐与有效期'], ...(member.plan && !member.revoked ? [['revoke', '撤销会员']] : [])], 'set');
    const plans = select('plan', cfg.membership.plans.map(p => [p.key, p.name + (p.enabled ? '' : '（已停用）')]), member.plan || cfg.membership.plans[0]?.key);
    const since = input('since', member.plan ? localTime(member.since) : localTime(new Date()), 'datetime-local');
    const until = input('until', member.plan ? localTime(member.until) : localTime(new Date(Date.now() + 30 * 86400000)), 'datetime-local');
    since.required = until.required = true;
    fields.append(label('操作', mode), label('会员套餐', plans), label('生效时间', since), label('到期时间', until), reason());
    mode.addEventListener('change', () => { plans.disabled = since.disabled = until.disabled = mode.value === 'revoke'; });
    form.append(fields, node('p', '时间按站点时区 ' + (window.ChunnelTime?.zone() || 'Asia/Shanghai') + ' 保存。', 'admin-user-help'));
    footer(form, '保存会员'); panel.append(form); holder.replaceChildren(panel);
    if (data.pending_order) { fields.disabled = true; required<HTMLButtonElement>(form,'[type="submit"]').disabled = true; panel.append(node('p', '有尚未处理完成的会员订单，请先在会员管理中完成或取消该订单。', 'admin-user-help')); }
    form.addEventListener('submit', event => {
      event.preventDefault();
      try {
        const body: UserMutation = { user_id: dialog.dataset.userId || '', version: cfg.version, member_version: member.version, action: mode.value==='revoke'?'revoke':'set', plan: mode.value === 'revoke' ? '' : plans.value, days: 0, reason: control(form,'reason').value.trim(), request_id: '' };
        if (!operations.has(form)&&mode.value === 'set') { body.since = member.plan && since.value===localTime(member.since)?member.since:instant(since.value); body.until = member.plan && until.value===localTime(member.until)?member.until:instant(until.value); if (Date.parse(body.until) <= Date.parse(body.since)) throw new Error('到期时间必须晚于生效时间。'); }
        perform(form, '/api/v1/admin/progression/membership', body, async () => {const fresh=await progression();preservePaint(form,()=>paintMembership(holder,fresh));});
      } catch (error) { feedback(failure(error).message, true); }
    });
  }
  function paintExperience(holder: HTMLElement, data: AdminProgressionSnapshot) {
    const panel = card('经验与等级');
    panel.append(facts([['当前经验', data.account.experience], ['当前等级', data.level?.name || '未启用等级'], ['下一级', data.next_level ? `${data.next_level.name} · ${data.next_level.threshold} 经验` : '—']]));
    const form = node('form'), fields = node('fieldset', undefined, 'admin-user-fields');
    const delta = input('delta'); delta.required = true; delta.pattern = '-?[0-9]+'; delta.placeholder = '例如 100 或 -50';
    fields.append(label('调整经验', delta, '正数增加，负数扣减；等级随经验自动计算。'), reason()); form.append(fields); footer(form, '保存经验调整'); panel.append(form); holder.replaceChildren(panel);
    form.addEventListener('submit', event => { event.preventDefault(); if (!operations.has(form)&&(!/^-?[0-9]+$/.test(delta.value) || BigInt(delta.value) === 0n)) return feedback('请输入非零整数经验值。', true); perform(form, '/api/v1/admin/progression/experience', { user_id: dialog.dataset.userId || '', version: data.settings.version, delta: operations.get(form)?.body.delta||BigInt(delta.value).toString(), reason: control(form,'reason').value.trim(), request_id: '' }, async () => {const fresh=await progression();preservePaint(form,()=>paintExperience(holder,fresh));}); });
  }
  function paintWallet(holder: HTMLElement, url: string, wallet: AdminWallet) {
    const panel = card(wallet.settings.name);
    panel.append(facts([['当前余额', wallet.balance + ' ' + wallet.settings.symbol], ['币种状态', wallet.settings.enabled ? '已启用' : '已停用']]));
    const form = node('form'), fields = node('fieldset', undefined, 'admin-user-fields');
    const delta = input('delta'); delta.required = true; delta.placeholder = '例如 100 或 -20'; delta.inputMode = 'decimal';
    fields.append(label('调整数量', delta, `正数增加，负数扣减；最多 ${wallet.settings.precision} 位小数。`), reason()); form.append(fields); footer(form, '保存' + wallet.settings.name + '调整'); panel.append(form);
    if (wallet.entries.length) {
      const details = node('details', undefined, 'admin-user-history'); details.append(node('summary', '最近流水'));
      const list = node('ul'); for (const e of wallet.entries.slice(0, 8)) list.append(node('li', `${e.delta} ${wallet.settings.symbol} · ${e.reason || e.label} · ${dateLabel(e.created_at)}`)); details.append(list); panel.append(details);
    }
    holder.replaceChildren(panel);
    form.addEventListener('submit', event => { event.preventDefault(); perform(form, url + '/adjust', { user_id: dialog.dataset.userId || '', version: wallet.settings.version, delta: delta.value.trim(), reason: control(form,'reason').value.trim(), request_id: '' }, async () => { const fresh = adminWallet(await request(url + '/wallet' + userQuery()),wallet.currency_key,dialog.dataset.username || '',wallet.user_id); preservePaint(form,()=>paintWallet(holder, url, fresh)); },wallet.settings.precision); });
  }
  function paintRecognition(holder: HTMLElement, settings: RecognitionSettings, account: RecognitionAccount) {
    let identity = account.identity;
    const panel = card('头衔、勋章与认证'), summary = node('div');
    const form = node('form'), fields = node('fieldset', undefined, 'admin-user-fields');
    const action = select('action', [['grant', '授予或更新'], ['revoke', '撤销'], ['approve', '通过认证申请'], ['reject', '拒绝认证申请']], 'grant');
    const key = select('key', settings.badges.map(b => [b.key, ({ title: '头衔', medal: '勋章', verified: '认证' })[b.kind] + ' · ' + b.name + (b.enabled ? '' : '（已停用）')]), '');
    const title = input('label'), days = input('days', '0', 'number'); title.maxLength = 30; days.min = '0'; days.max = '3650'; days.required = true;
    fields.append(label('操作', action), label('身份标识', key), label('显示名称', title, '留空使用默认名称。'), label('有效天数', days, '0 表示永久有效。'), reason()); form.append(fields); footer(form, '保存身份调整');
    const equipped = node('form'), choices = node('fieldset', undefined, 'admin-user-medal-options'); choices.append(node('legend', '展示的勋章'));
    equipped.append(choices); footer(equipped, '保存勋章展示');
    const paintSummary = () => {
      summary.replaceChildren();
      for (const grant of identity.grants) { const badge = settings.badges.find(b => b.key === grant.key); summary.append(node('p', `${badge?.name || grant.key}${grant.label ? ' · ' + grant.label : ''} · ${grant.expires_at.startsWith('0001-') ? '永久有效' : '到期 ' + dateLabel(grant.expires_at)}`, 'admin-user-grant')); }
      if (!identity.grants.length) summary.append(node('p', '尚未授予头衔、勋章或认证。', 'admin-user-help'));
      if (identity.application) summary.append(node('p', `认证申请：${identity.application.label} · ${identity.application.status} · ${identity.application.evidence || ''}`, 'admin-user-help'));
      const selected = new Set([...choices.querySelectorAll<HTMLInputElement>('input:checked')].map(el => el.value));
      const preserve = baselines.has(equipped) && signature(equipped) !== baselines.get(equipped);
      choices.replaceChildren(node('legend', '展示的勋章（最多 ' + settings.max_medals + ' 枚）'));
      for (const grant of identity.grants) {
        const badge = settings.badges.find(b => b.key === grant.key);
        if (badge?.kind !== 'medal' || !badge.enabled || (!grant.expires_at.startsWith('0001-') && Date.parse(grant.expires_at) <= Date.now())) continue;
        const el = input('equipped', grant.key, 'checkbox'); el.checked = preserve ? selected.has(grant.key) : identity.equipped.includes(grant.key); choices.append(label(badge.name, el));
      }
      if (!preserve) track(equipped);
    };
    paintSummary(); panel.append(summary, form, equipped); holder.replaceChildren(panel);
    const refresh = async (savedForm: HTMLFormElement) => { const fresh = recognitionAccount(await request('/api/v1/admin/recognition/account' + userQuery()),dialog.dataset.userId || '',dialog.dataset.username || ''); identity = fresh.identity; const operation=operations.get(savedForm);if(operation)operation.changed=signature(savedForm)!==operation.signature; if (savedForm === form && !operation?.changed) {form.reset();if(operation)operation.signature=signature(form);} if(savedForm===equipped&&!operation?.changed)baselines.delete(equipped); paintSummary(); };
    form.addEventListener('submit', event => { event.preventDefault(); perform(form, '/api/v1/admin/recognition/account', { user_id: Number(dialog.dataset.userId), version: identity.version, action: action.value as 'grant'|'revoke'|'approve'|'reject', key: key.value, label: title.value.trim(), evidence: '', reason: control(form,'reason').value.trim(), days: Number(days.value) }, () => refresh(form)); });
    equipped.addEventListener('submit', event => { event.preventDefault(); perform(equipped, '/api/v1/admin/recognition/account', { user_id: Number(dialog.dataset.userId), version: identity.version, action: 'equip', key: '', label: '', evidence: '', reason: '', days: 0, equipped: [...choices.querySelectorAll<HTMLInputElement>('input:checked')].map(el => el.value) }, () => refresh(equipped)); });
  }
  function close(force = false) {
    if (busy || operations.size || recovered || pendingInvalid || nativeUncertain) { feedback('正在确认保存结果，请完成或重试当前操作后再关闭。', true); return; }
    if (!force && dirty()) { required(dialog,'[data-user-discard]').hidden = false; required(dialog,'[data-user-keep]').focus(); return; }
    dialog.close(); document.body.classList.remove('admin-user-dialog-open');
    const target = opener?.isConnected ? opener : [...page.querySelectorAll<HTMLAnchorElement>('[data-user-edit]')].find(link => new URL(link.href).searchParams.get('edit') === dialog.dataset.userId) || page.querySelector<HTMLElement>('[data-user-create]');
    target?.focus();
  }
  function mount(next: HTMLDialogElement) {
    if (dialog !== next) { dialog.replaceWith(next); dialog = next; }
    generation++; baselines.clear(); operations.clear(); loaded.clear();
    dialog.querySelectorAll<HTMLFormElement>('form[data-user-native]').forEach(track);
    dialog.addEventListener('cancel', event => { event.preventDefault(); close(); });
    dialog.addEventListener('close', () => { document.body.classList.remove('admin-user-dialog-open'); });
    dialog.addEventListener('click', event => { if(!(event.target instanceof Element))return;
      if (event.target.closest('[data-user-close]')) close();
      if (event.target.closest('[data-user-discard-close]')) close(true);
      if (event.target.closest('[data-user-keep]')) required(dialog,'[data-user-discard]').hidden = true;
      const tab = event.target.closest<HTMLElement>('[data-user-tab]'); if (tab?.dataset.userTab) activate(tab.dataset.userTab);
      const choose = event.target.closest<HTMLElement>('[data-user-avatar-choose]'), remove = event.target.closest('[data-user-avatar-remove]');
      if (choose || remove) {
        const field = required(dialog,'[data-user-avatar]'), value = required<HTMLInputElement>(field,'[name="avatar_media_id"]'), preview = required(field,'[data-user-avatar-preview]');
        const update = (id: number | string, url: string) => { value.value = String(id); if (url) { const img = node('img'); Object.assign(img, { src: url, alt: '所选头像', width: 48, height: 48 }); preview.replaceChildren(img); } else preview.replaceChildren(node('span', '无', 'admin-user-avatar')); value.dispatchEvent(new Event('input', { bubbles: true })); };
        if (remove) update(0, '');
        else if (choose && window.NewPressMediaPicker) window.NewPressMediaPicker.open(choose, { title: '选择账户头像', uploadSource: 'avatar', disabled: () => busy || value.matches(':disabled') || !value.isConnected, onSelect: (url, item) => update(item.id, url) });
        else feedback('图片选择器正在载入，请稍后重试。', true);
      }
      if (event.target === dialog) { const rect = dialog.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) close(); }
    });
    dialog.addEventListener('keydown', event => { if(!(event.target instanceof HTMLElement))return;
      if (!event.target.matches('[data-user-tab]') || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      const tabs = [...dialog.querySelectorAll<HTMLElement>('[data-user-tab]')], index = tabs.indexOf(event.target);
      const nextIndex = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
      event.preventDefault(); const next=tabs[nextIndex]?.dataset.userTab;if(next)activate(next,true);
    });
    dialog.addEventListener('submit', nativeSubmit);
    try{const raw=sessionStorage.getItem(pendingKey());recovered=raw?userOperation(JSON.parse(raw),owner,dialog.dataset.userId||''):null;nativeUncertain=sessionStorage.getItem(nativeKey())!==null;pendingInvalid=false;}catch{recovered=null;pendingInvalid=true;}recovery();dialog.showModal(); document.body.classList.add('admin-user-dialog-open');
    if (dialog.querySelector('#admin-user-password .has-error')) activate('security');
    dialog.querySelector<HTMLElement>('.has-error input, .has-error textarea, [name="username"]')?.focus();
  }
  async function openEditor(link: HTMLAnchorElement) {
    if (busy || dialog.open || !runtime.alive()) return;
    loading?.abort(); const controller = new AbortController(); loading = controller; opener = link;
    link.setAttribute('aria-busy', 'true');
    try {
      const {response,doc} = await html(link.href);
      const next = doc.querySelector<HTMLDialogElement>('[data-user-dialog]');
      if (!response.ok || !next) throw new Error('用户资料暂时无法载入，请刷新页面后重试。');
      if (loading === controller && runtime.alive()) { if(next.dataset.csrf!==csrf){runtime.retire();return;} mount(next); }
    } catch (error) { if (!controller.signal.aborted) window.ChunnelAdmin?.showToast(failure(error).message, 'error'); }
    finally { link.removeAttribute('aria-busy'); if (loading === controller) loading = null; }
  }
  async function refreshList() {
    const url = new URL(location.href); ['edit', 'create', 'notice'].forEach(key => url.searchParams.delete(key));
    const {response,doc}=await html(url.href);if(!response.ok)throw new Error('列表刷新暂不可用');const index=doc.querySelector('.admin-users-index');
    if (index) { required(page,'.admin-users-index').replaceWith(index); document.dispatchEvent(new CustomEvent('chunnel:admin-updated')); }
  }
  async function nativeSubmit(event: SubmitEvent) {
    const form = event.target; if (!(form instanceof HTMLFormElement) || !form.matches('[data-user-native]')) return;
    event.preventDefault(); if (busy || readOnly || !runtime.alive() || operations.size || recovered || pendingInvalid || nativeUncertain) return;
    if (form.getAttribute('id') === 'admin-user-form' && !form.querySelector('[name="role"]:checked')) return feedback('至少选择一个账户角色。', true);
    const data = new URLSearchParams([...new FormData(form)].map(([key,value])=>[key,String(value)]));
    const submitter = event.submitter instanceof HTMLButtonElement ? event.submitter : form.querySelector<HTMLButtonElement>('button[type="submit"][name="action"]');
    if (submitter?.name) data.set(submitter.name, submitter.value);
    const oldBaseline = baselines.get(form), formID = form.getAttribute('id'), creating = data.get('action') === 'create';
    const controls: [Field,boolean][] = formFields(form).map(el=>[el,el.disabled]); controls.forEach(([el])=>{if(el instanceof HTMLButtonElement)el.disabled=true;});const submittedSignature=signature(form),serial=generation;
    let confirmed=false;busy = true; feedback('正在保存…');
    try {
      sessionStorage.setItem(nativeKey(),JSON.stringify({action:data.get('action'),username:data.get('username'),version:data.get('version')}));nativeUncertain=true;
      const {response,doc}=await html(form.getAttribute('action')||'/admin/users/',{method:'POST',body:data.toString(),headers:{'Content-Type':'application/x-www-form-urlencoded',Accept:'text/html'}});const next=doc.querySelector<HTMLDialogElement>('[data-user-dialog]');if(serial!==generation)return;
      if (!next) throw new Error(response.status === 403 ? '会话已失效，请刷新页面后重新登录。' : '未能确认保存结果，请重新载入账户核对。');
      const notice=new URL(response.url).searchParams.get('notice');
      if (response.ok && notice !== (creating?'created':data.get('action')==='password'?'password':'updated')) throw new Error('尚未收到保存确认，请重新载入账户核对。');
      if (creating && response.ok) { confirmed=true;sessionStorage.removeItem(nativeKey());nativeUncertain=false;closeAfterCreate(doc); return; }
      if (next.dataset.userId !== dialog.dataset.userId) { runtime.retire(); return; }
      const replacement = next.querySelector<HTMLFormElement>('#' + formID);
      if (!replacement) throw new Error('未能读取保存后的账户资料。');
      if(response.ok && BigInt(control(replacement,'version').value)!==BigInt(data.get('version')||'0')+1n)throw new Error('账户保存版本未确认，请重新载入核对。');
      if (next.dataset.csrf !== csrf) {
        if (response.ok && data.get('action') === 'password' && dialog.dataset.userId === owner) {
          confirmed = true;
          sessionStorage.removeItem(nativeKey());
          nativeUncertain = false;
          runtime.retire();
          feedback('密码已更新，请重新载入以使用新会话。');
        } else runtime.retire();
        return;
      }
      const later=signature(form)!==submittedSignature?drafts(form):null;form.replaceWith(replacement); baselines.delete(form); track(replacement);if(later)restoreDraft(replacement,later);
      if (!response.ok) { if(response.status<500){sessionStorage.removeItem(nativeKey());nativeUncertain=false;}baselines.set(replacement, oldBaseline || ''); feedback(required(next,'[data-user-feedback]').textContent || '请检查输入后重试。', true); replacement.querySelector<HTMLElement>('.has-error input, .has-error textarea')?.focus(); return; }
      confirmed=true;sessionStorage.removeItem(nativeKey());nativeUncertain=false;
      dialog.dataset.csrf = next.dataset.csrf; dialog.dataset.username = next.dataset.username;
      for (const el of dialog.querySelectorAll<HTMLInputElement>('[name="csrf_token"]')) el.value = next.dataset.csrf || '';
      const picker = document.querySelector<HTMLElement>('[data-editor-media-dialog]'); if (picker) picker.dataset.csrf = next.dataset.csrf;
      if (formID === 'admin-user-form') {
        const password = dialog.querySelector<HTMLFormElement>('#admin-user-password'), incoming = next.querySelector<HTMLFormElement>('#admin-user-password');
        if (password && incoming) {
          const secrets = [...password.querySelectorAll<HTMLInputElement>('[type="password"]')].map(el => el.value);
          password.replaceWith(incoming); baselines.delete(password); track(incoming);
          incoming.querySelectorAll<HTMLInputElement>('[type="password"]').forEach((el, i) => { el.value = secrets[i] || ''; });
        }
        required(dialog,'.admin-user-dialog-heading').replaceWith(required(next,'.admin-user-dialog-heading'));
      } else {
        const version = next.querySelector<HTMLInputElement>('#admin-user-form [name="version"]')?.value;
        if (version) required<HTMLInputElement>(dialog,'#admin-user-form [name="version"]').value = version;
      }
      feedback(later?'本次资料已保存；后续输入仍待保存。':'已保存，账户信息已更新。');try{await refreshList();}catch{feedback('资料已保存；用户列表暂未刷新。');}
    } catch (error) { if(runtime.alive()){feedback(confirmed?'资料已保存；账户显示暂未更新。':failure(error).message+'；请先核对实际账户。', true);if(nativeUncertain)recovery();} }
    finally { busy = false; controls.forEach(([el, disabled]) => { el.disabled = disabled || !runtime.alive(); }); }
  }
  function closeAfterCreate(doc: Document) {
    baselines.clear(); busy = false; close(true);
    const index = doc.querySelector('.admin-users-index'); if (index) required(page,'.admin-users-index').replaceWith(index);
    window.ChunnelAdmin?.showToast('用户已创建，可再次打开编辑会员、积分和其他资料。', 'success');
    refreshList().catch(() => {});
  }
  page.addEventListener('click', event => { if(!(event.target instanceof Element))return;const link = event.target.closest<HTMLAnchorElement>('[data-user-edit], [data-user-create]'); if (!link || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0) return; event.preventDefault(); openEditor(link); });
  window.addEventListener('beforeunload', event => { if (dialog.open && (busy || operations.size || dirty())) { event.preventDefault(); event.returnValue = ''; } });
  page.dataset.userWorkspaceReady = 'true';
  if (dialog.dataset.autoOpen === 'true') mount(dialog);
})();
