import type {} from '../shared/account-types';
import { create } from '../shared/newpress-client';
import { failure, record, nonce } from '../shared/ledger-data';
import { paymentAdminConfig, paymentSettings, adminPaymentOrders, paymentOrder, emptyProviderConfig, configFields, secretFields, orderID } from '../shared/payment-data';
import type { PaymentAdminConfig, PaymentSettings, PaymentChannel, PaymentProduct, PaymentExchange, AdminPaymentOrder, ConfigField, SecretField } from '../shared/payment-data';
import { required, control } from './ledger-admin-dom';
import type { Control } from './ledger-admin-dom';
type Group = 'channels' | 'products' | 'exchanges';
interface FieldOptions { wide?: boolean; hint?: string; choices?: string[][]; refresh?: boolean; area?: boolean; rows?: number; max?: number; secret?: boolean; type?: string; amount?: boolean; required?: boolean; min?: number; maxNumber?: number; placeholder?: string }
const groupKey = (value: unknown): value is Group => value === 'channels' || value === 'products' || value === 'exchanges';
(() => {
  'use strict';
  const host = document.querySelector<HTMLElement>('[data-payments-admin]'), ui = window.ChunnelAdminUI;
  if (!host || !ui) return;
  const root = host, adminUI = ui, owner = root.dataset.userId || '', csrf = root.dataset.csrf || '';
  const $ = <T extends HTMLElement = HTMLElement>(selector: string): T => required<T>(root, selector);
  const form = $<HTMLFormElement>('[data-payments-form]');
  const readOnly = root.dataset.readOnly === 'true';
  // Enhance the existing page without replacing forms or losing their controls.
  const tabs = $('.payments-tabs');
  const layout = document.createElement('div');
  layout.className = 'payments-layout admin-settings-layout';
  const outline = document.createElement('aside');
  outline.className = 'admin-settings-nav';
  const main = document.createElement('div');
  main.className = 'payments-main';
  tabs.before(layout);
  for (const link of tabs.querySelectorAll('a')) {
    const button = document.createElement('button');
    button.type = 'button';
    button.dataset.settingsTarget = link.hash.slice(1);
    button.setAttribute('aria-controls', button.dataset.settingsTarget);
    const title = document.createElement('span');
    title.className = 'admin-settings-nav-title';
    const text = document.createElement('b');
    text.textContent = link.textContent;
    title.append(text); button.append(title); link.replaceWith(button);
  }
  tabs.classList.remove('payments-tabs');
  outline.append(tabs); layout.append(outline, main);
  const bar = $('.payments-savebar');
  bar.className = 'admin-savebar admin-save-dock';
  bar.setAttribute('data-admin-savebar', '');
  form.id = 'payments-settings'; form.noValidate = true;
  bar.dataset.formId = form.id;
  const save = $('[data-payments-save]');
  save.setAttribute('data-admin-save', ''); save.setAttribute('form', form.id);
  const version = $('[data-payments-version]');
  version.hidden = true; outline.append(version);
  bar.innerHTML = '<p class="admin-save-feedback" data-save-state role="status" aria-live="polite"></p><div class="admin-savebar-actions"><button class="admin-button admin-button-quiet" type="button" data-admin-discard hidden>放弃修改</button></div>';
  required(bar, '.admin-savebar-actions').append(save);
  main.append(form, $('#payments-orders'), bar);
  root.querySelectorAll('.payments-section').forEach(section => section.setAttribute('data-settings-section', ''));
  const navigation = adminUI.settingsNavigation(document);
  const observer = window.ResizeObserver ? new ResizeObserver(() => {
    document.querySelector<HTMLElement>('.admin-main')?.style.setProperty('--admin-ui-dock-space', `${Math.ceil(bar.getBoundingClientRect().height)}px`);
  }) : null; observer?.observe(bar);
  const esc = (value: unknown) => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c] || c));
  const labels: Record<ConfigField, string> = {endpoint:'网关地址（可选覆盖）',merchant_id:'商户号 / PID',app_id:'应用 ID / Client ID',api_key:'API 密钥',secret:'签名 / 回调密钥',private_key:'商户私钥（PEM）',public_key:'平台公钥 / 平台证书（PEM）',certificate_serial:'商户证书序列号',platform_serial:'平台公钥 ID / 证书序列号',webhook_id:'Webhook ID',payment_type:'支付方式'};
  const statusLabels: Record<string, string> = {pending:'待付款',paid:'已付款 · 待入账',completed:'已完成',cancelled:'已取消',review:'需要核对',refunded:'已退款'};
  const state: Omit<PaymentAdminConfig, 'settings'> & { settings: PaymentSettings | null; dirty: boolean; busy: boolean } = {settings:null,providers:[],currencies:[],plans:[],public_url:'',dirty:false,busy:false};
  let baseline: PaymentSettings | null = null, baselineFields = '', feedback = '', feedbackError = false, retired = false;
  let orderSequence = 0, orderController: AbortController | undefined, query = '';
  let orderRows: AdminPaymentOrder[] = [], orderRevision = 0; const syncs = new Set<string>();
  const confirmedOrders = new Map<string, { revision: number; order: AdminPaymentOrder }>();
  const rawFields = () => [...form.querySelectorAll<Control>('input,select,textarea')].map(field => {
    const editor = field.closest<HTMLElement>('.payments-editor'); return { key: (editor ? editor.dataset.group + ':' + editor.dataset.index : 'general') + ':' + field.name, value: field instanceof HTMLInputElement && field.type === 'checkbox' ? field.checked : field.value, field };
  });
  const fieldSignature = () => JSON.stringify(rawFields().map(({key, value}) => [key, value]));
  function controls() {
    form.setAttribute('aria-busy', String(state.busy));
    adminUI.paintSavebar(bar, {dirty:state.dirty,busy:state.busy,readOnly:readOnly||retired,disabled:!baseline,message:feedback,error:feedbackError});
  }
  function acceptSettings() {
    baseline = structuredClone(state.settings); state.dirty = false;
    render(); baselineFields = fieldSignature(); controls();
  }
  const precision = (key: string) => state.currencies.find(c => c.key === key)?.precision ?? 0;
  const moneyPrecision = (currency: string) => currency === 'JPY' ? 0 : 2;
  function display(units: string | number, places: number) { try { const n=BigInt(units || '0');const scale=10n**BigInt(places);return places ? `${n/scale}.${String(n%scale).padStart(places,'0')}` : String(n); } catch { return ''; } }
  function units(value: string, places: number) { const v=String(value).trim();if(!/^\d+(\.\d+)?$/.test(v))throw new Error('金额请输入非负十进制数字。');const [a,raw='']=v.split('.');const b=raw.replace(/0+$/,'');if(b.length>places)throw new Error(`此币种最多支持 ${places} 位小数。`);const n=BigInt(a || '0')*10n**BigInt(places)+BigInt((b+'0'.repeat(places)).slice(0,places)||'0');if(n>10000000000000n)throw new Error('金额超出允许范围。');return String(n); }
  function message(text: string,error=false) { const el=$('[data-payments-status]');el.textContent=text;el.dataset.error=String(error); }
  function dirty() { if (!baseline || retired || readOnly) return; state.dirty=fieldSignature()!==baselineFields;feedback='';feedbackError=false;controls(); }
  function field(name: string,label: string,value: string | number,options: FieldOptions={}) {
    const wide=options.wide?' payments-wide':'';const hint=options.hint?`<small>${esc(options.hint)}</small>`:'';
    let input;
    if(options.choices){const choices=[...options.choices];if(value && !choices.some(x=>String(x[0])===String(value)))choices.push([String(value),`${value}（未启用或已移除）`]);input=`<select name="${esc(name)}" ${options.refresh?'data-refresh="true"':''}>${choices.map(([key,title])=>`<option value="${esc(key)}" ${String(key)===String(value)?'selected':''}>${esc(title)}</option>`).join('')}</select>`;}
    else if(options.area)input=`<textarea name="${esc(name)}" rows="${options.rows||3}" maxlength="${options.max||16000}" spellcheck="false">${esc(value)}</textarea>`;
    else input=`<input name="${esc(name)}" type="${options.secret?'password':options.type||'text'}" value="${esc(value)}" maxlength="${options.max||200}" ${options.amount?'inputmode="decimal"':''} ${options.required?'required':''} ${options.min!==undefined?`min="${options.min}"`:''} ${options.maxNumber?`max="${options.maxNumber}"`:''} ${options.placeholder?`placeholder="${esc(options.placeholder)}"`:''} ${options.secret?'autocomplete="new-password"':''}>`;
    return `<label class="payments-field${wide}"><span>${esc(label)}</span>${input}${hint}</label>`;
  }
  const check=(name: string,label: string,value: boolean)=>`<label class="admin-switch admin-switch-field"><input type="checkbox" role="switch" name="${esc(name)}" ${value?'checked':''}><span class="admin-switch-track" aria-hidden="true"><i></i></span><span class="admin-switch-copy"><b>${esc(label)}</b></span></label>`;
  const empty=(text: string)=>`<p class="payments-empty">${esc(text)}</p>`;
  const coins=()=>state.currencies.map(c=>[c.key,`${c.name} · ${c.symbol}${c.enabled?'':'（停用）'}`]);
  const fiat=()=>['CNY','USD','EUR','GBP','HKD','JPY','LDC'].map(c=>[c,c]);
  function editor(group: Group,i: number,title: string,enabled: boolean,body: string,open: boolean) {return `<details class="payments-editor" data-group="${group}" data-index="${i}" ${open?'open':''}><summary><strong>${esc(title)}</strong><span class="payments-state" data-enabled="${enabled}">${enabled?'启用':'停用'}</span></summary><div class="payments-editor-body">${body}<footer>${check('enabled','启用此项',enabled)}<button type="button" class="payments-remove" data-remove="${group}" data-index="${i}">移除</button></footer></div></details>`;}
  function render(openNew?: string) {
    const s=state.settings; if (!s) return;
    const opened=new Set([...root.querySelectorAll<HTMLElement>('.payments-editor[open]')].map(el=>`${el.dataset.group}:${el.dataset.index}`));
    if(openNew)opened.add(openNew);
    $('[data-payment-general]').innerHTML=`<div class="payments-grid">${check('enabled','开放充值、会员购买与兑换',s.enabled)}${check('require_verified_email','购买前要求验证邮箱',s.require_verified_email)}${field('order_minutes','付款有效时间（分钟）',s.order_minutes,{type:'number',min:5,maxNumber:1440,required:true})}<p class="payments-help">关闭入口后仍接收已有订单的支付通知。会员权益与站内余额保持可查询。</p>${field('disclaimer','支付须知与免责声明',s.disclaimer,{area:true,wide:true,rows:4,max:12000})}${field('refund_policy','退款规则与联系方式',s.refund_policy,{area:true,wide:true,rows:3,max:8000})}</div>`;
    $('[data-payments-enabled-label]').textContent=s.enabled?'入口已开启':'入口已关闭';
    $('[data-payments-enabled-label]').dataset.enabled=String(s.enabled);
    $('[data-payment-channels]').innerHTML=s.channels.length?s.channels.map((c,i)=>{
      const def=state.providers.find(p=>p.key===c.config.provider)||state.providers[0]; if (!def) throw new Error('渠道协议未确认');
      let body=`<div class="payments-grid">${field('key','渠道标识',c.key,{required:true,max:32,hint:'小写字母、数字与短横线；使用后请保持稳定。'})}${field('name','显示名称',c.name,{required:true,max:60})}${field('provider','支付协议',c.config.provider,{choices:state.providers.map(p=>[p.key,p.name]),refresh:true})}${field('currency','结算币种',c.currency,{choices:def.currencies.map(v=>[v,v])})}`;
      const fields=[...new Set<ConfigField>(['endpoint',...def.fields])];
      for(const key of fields){const secret=['api_key','secret','private_key'].includes(key);const saved=c.saved_secrets.includes(key as SecretField);let hint=saved?'已保存；留空保留现有密钥。':'';if(key==='endpoint')hint='使用 HTTPS；留空采用协议默认地址。易支付需填写网关根地址。';if(key==='payment_type')hint='通常为 alipay 或 wxpay，依网关协议填写。';body+=field(`config.${key}`,labels[key],c.config[key],{secret,area:key==='private_key'||key==='public_key',wide:key==='private_key'||key==='public_key'||key==='endpoint',hint,max:16000,placeholder:saved?'已保存，留空保留':''});if(saved)body+=check(`clear.${key}`,`清除已保存的${labels[key]}`,c.clear_secrets.includes(key as SecretField));}
      body+=`${['wechat','linuxdo','epay'].includes(c.config.provider)?'<p class="payments-help">此协议未提供沙箱切换，启用后连接所配置的收款环境。</p>':check('config.sandbox','使用渠道沙箱环境',c.config.sandbox)}</div><p class="payments-help">${esc(def.note)}</p><div class="payments-callback">固定回调地址（在支付平台配置）<code>${esc(state.public_url.replace(/\/$/,'')+'/api/v1/payments/webhook/'+c.key)}</code>订单专用通知地址会随创建支付请求自动提交。测试环境不会替代正式商户验收。</div>`;
      return editor('channels',i,c.name||'新支付渠道',c.enabled,body,opened.has(`channels:${i}`));
    }).join(''):empty('尚未添加渠道。添加后填写商户配置，再启用对应渠道。');
    $('[data-payment-products]').innerHTML=s.products.length?s.products.map((p,i)=>{
      let body=`<div class="payments-grid">${field('key','商品标识',p.key,{required:true,max:32})}${field('title','商品名称',p.title,{required:true,max:80})}${field('kind','商品类型',p.kind,{choices:[['recharge','钱包充值'],['membership','会员开通 / 续期']],refresh:true})}${field('currency','售价币种',p.currency,{choices:fiat(),refresh:true})}${field('price','售价',display(p.price,moneyPrecision(p.currency)),{amount:true,required:true})}`;
      if(p.kind==='membership')body+=field('plan','会员方案',p.plan,{choices:state.plans.map(v=>[v.key,`${v.name} · ${v.days} 天${v.enabled?'':'（停用）'}`]),hint:'会员天数与权益在“成长与会员”中配置。'});
      else body+=`${field('target_currency','到账钱包',p.target_currency,{choices:coins(),refresh:true})}${field('credit','到账数量',display(p.credit,precision(p.target_currency)),{amount:true,required:true})}`;
      return editor('products',i,p.title||'新商品',p.enabled,body+'</div>',opened.has(`products:${i}`));
    }).join(''):empty('添加充值或会员商品，并为售价币种配置可用支付渠道。');
    $('[data-payment-exchanges]').innerHTML=s.exchanges.length?s.exchanges.map((x,i)=>{
      const body=`<div class="payments-grid">${field('key','规则标识',x.key,{required:true,max:32})}${field('title','显示名称',x.title,{required:true,max:80})}${field('from','扣除货币',x.from,{choices:coins(),refresh:true})}${field('to','到账货币',x.to,{choices:coins(),refresh:true})}${field('from_units','基准扣除数量',display(x.from_units,precision(x.from)),{amount:true,required:true})}${field('to_units','基准到账数量（扣费前）',display(x.to_units,precision(x.to)),{amount:true,required:true})}${field('fee_bps','手续费（%）',display(x.fee_bps,2),{amount:true,hint:'允许 0–50%，从到账数量扣除。'})}${field('minimum','单笔最少扣除',display(x.minimum,precision(x.from)),{amount:true,required:true})}${field('maximum','单笔最多扣除',display(x.maximum,precision(x.from)),{amount:true,required:true})}</div>`;
      return editor('exchanges',i,x.title||'新兑换规则',x.enabled,body,opened.has(`exchanges:${i}`));
    }).join(''):empty('每条规则只适用于一个兑换方向；反向兑换请另建规则。');
    form.hidden=false;version.textContent=`配置版本 ${s.version}`;
    navigation.refresh(); controls();
  }
  function collect(): PaymentSettings {
    if (!state.settings) throw new Error('配置尚未载入');
    const s = structuredClone(state.settings), g = $('[data-payment-general]');
    const val = (el: ParentNode, name: string) => el.querySelector<Control>('[name="' + name + '"]')?.value || '';
    const yes = (el: ParentNode, name: string) => el.querySelector<HTMLInputElement>('[name="' + name + '"]')?.checked === true;
    s.enabled = yes(g, 'enabled'); s.require_verified_email = yes(g, 'require_verified_email'); s.order_minutes = Number(val(g, 'order_minutes')); s.disclaimer = val(g, 'disclaimer'); s.refund_policy = val(g, 'refund_policy');
    for (const el of root.querySelectorAll<HTMLElement>('.payments-editor')) {
      const group = el.dataset.group, index = Number(el.dataset.index); if (!groupKey(group) || !Number.isInteger(index) || index < 0) throw new Error('配置分组未确认');
      const item = s[group][index]; if (!item) throw new Error('配置项目未确认'); item.key = val(el, 'key').trim(); item.enabled = yes(el, 'enabled');
      if (group === 'channels') {
        const item = s.channels[index]; if (!item) continue;
        item.name = val(el, 'name').trim(); const provider = val(el, 'provider');
        if (provider !== item.config.provider) { const def = state.providers.find(p => p.key === provider); if (!def?.currencies[0]) throw new Error('渠道协议未确认'); item.config = emptyProviderConfig(provider); item.saved_secrets = []; item.clear_secrets = []; item.enabled = false; item.currency = def.currencies[0]; }
        else { item.currency = val(el, 'currency'); for (const key of configFields) { const field = el.querySelector<Control>('[name="config.' + key + '"]'); if (field) item.config[key] = field.value.trim(); } item.config.sandbox = !['wechat', 'linuxdo', 'epay'].includes(provider) && yes(el, 'config.sandbox'); item.clear_secrets = secretFields.filter(key => yes(el, 'clear.' + key)); }
      } else if (group === 'products') {
        const item = s.products[index]; if (!item) continue;
        item.title = val(el, 'title').trim(); item.price = units(val(el, 'price'), moneyPrecision(val(el, 'currency')));
        if (item.kind === 'recharge') { item.credit = units(val(el, 'credit'), precision(val(el, 'target_currency'))); item.target_currency = val(el, 'target_currency'); } else item.plan = val(el, 'plan');
        const kind = val(el, 'kind'); if (kind !== 'recharge' && kind !== 'membership') throw new Error('商品类型未确认');
        if (kind !== item.kind) { item.kind = kind; item.enabled = false; if (kind === 'membership') { item.plan = state.plans[0]?.key || ''; item.target_currency = ''; item.credit = '0'; } else { item.plan = ''; item.target_currency = state.currencies[0]?.key || ''; item.credit = String(10n ** BigInt(precision(item.target_currency))); } }
        item.currency = val(el, 'currency');
      } else {
        const item = s.exchanges[index]; if (!item) continue;
        item.title = val(el, 'title').trim(); item.from_units = units(val(el, 'from_units'), precision(val(el, 'from'))); item.to_units = units(val(el, 'to_units'), precision(val(el, 'to'))); item.minimum = units(val(el, 'minimum'), precision(val(el, 'from'))); item.maximum = units(val(el, 'maximum'), precision(val(el, 'from'))); item.fee_bps = Number(units(val(el, 'fee_bps'), 2)); if (item.fee_bps > 5000) throw new Error('手续费不能超过 50%。'); item.from = val(el, 'from'); item.to = val(el, 'to');
      }
    }
    return s;
  }
  function retire() {
    if (retired) return; retired = true; orderSequence++; client.cancel(); observer?.disconnect();
    state.settings = baseline = null; state.dirty = false; orderRows = []; confirmedOrders.clear(); form.hidden = true; $('[data-payment-orders]').replaceChildren();
    for (const selector of ['general', 'channels', 'products', 'exchanges']) $('[data-payment-' + selector + ']').replaceChildren();
    feedback = ''; message('页面或登录状态已变化，请重新打开支付管理。', true); controls();
  }
  const client = create({ session: () => ({ authenticated: !retired, csrf_token: csrf }), onUnauthorized: retire });
  async function api(path: string, method = 'GET', data?: unknown, signal?: AbortSignal) {
    if (retired || !root.isConnected) throw new DOMException('页面已变化', 'AbortError');
    try { const result = await client.request('/api/v1/admin/payments' + path, { method, body: data, signal }); if (retired || !root.isConnected) throw new DOMException('页面已变化', 'AbortError'); return result; }
    catch (error) { const problem = failure(error); if ([401, 403].includes(problem.status || 0) && problem.code?.startsWith('auth.')) retire(); throw error; }
  }
  function setBusy(busy: boolean) { state.busy = busy; $<HTMLFieldSetElement>('[data-payments-fields]').disabled = busy || readOnly || retired; controls(); }
  async function load() { if (state.busy || retired) return; setBusy(true); try { Object.assign(state, paymentAdminConfig(await api(''))); if (retired) return; acceptSettings(); message(''); } catch (error) { if (!retired) message(failure(error).message, true); } finally { setBusy(false); } }
  function renderOrders() {
    const target = $('[data-payment-orders]'); target.innerHTML = orderRows.length ? '<div class="payments-order-list">' + orderRows.map(o => '<article class="payments-order"><div><strong>' + esc(o.title) + '</strong><code>' + esc(o.id) + '</code><p>' + esc(o.channel || '站内兑换') + ' · 交易 ' + esc(o.transaction_id || '尚未确认') + '</p><p>到账流水 ' + esc(o.credit_entry || '—') + ' · 会员流水 ' + esc(o.member_entry || '—') + ' · 扣款流水 ' + esc(o.source_entry || '—') + '</p><p>账号 ' + esc(o.user_id) + ' · ' + esc(window.ChunnelTime?.formatDateTime(o.created_at) || new Date(o.created_at).toLocaleString('zh-CN')) + '</p><span class="payments-state">' + esc(statusLabels[o.status]) + '</span></div><div class="payments-order-side"><b>' + esc(o.amount) + ' ' + esc(o.currency) + '</b><button type="button" class="admin-button admin-button-secondary" data-sync-order="' + esc(o.id) + '" ' + (readOnly || retired || syncs.has(o.id) ? 'disabled' : '') + '>同步状态</button></div>' + (o.error_code ? '<p class="payments-order-note">' + esc(o.error_code === 'refund_requires_reconciliation' ? '渠道报告退款，请核对钱包流水、会员权益和渠道退款记录。' : o.error_code === 'fulfillment_pending' ? '渠道已确认付款，权益尚未入账。修复账号、钱包上限或会员冲突后同步重试。' : '支付请求结果待确认，请同步订单核对。') + '</p>' : '') + '</article>').join('') + '</div>' : empty('暂无订单。启用渠道和商品后，用户可在收银台发起购买。');
  }
  async function loadOrders(id = query): Promise<boolean> { if (retired) return false; query = id; const seq = ++orderSequence, revision = orderRevision; orderController?.abort(); const controller = new AbortController(); orderController = controller;
    try { const data = await api('/orders' + (id ? '/' + encodeURIComponent(id) : ''), 'GET', undefined, controller.signal); if (seq !== orderSequence || retired) return false;
      orderRows = adminPaymentOrders(data, id).map(order => { const confirmed = confirmedOrders.get(order.id); return confirmed && confirmed.revision > revision ? confirmed.order : order; }); renderOrders(); return true;
    }
    catch (error) { if (seq === orderSequence && !retired) { if (orderRows.length) renderOrders(); else $('[data-payment-orders]').textContent = failure(error).message; } return false; }
  }
  $('[data-payments-order-lookup]').addEventListener('submit',event=>{event.preventDefault();const id=control($<HTMLFormElement>('[data-payments-order-lookup]'),'order').value.trim();if(id&&!/^np_[0-9a-f]{28}$/.test(id)){message('请输入完整订单号。',true);return;}loadOrders(id);});
  form.addEventListener('input',dirty);
  form.addEventListener('change',event=>{dirty();if(!state.busy && event.target instanceof Element && event.target.matches('[data-refresh]')){try{state.settings=collect();render();dirty();}catch(caught){const e=failure(caught);if(retired)return;message(e.message,true);}}});
  $('[data-admin-discard]').addEventListener('click',()=>{if(!baseline||readOnly||state.busy||retired)return;state.settings=structuredClone(baseline);state.dirty=false;feedback='';feedbackError=false;render();message('');});
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(readOnly||state.busy||retired||!state.dirty)return;
    // Reveal the first invalid control, including a collapsed editor in another group.
    const invalid=form.querySelector<Control>('input:invalid,select:invalid,textarea:invalid');
    if(invalid){const section=invalid.closest('[data-settings-section]'); if(section)navigation.activate(section.id,true);const editor=invalid.closest('details');if(editor)editor.open=true;invalid.reportValidity();return;}
    let next: PaymentSettings;try{next=paymentSettings(collect(), undefined, false);}catch(caught){const e=failure(caught);if(retired)return;message(e.message,true);feedback=e.message;feedbackError=true;controls();return;}
    const submitted = new Map(rawFields().map(({key,value}) => [key,value]));
    feedback='';feedbackError=false;setBusy(true);
    try {
      const result = await api('', 'PUT', next), confirmed = paymentSettings(result.settings, next.version); if (retired) return;
      const newer = rawFields().filter(({key,value}) => submitted.get(key) !== value);
      state.settings = confirmed; acceptSettings();
      for (const current of rawFields()) { const draft = newer.find(v => v.key === current.key); if (!draft) continue; if (current.field instanceof HTMLInputElement && typeof draft.value === 'boolean') current.field.checked = draft.value; else current.field.value = String(draft.value); }
      state.dirty = fieldSignature() !== baselineFields; feedback = state.dirty ? '已保存提交时的配置，新的修改尚未保存' : '已保存'; message('');
    }
    catch(caught){const e=failure(caught);if(retired)return;message(e.message,true);feedback=e.message;feedbackError=true;}
    finally{setBusy(false);}
  });
  root.addEventListener('click',async event=>{
    if (!(event.target instanceof Element) || retired) return;
    const add=event.target.closest<HTMLElement>('[data-payments-add]');const remove=event.target.closest<HTMLElement>('[data-remove]');const sync=event.target.closest<HTMLButtonElement>('[data-sync-order]');
    if(add||remove){if(readOnly||state.busy)return;try{state.settings=collect();const group=add?.dataset.paymentsAdd||remove?.dataset.remove; if(!groupKey(group))return;if(remove){state.settings[group].splice(Number(remove.dataset.index),1);render();dirty();return;}if(state.settings[group].length >= (group==='channels'?40:100))throw new Error('已达到当前分组数量上限。'); const id=nonce().slice(0,8);let value: PaymentChannel | PaymentProduct | PaymentExchange;
      if(group==='channels')value={key:`channel-${id}`,name:'新支付渠道',enabled:false,currency:'CNY',config:{...emptyProviderConfig('epay'),payment_type:'alipay'},saved_secrets:[],clear_secrets:[]};
      else if(group==='products'){const coin=state.currencies[0]?.key||'points';value={key:`product-${id}`,title:'新充值商品',enabled:false,kind:'recharge',currency:'CNY',price:'1000',target_currency:coin,credit:String(100n*10n**BigInt(precision(coin))),plan:''};}
      else{if(state.currencies.length<2)throw new Error('请先在“虚拟币与奖励”中创建第二种货币。');const from=state.currencies[0]?.key||'points',to=state.currencies[1]?.key||'';value={key:`exchange-${id}`,title:'新兑换规则',enabled:false,from,to,from_units:String(10n**BigInt(precision(from))),to_units:String(10n**BigInt(precision(to))),minimum:String(10n**BigInt(precision(from))),maximum:String(1000n*10n**BigInt(precision(from))),fee_bps:0};}
      if (group === 'channels' && 'config' in value) state.settings.channels.push(value); else if (group === 'products' && 'kind' in value) state.settings.products.push(value); else if (group === 'exchanges' && 'from' in value) state.settings.exchanges.push(value);render(`${group}:${state.settings[group].length-1}`);dirty();
    }catch(caught){const e=failure(caught);if(retired)return;message(e.message,true);}return;}
    if(sync&&!readOnly) {
      const id = sync.dataset.syncOrder, original = orderRows.find(o => o.id === id); if (!orderID(id) || !original || syncs.has(id)) return;
      const originalQuery = query; syncs.add(id); renderOrders();
      try { const receipt = paymentOrder(await api('/orders/' + encodeURIComponent(id) + '/sync', 'POST', {}), original.user_id, id, true); if (retired) return;
        if (receipt.kind !== original.kind || receipt.amount !== original.amount || receipt.currency !== original.currency) throw new Error('同步回执与原订单不一致');
        const confirmed = { ...original, ...receipt }; confirmedOrders.set(id, { revision: ++orderRevision, order: confirmed });
        orderRows = orderRows.map(o => o.id === id ? confirmed : o); renderOrders();
        const copy = '订单状态已同步：' + statusLabels[receipt.status] + '。'; message(copy);
        if (query === originalQuery && !await loadOrders() && !retired && query === originalQuery) message(copy + ' 订单列表刷新未完成，请稍后更新。');
      } catch(error) { if (!retired) message(failure(error).message, true); } finally { syncs.delete(id); if (!retired) renderOrders(); }
    }
    if(event.target.closest('[data-payments-orders-refresh]'))await loadOrders();
  });
  document.addEventListener('chunnel:session', event => { if (event.detail?.authenticated !== true || String(event.detail.id || '') !== owner || event.detail.csrf_token !== csrf) retire(); });
  window.addEventListener('pagehide', retire); window.addEventListener('pageshow', event => { if (event.persisted) retire(); });
  window.addEventListener('beforeunload',event=>{if(state.dirty){event.preventDefault();event.returnValue='';}});
  load();loadOrders();
})();
