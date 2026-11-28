import { orderID, exponent } from '../shared/payment-data';
import { format } from '../shared/currency-amount';
import type { ResourceView, ResourceCondition, ResourceDownload } from './resource-public-data';
  export const esc = (value: unknown) => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c] || c));
  const icons: Record<string, string> = {
    direct: '<path d="M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5"/>',
    baidu: '<ellipse cx="5" cy="8" rx="2" ry="3"/><ellipse cx="10" cy="5" rx="2" ry="3"/><ellipse cx="16" cy="5" rx="2" ry="3"/><ellipse cx="20" cy="10" rx="2" ry="3"/><path d="M6 18c0-4 4-8 7-8s6 4 6 8c0 4-4 1-6 1s-7 3-7-1Z"/>',
    aliyun: '<path d="M9 5C3 4 1 9 5 14l4 5m6-14c6-1 8 4 4 9l-4 5M8 10l4 5 4-5"/>',
    quark: '<circle cx="11" cy="11" r="8"/><path d="m15 15 7 7"/>',
    onedrive: '<path d="M8 9a6 6 0 0 1 11 2 4 4 0 0 1-1 8H6a5 5 0 0 1-1-10Z"/>',
    google: '<path d="m8 3 8 0 8 14-4 5H4l-4-5Zm0 0 8 14H0m24 0H8L16 3"/>',
    dropbox: '<path d="m6 3 6 4-6 4-6-4Zm12 0 6 4-6 4-6-4ZM6 11l6 4-6 4-6-4Zm12 0 6 4-6 4-6-4ZM6 20l6 3 6-3"/>',
    lanzou: '<path d="M6 18h12a4 4 0 0 0 0-8 6 6 0 0 0-11-2 5 5 0 0 0-1 10Z"/>',
  };
  export const logo = (provider: string) => `<span class="npr-provider npr-provider-${esc(provider)}" aria-hidden="true">${icons[provider] ? `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">${icons[provider]}</svg>` : esc(provider === '123pan' ? '123' : '115')}</span>`;
  export const login = () => `/admin/login/?next=${encodeURIComponent(location.pathname + location.hash)}`;
  const orderLink = (condition: ResourceCondition) => orderID(condition.order_id) && ['pending','paid','review'].includes(condition.order_status) ? `<a href="/billing/?order=${encodeURIComponent(condition.order_id)}" target="_blank" rel="noopener">查看原订单 · ${esc(condition.order_amount)} ${esc(condition.order_currency)} →</a>` : '';
  export const date = (value: string, zone: string) => { if (!value) return ''; try { return new Date(value).toLocaleString('zh-CN', {timeZone:zone || undefined}); } catch { return ''; } };
