(() => {
  'use strict';
  const root=document.querySelector('[data-recognition-admin]'); if(!root)return;
  const status=root.querySelector('[data-recognition-status]'),form=root.querySelector('[data-recognition-settings]'),list=root.querySelector('[data-recognition-designs]'),readOnly=root.dataset.readOnly==='true';
  let data,account,saving=false,lookupSequence=0,editVersion=0,baseline='';
  const signature=()=>JSON.stringify([...form.querySelectorAll('input,select,textarea')].map(e=>[e.value,e.checked]));
  const publishDraft=()=>document.dispatchEvent(new CustomEvent('newpress:feature-status-draft',{detail:{dirty:!!baseline&&signature()!==baseline}}));
  const markEdited=()=>{editVersion++;publishDraft();};
  form.addEventListener('input',markEdited);form.addEventListener('change',markEdited);
  window.addEventListener('beforeunload',e=>{if(baseline&&signature()!==baseline){e.preventDefault();e.returnValue='';}});
  const node=(tag,text,cls)=>{const e=document.createElement(tag);if(text!==undefined)e.textContent=text;if(cls)e.className=cls;return e;};
  const report=(e,text,error=false)=>{e.textContent=text;e.dataset.error=String(error);};
  async function request(path,method='GET',body){const r=await fetch(path,{method,credentials:'same-origin',cache:'no-store',redirect:'error',headers:{Accept:'application/json',...(body?{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf}:{})},...(body?{body:JSON.stringify(body)}:{})});const result=await r.json();if(!r.ok)throw new Error(result?.error?.message||'请求失败，请稍后重试');return result;}
  const kinds={title:'头衔',medal:'勋章',verified:'认证'};
  const icons={'award':'奖章','sparkles':'闪光','star':'星星','heart':'爱心','diamond':'菱形','shield-check':'盾牌','badge-check':'认证','zap':'闪电','pen-line':'创作','newspaper':'文章'};
  function input(label,key,value,type='text',options){const wrap=node('label',label),e=node(options?'select':'input');e.dataset.field=key;if(options){Object.entries(options).forEach(([key,name])=>{const o=node('option',name);o.value=key;e.append(o);});e.value=value;}else{e.type=type;if(type==='checkbox')e.checked=value;else e.value=value ?? '';}
    if(type==='checkbox')wrap.className='recognition-check';if(['key','name','color','background'].includes(key))e.required=true;
    if(key==='key'){e.pattern='[a-z](?:[a-z0-9]|-){0,31}';e.maxLength=32;}
    if(key==='name')e.maxLength=20;if(key==='description')e.maxLength=160;if(key==='image'){e.dataset.mediaInput='';e.dataset.mediaTitle='勋章图片';e.placeholder='/media/…（PNG / WebP）';e.maxLength=500;}
    e.disabled=readOnly;wrap.append(e);return wrap;}
  function valueOf(card){const out={};card.querySelectorAll('[data-field]').forEach(e=>{out[e.dataset.field]=e.type==='checkbox'?e.checked:e.value.trim();});return out;}
  function design(b,open=false){const card=node('details',undefined,'recognition-design');card.open=open;const summary=node('summary'),preview=node('span'),type=node('small');summary.append(preview,type);const body=node('div',undefined,'recognition-design-body');const first=node('div',undefined,'recognition-fields');first.append(input('名称','name',b.name),input('类型','kind',b.kind,'text',kinds),input('标识（英文，授予后不可改变）','key',b.key));body.append(first,input('说明','description',b.description));
    const style=node('div',undefined,'recognition-fields');style.append(input('图标','icon',b.icon,'text',icons),input('文字与强调色','color',b.color,'color'),input('底色','background',b.background,'color'));body.append(style);
    const more=node('div',undefined,'recognition-fields');more.append(input('展示样式','style',b.style,'text',{soft:'柔和底色',outline:'描边',solid:'实色'}),input('自定义勋章图片（媒体库地址）','image',b.image),input('启用展示','enabled',b.enabled,'checkbox'));body.append(more);
    const remove=node('button','移除设计','admin-button admin-button-quiet');remove.type='button';remove.disabled=readOnly;remove.addEventListener('click',()=>{card.remove();markEdited();report(status,'设计已从当前草稿移除，保存后生效。已授予的设计请改用停用。');});body.append(remove);card.append(summary,body);
    const update=()=>{const value=valueOf(card);preview.replaceChildren(window.NewPressRecognition.badge(value,{interactive:false,decorative:value.kind==='medal'}));if(value.kind==='medal')preview.append(node('span',value.name,'identity-medal-name'));type.textContent=kinds[value.kind]+(value.enabled?' · 已启用':' · 已停用');};card.addEventListener('input',update);update();list.append(card);
  }
  function render(){
    const cfg=data.settings;['tips_enabled','hide_amounts','allow_hide_amount','verification_enabled'].forEach(k=>form.elements[k].checked=cfg[k]);form.elements.max_medals.value=cfg.max_medals;form.elements.verification_help.value=cfg.verification_help;
    const currencies=root.querySelector('[data-recognition-currencies]');currencies.replaceChildren();
    const all=[...data.currencies];cfg.currencies.forEach(c=>{if(!all.some(x=>x.key===c.key))all.push({key:c.key,name:c.key,enabled:false,transfers:false});});
    all.forEach(c=>{const chosen=cfg.currencies.find(x=>x.key===c.key),row=node('div',undefined,'recognition-currency-row');row.dataset.currency=c.key;const label=node('label',undefined,'recognition-check'),box=node('input');box.type='checkbox';box.checked=!!chosen;box.dataset.currencyEnabled='';label.append(box,node('span',c.name+' · '+c.key+(!c.enabled||!c.transfers?'（需开启钱包与转账）':'')));const presets=node('label','快捷数值（逗号分隔）'),field=node('input');field.value=(chosen?.presets||['10','30','50']).join(', ');field.dataset.currencyPresets='';field.maxLength=180;presets.append(field);row.append(label,presets);currencies.append(row);});
    list.replaceChildren();cfg.badges.forEach(b=>design(b));form.hidden=false;
    renderPending();
    baseline=signature();publishDraft();
    if(readOnly)root.querySelectorAll('form input,form select,form textarea,form button,[data-recognition-add]').forEach(e=>e.disabled=true);
  }
  function renderPending(){
    const pending=root.querySelector('[data-recognition-pending]');pending.replaceChildren();root.querySelector('[data-recognition-pending-count]').textContent=`${data.pending_total} 个待审核申请`;
    data.pending.forEach(p=>{const b=node('button',p.name||p.username,'admin-button admin-button-secondary');b.type='button';b.addEventListener('click',()=>{root.querySelector('[data-recognition-lookup]').elements.username.value=p.username;lookup(p.username);});pending.append(b);});if(!data.pending.length)pending.append(node('p','当前没有待审核的认证申请。','recognition-help'));
  }
  async function load(){const revision=editVersion;report(status,'正在读取配置…');try{const fresh=await request('/api/v1/admin/recognition');if(revision!==editVersion){report(status,'读取期间有新修改，当前输入已保留。');return;}data=fresh;render();report(status,'配置已载入。');}catch(e){report(status,e.message,true);}}
  root.querySelector('[data-recognition-reload]').addEventListener('click',()=>{if(baseline&&signature()!==baseline&&!window.confirm('重新载入会放弃尚未保存的互动设置，是否继续？'))return;load();});
  root.querySelector('[data-recognition-add]').addEventListener('click',()=>{if(list.children.length>=100){report(status,'最多配置 100 个身份设计',true);return;}design({key:'badge-'+crypto.randomUUID().slice(0,8),kind:'medal',name:'新勋章',description:'记录值得纪念的社区贡献',icon:'award',color:'#2563eb',background:'#eff6ff',style:'soft',image:'',enabled:true},true);markEdited();list.lastElementChild.querySelector('[data-field=name]').focus();});
  form.addEventListener('submit',async e=>{e.preventDefault();if(readOnly||saving||!data)return;saving=true;const savedSignature=signature();const button=form.querySelector('[type=submit]');button.disabled=true;form.setAttribute('aria-busy','true');try{
    const cfg={...data.settings};['tips_enabled','hide_amounts','allow_hide_amount','verification_enabled'].forEach(k=>cfg[k]=form.elements[k].checked);cfg.max_medals=Number(form.elements.max_medals.value);cfg.verification_help=form.elements.verification_help.value.trim();cfg.badges=[...list.children].map(valueOf);cfg.currencies=[...root.querySelectorAll('[data-currency]')].filter(row=>row.querySelector('[data-currency-enabled]').checked).map(row=>({key:row.dataset.currency,presets:row.querySelector('[data-currency-presets]').value.split(/[,，]/).map(x=>x.trim()).filter(Boolean)}));
    const receipt=await request('/api/v1/admin/recognition','PUT',cfg);if(receipt.saved!==true)throw new Error('保存结果未确认，请先重新检测');data.settings={...cfg,version:cfg.version+1};baseline=savedSignature;publishDraft();document.dispatchEvent(new Event('newpress:feature-status-refresh'));report(status,signature()===baseline?'互动与身份设置已保存。':'已保存提交时的设置，保存期间的新修改仍保留，请继续保存。');
  }catch(e){report(status,e.message+'，当前编辑内容已保留。',true);}finally{saving=false;button.disabled=false;form.setAttribute('aria-busy','false');}});
  async function lookup(username){const seq=++lookupSequence;const state=root.querySelector('[data-recognition-account-status]');report(state,'正在查询成员…');account=null;root.querySelector('[data-recognition-account]').hidden=true;try{
    const result=await request('/api/v1/admin/recognition/account?username='+encodeURIComponent(username));if(seq!==lookupSequence)return;account=result;
    const summary=root.querySelector('[data-recognition-member-summary]');summary.replaceChildren(node('h3',account.name+' · @'+account.username));const badges=node('div',undefined,'identity-badges');account.badges.forEach(b=>badges.append(window.NewPressRecognition.badge(b)));summary.append(badges);
    const p=account.identity.application;report(root.querySelector('[data-recognition-evidence]'),p?`认证状态：${({pending:'待审核',approved:'已通过',rejected:'已驳回',revoked:'已撤销'})[p.status]}\n公开说明：${p.label}\n申请说明（仅本人和管理员可见）：${p.evidence}${p.reason?'\n审核原因：'+p.reason:''}`:'');
    const manage=root.querySelector('[data-recognition-manage]');manage.elements.key.replaceChildren(...data.settings.badges.map(b=>{const o=node('option',kinds[b.kind]+' · '+b.name);o.value=b.key;return o;}));if(p?.status==='pending'){manage.elements.key.value=p.key;manage.elements.action.value='approve';}else manage.elements.action.value='grant';manage.elements.reason.value='';root.querySelector('[data-recognition-account]').hidden=false;report(state,'');
  }catch(e){if(seq===lookupSequence)report(state,e.message,true);}}
  root.querySelector('[data-recognition-lookup]').addEventListener('submit',e=>{e.preventDefault();lookup(e.target.elements.username.value.trim());});
  root.querySelector('[data-recognition-manage]').addEventListener('submit',async e=>{e.preventDefault();if(readOnly||!account)return;const f=e.target,b=f.querySelector('button[type=submit]');if(b.disabled)return;b.disabled=true;f.setAttribute('aria-busy','true');const state=root.querySelector('[data-recognition-account-status]');try{const original=account;await request('/api/v1/admin/recognition/account','POST',{user_id:original.user_id,version:original.identity.version,action:f.elements.action.value,key:f.elements.key.value,label:f.elements.label.value.trim(),reason:f.elements.reason.value.trim(),days:Number(f.elements.days.value)});await lookup(original.username);report(state,'成员身份已更新。');const fresh=await request('/api/v1/admin/recognition');data.pending=fresh.pending;data.pending_total=fresh.pending_total;renderPending();}catch(e){report(state,e.message,true);}finally{b.disabled=false;f.setAttribute('aria-busy','false');}});
  load();
})();