export function renderResource(state: ResourceView, selected: string, previewUnlocked: boolean, keepDraft = false): string {
      const preview = state.preview;
      if (!state.variants.some(v => v.id === selected)) selected = (!preview && state.variants.find(v => v.allowed)?.id) || state.variants[0]?.id || '';
      const v = state.variants.find(v => v.id === selected); if (!v) return ''; 
      const allowed = preview ? previewUnlocked : v.allowed;
      const tabs = state.variants.length > 1 ? `<div class="npr-tabs" role="tablist" aria-label="内容版本">${state.variants.map(item => `<button type="button" role="tab" aria-selected="${item.id===selected}" data-version="${esc(item.id)}">${esc(item.name)}<span>${preview ? '预览版本' : item.allowed ? '已解锁' : '待解锁'}</span></button>`).join('')}</div>` : '';
      const actions = v.groups.map((g, gi) => `<section class="npr-rule"><div class="npr-rule-heading"><b>${esc(g.name || `解锁方式 ${gi+1}`)}</b><span>${g.passed ? '✓ 已满足' : '满足以下全部条件'}</span></div><ul>${g.conditions.map(c => {
        const attr = `data-condition="${esc(c.id)}" data-group="${gi}"`;
        let action = '';
        if (preview && !c.passed) {
          const label = ({points:'积分解锁',payment:'立即支付',follow:'关注作者',like:'点赞文章',membership:'查看会员',comment:'去评论',tip:'去打赏'} as Record<string, string>)[c.kind];
          if (label) action = `<button type="button" disabled>${label}（预览）</button>`;
        } else if (!preview && !c.passed && state.logged_in && !v.blocked) {
          if (c.kind==='points' || c.kind==='payment') action = orderLink(c) || (c.purchasable === true ? `<button type="button" data-buy="${c.kind}" ${attr}>${c.kind==='points' ? '积分解锁' : '立即支付'} →</button>` : `<small>${esc(({prerequisite:'请先完成本组其他条件',limit:'下载次数已用完',already_unlocked:'已有可用的解锁方式'} as Record<string, string>)[c.purchase_reason] || '当前不可购买')}</small>`);
          if (c.kind==='follow' || c.kind==='like') action = `<button type="button" data-engage="${c.kind}">${c.kind==='follow' ? '关注作者' : '点赞文章'}</button>`;
          if (c.kind==='membership') action = '<a href="/billing/?kind=membership">查看会员 →</a>';
          if (c.kind==='comment') action = '<a href="#comments">去评论 →</a>';
          if (c.kind==='tip') action = '<button type="button" data-tip>去打赏 →</button>';
        }
        return `<li class="${c.passed ? 'npr-met' : 'npr-unmet'}"><span>${c.passed?'✓':'○'}</span><span>${esc(c.label)}</span>${action}</li>`;
      }).join('')}</ul></section>`).join('<div class="npr-or"><span>或选择另一种方式</span></div>');
      const questions = v.questions?.length && !v.quiz_passed ? `<fieldset class="npr-questions" ${preview?'disabled':''}><legend>回答问题 · 达到 ${v.pass_percent}% 即可解锁</legend>${v.questions.map(q => `<label>${esc(q.title)}${q.options?.length ? `<select data-answer="${esc(q.id)}" required><option value="">请选择答案</option>${q.options.map(o=>`<option>${esc(o)}</option>`).join('')}</select>` : `<input data-answer="${esc(q.id)}" maxlength="500" required autocomplete="off">`}</label>`).join('')}<small>${preview?'每日最多尝试':'今日还可尝试'} ${v.attempts_remaining} 次</small></fieldset>` : '';
      const fields = v.fields?.length && !v.form_complete ? `<fieldset ${preview?'disabled':''}><legend>填写资料</legend>${v.fields.map(f => `<label>${esc(f.label)}${f.required ? '<span class="npr-required"> *</span>' : ''}${f.type==='textarea' ? `<textarea data-field="${esc(f.id)}" maxlength="4000" ${f.required?'required':''}></textarea>` : f.type==='select' ? `<select data-field="${esc(f.id)}" ${f.required?'required':''}><option value="">请选择</option>${f.options.map(o=>`<option>${esc(o)}</option>`).join('')}</select>` : `<input type="${esc(f.type)}" data-field="${esc(f.id)}" maxlength="4000" ${f.required?'required':''}>`}</label>`).join('')}</fieldset>` : '';
      const submit = !v.blocked && (questions || fields) ? `<form class="npr-form">${questions}${fields}<button class="npr-primary" type="submit" ${preview?'disabled':''}>${preview?'提交核验（预览）':'提交并核验条件 →'}</button></form>` : '';
      const priced = (c: ResourceCondition) => c.kind==='payment' && (preview || (!c.passed && c.purchasable===true && !orderLink(c)));
      const priceGroup=!v.blocked?v.groups.findIndex(g=>g.conditions.some(priced)):-1;
      const price=priceGroup>=0?v.groups[priceGroup]?.conditions.find(priced):null;
      const primary=price?`<div class="npr-pricebar"><div><small>解锁此版本${preview?'（预览）':''}</small><strong><i>${esc(({CNY:'¥',USD:'$',EUR:'€',GBP:'£',JPY:'¥'} as Record<string, string>)[price.currency]||price.currency)}</i>${format(BigInt(price.amount), exponent(price.currency))}</strong></div>${preview?'<button type="button" class="npr-primary" data-preview-view="unlocked">预览解锁效果</button>':state.logged_in?`<button type="button" class="npr-primary" data-buy="payment" data-condition="${esc(price.id)}" data-group="${priceGroup}">立即解锁</button>`:`<a class="npr-primary" href="${login()}">登录后解锁</a>`}</div>`:'';
      const moreConditions=v.groups.length>1||v.groups.some(group=>group.conditions.length>1);
      const conditions=price?`${primary}${moreConditions?`<details class="npr-alternatives"><summary>查看解锁条件与其他方式</summary>${actions}</details>`:''}`:actions;
      const delivery = (link: ResourceDownload) => preview ? `<button type="button" class="npr-primary" disabled>获取下载地址（预览）</button><div class="npr-delivery npr-preview-delivery"><span>下载地址 <b>${esc(link.url)}</b></span>${link.code?`<span>提取码 <b>${esc(link.code)}</b></span>`:''}${link.password?`<span>解压密码 <b>${esc(link.password)}</b></span>`:''}</div>` : `<button type="button" class="npr-primary" data-download="${esc(link.id)}">获取下载地址</button><div class="npr-delivery" data-delivery="${esc(link.id)}"></div>`;
      const content = allowed ? `<div class="npr-unlocked"><p class="npr-success">${preview?'解锁后效果预览 · ':'✓ '}${esc(v.name)}${preview?'':'已解锁'}</p>${v.body || ''}${v.kind==='image' ? `<img class="npr-private-image" src="${esc(v.media_url)}" alt="${esc(v.name)}" loading="lazy">` : ''}${v.kind==='video' ? `<video controls preload="metadata" src="${esc(v.media_url)}" aria-label="${esc(v.name)}"></video>` : ''}${v.downloads.map(link=>`<div class="npr-download">${logo(link.provider)}<span><b>${esc(link.name || link.provider_name)}</b><small>${esc(link.provider_name)}</small></span>${delivery(link)}</div>`).join('')}</div>` : `<p class="npr-intro">${!preview && state.selection==='first' && state.variants.some(x=>x.allowed) ? '此版本未被选中，当前展示首个符合条件的版本。' : '完成任意一组条件，即可解锁这个版本。'}</p>${!preview && !state.logged_in && !price ? `<a class="npr-login" href="${login()}">登录后使用积分、支付与会员权益 →</a>`:''}${conditions}${submit}`;
      const windowCopy = [state.starts_at ? `开放时间：${date(state.starts_at,state.timezone)}` : '', state.ends_at ? `访问截止：${date(state.ends_at,state.timezone)}` : ''].filter(Boolean).join(' · ');
      const recovery = !preview && !v.available && state.logged_in ? v.groups.flatMap(g=>g.conditions.map(orderLink)).filter(Boolean).join('') : '';
      const previewControls = preview ? `<div class="npr-preview-controls" role="group" aria-label="资源预览状态"><span>查看效果</span><button type="button" data-preview-view="locked" aria-pressed="${!previewUnlocked}">解锁前</button><button type="button" data-preview-view="unlocked" aria-pressed="${previewUnlocked}">解锁后</button></div><p class="npr-intro">使用本次草稿快照展示全部版本。${state.selection==='first'?'正式访问仅展示首个符合条件的版本。':'正式访问按各版本条件核验。'}</p>` : '';
      const unavailable = !v.available ? `<p class="npr-unavailable">此资源尚未开放或已结束访问。${preview?'下方仅预览内容效果。':''}</p>` : '';
      return `${previewControls}${tabs}${windowCopy?`<p class="npr-intro">${esc(windowCopy)}</p>`:''}${unavailable}${preview || v.available ? content : recovery}${!preview && v.available && allowed && keepDraft ? submit : ""}<footer class="npr-controls"><span>${preview?'预览额度 · ':''}${state.remaining<0 ? '下载次数不限' : `${preview?'可下载':'剩余下载'} ${state.remaining} 次`}</span>${preview?'':'<button type="button" data-refresh>重新核验</button>'}</footer><p class="npr-status" role="status" aria-live="polite"></p>`;
    }
