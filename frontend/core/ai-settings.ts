import { parseModels, isImageProtocol, aiSettings, savedAISettings, mergeAISettings, discoveredModels, telemetryDefaults, defaultBalance, protocolIDs } from '../shared/ai-settings-data';
import type { AISettings, AIProvider, BalanceConfig, Protocol } from '../shared/ai-settings-data';
import { choice, balanceResult } from '../shared/ai-usage-data';
import type { ModelPrice, TelemetryConfig } from '../shared/ai-usage-data';
import { required, control as namedControl } from './ledger-admin-dom';
import { failure } from '../shared/ledger-data';
import { adminAccountRuntime } from './admin-account-runtime';
type ConfigFields = Record<'status'|'list'|'empty',HTMLElement> & Record<'save'|'add'|'reload',HTMLButtonElement> & Record<'default'|'image-default'|'ops-retention',HTMLSelectElement> & Record<'ops-enabled'|'ops-details'|'ops-costs'|'ops-balances'|'ops-charts'|'ops-records',HTMLInputElement> & {form:HTMLFormElement;fields:HTMLFieldSetElement;template:HTMLTemplateElement};
type ProviderFields = Record<'name'|'base'|'key'|'api-version'|'allow-insecure'|'balance-enabled'|'balance-path'|'balance-cache'|'balance-token'|'balance-clear'|'balance-user'|'balance-remaining'|'balance-used'|'balance-total'|'balance-divisor',HTMLInputElement> & Record<'protocol'|'default'|'balance-format'|'balance-currency'|'balance-auth',HTMLSelectElement> & Record<'probe'|'remove'|'balance-probe',HTMLButtonElement> & Record<'title'|'key-state'|'status'|'protocol-help'|'api-version-field'|'pricing-list'|'balance-status'|'balance-key-state'|'discovered'|'options',HTMLElement> & {models:HTMLTextAreaElement};
const priceNumbers=['input','output','cache_read','cache_write','flat','multiplier'] as const;
type PriceNumber=typeof priceNumbers[number];
type PriceControls=Record<PriceNumber,HTMLInputElement>&{model:string;enabled:HTMLInputElement;mode:HTMLSelectElement;currency:HTMLSelectElement};
(() => {
  if (typeof document === 'undefined') return;
  const host = document.querySelector<HTMLElement>('[data-ai-config]');
  if (!host) return; const root=host;
  const $ = <K extends keyof ConfigFields>(name:K):ConfigFields[K] => required<ConfigFields[K]>(root,`[data-ai-config-${name}]`);
  const optional=<K extends keyof ConfigFields>(name:K)=>root.querySelector<ConfigFields[K]>(`[data-ai-config-${name}]`);
  const startupError = () => {
    const status=optional('status'),save=optional('save'),add=optional('add'),reload=optional('reload');
    if(status){status.textContent='AI 设置页面与脚本版本不一致，请刷新页面；若仍失败，请同步更新主题文件并重启服务。';status.dataset.tone='error';}
    if(save)save.disabled=true;if(add)add.disabled=true;
    if(reload){reload.disabled=false;reload.textContent='刷新页面';reload.addEventListener('click',()=>window.location.reload());}
  };
  if ((['form','fields','list','default','image-default','status','save','add','reload','empty','template','ops-enabled','ops-details','ops-costs','ops-balances','ops-charts','ops-retention','ops-records'] as const).some(name=>!optional(name)) || ['api-version','allow-insecure'].some(name=>!optional('template')?.content?.querySelector(`[data-provider-${name}]`))) { startupError();return; }
  try { initialize(); } catch { startupError(); }
  function initialize() {
  const form = $('form'), list = $('list'), defaultProvider = $('default'), defaultImageProvider = $('image-default');
  const protocols:Record<Protocol,{base:string;models:string;help:string}> = {
    '': {base: '', models: '每行一个模型 ID，也可以用逗号分隔', help: '用于文章写作，支持 Chat Completions 兼容接口。'},
    openai_responses: {base: 'https://api.openai.com/v1', models: '填写支持 Responses 的模型 ID', help: 'OpenAI Responses 原生接口，使用 /responses，支持流式写作。'},
    anthropic: {base: 'https://api.anthropic.com/v1', models: '填写 Claude 模型 ID', help: 'Claude Messages 原生接口，使用 x-api-key。Token 上限留空时使用 4096。'},
    gemini: {base: 'https://generativelanguage.googleapis.com/v1beta', models: '填写 Gemini 模型 ID', help: 'Gemini 原生 streamGenerateContent 接口，使用 x-goog-api-key。'},
    azure_openai: {base: '', models: '填写 Azure 中的模型部署名称', help: '微软 Azure OpenAI v1 Chat Completions。填写资源根地址或 /openai/v1，模型填写部署名称，使用 api-key。'},
    azure_responses: {base: '', models: '填写 Azure 中的模型部署名称', help: '微软 Azure OpenAI v1 Responses。填写资源根地址或 /openai/v1，模型填写部署名称，使用 api-key。'},
    azure_openai_legacy: {base: '', models: '填写 Azure 中的模型部署名称', help: 'Azure 部署式 Chat Completions。填写资源根地址和 API 版本，在模型栏手动填写部署名称。'},
    openai_images: {base: 'https://api.openai.com/v1', models: '例如：gpt-image-1\ngpt-image-1.5', help: '使用 /images/generations。中转服务按其实际协议选择，模型 ID 可自行填写。'},
    gemini_images: {base: 'https://generativelanguage.googleapis.com/v1beta', models: '例如：gemini-2.5-flash-image', help: 'Google 原生 generateContent 协议，使用 x-goog-api-key。模型须支持图片输出。'},
    gemini_interactions: {base: 'https://generativelanguage.googleapis.com/v1beta', models: '例如：gemini-3.1-flash-image', help: 'Google Interactions 图片协议。请确认服务商支持 /interactions。'},
    seedream: {base: 'https://ark.cn-beijing.volces.com/api/v3', models: '填写已开通的 Seedream 模型 ID\n或 ep- 开头的推理接入点 ID', help: '在火山方舟控制台复制已开通的模型或接入点 ID。支持 Seedream 文生图并保留服务商水印。'},
  };
  const readOnly = $('fields').disabled;
  let saved:AISettings|null=null, busy=false, loading=false, dirty=false, probing=0, editing=0, lastDraft='';
  const pricing=new WeakMap<HTMLElement,PriceControls[]>(),pricingArchive=new WeakMap<HTMLElement,Map<string,ModelPrice>>();
  const runtime=adminAccountRuntime(root,form.dataset.userId||'',namedControl(form,'csrf_token').value,readOnly,()=>{
    cards().forEach(card=>{field(card,'key').value='';field(card,'balance-token').value='';});saved=null;list.replaceChildren();controls();say('登录状态或页面已变化，请重新载入后继续。','error');
  });
  const say = (message:string, tone = '') => { $('status').textContent = message; $('status').dataset.tone = tone; };
  const cards = () => [...list.querySelectorAll<HTMLElement>('.ai-provider-card')];
  const field = <K extends keyof ProviderFields>(card:HTMLElement,name:K):ProviderFields[K] => required<ProviderFields[K]>(card,`[data-provider-${name}]`);
  const option = (value:string, label:string) => { const el = document.createElement('option'); el.value = value; el.textContent = label; return el; };
  function controls() {
    $('save').disabled = !runtime.alive() || readOnly || busy || loading || probing > 0 || !saved || !dirty;
    $('save').textContent = busy ? '保存中…' : '保存 AI 设置';
    $('add').disabled = !runtime.alive() || readOnly || loading || !saved || cards().length >= 20;
    $('reload').disabled = !runtime.alive() || busy || loading || probing > 0;
    $('reload').textContent = dirty ? '放弃修改并重新加载' : '重新加载';
    $('fields').disabled = !runtime.alive() || readOnly || !saved;
    for (const [select, image] of ([[defaultProvider, false], [defaultImageProvider, true]] as const)) select.disabled = !runtime.alive() || readOnly || !saved || !cards().some(card => isImageProtocol(field(card, 'protocol').value) === image);
    form.setAttribute('aria-busy', String(busy || loading));
  }
  function changed() { if(!runtime.alive()||readOnly)return;const current=JSON.stringify(readDraft());if(current===lastDraft)return;lastDraft=current;editing++;dirty=true;say('AI 设置有未保存的修改。');controls(); }
  const request=(path:string,body?:unknown)=>runtime.request('/api/v1/admin/ai/'+path,body,30000);
  function updateDefaults() {
    const providers = cards();
    for (const [select, image] of ([[defaultProvider, false], [defaultImageProvider, true]] as const)) {
      const current = select.value, available = providers.filter(card => isImageProtocol(field(card, 'protocol').value) === image);
      select.replaceChildren(...available.map((card, i) => option(card.dataset.providerId||'', field(card, 'name').value.trim() || `接口 ${i + 1}`)));
      if (!available.length) select.append(option('', image ? '尚未配置生图接口' : '尚未配置写作接口'));
      if (available.some(card => card.dataset.providerId === current)) select.value = current;
    }
    providers.forEach((card, i) => {
      required(card,'.ai-provider-number').textContent = String(i + 1).padStart(2, '0');
      field(card, 'title').textContent = field(card, 'name').value.trim() || '新接口';
    });
    $('empty').hidden = providers.length > 0; controls();
  }
  function updateModels(card:HTMLElement, selected?:string) {
    const models = parseModels(field(card, 'models').value), select = field(card, 'default');
    const prior = selected || select.value;
    select.replaceChildren(...models.map(value => option(value, value)));
    if (!models.length) select.append(option('', '先添加模型'));
    if (models.includes(prior)) select.value = prior;
    updatePrices(card);
    card.querySelectorAll<HTMLInputElement>('[data-provider-options] input').forEach(input => { input.checked = models.includes(input.value); });
  }
  function readPrices(card:HTMLElement):ModelPrice[] {
    return (pricing.get(card) || []).map(row => {
      const result:ModelPrice = {model:row.model, enabled:row.enabled.checked, currency:row.currency.value, mode:choice(row.mode.value,['tokens','image','request']), multiplier:Number(row.multiplier.value)};
      for (const key of (['input','output','cache_read','cache_write','flat'] as const)) if (row[key].value.trim() !== '') result[key] = Number(row[key].value);
      return result;
    });
  }
  function updatePrices(card:HTMLElement,initial?:ModelPrice[]) {
    const archive=pricingArchive.get(card)||new Map<string,ModelPrice>();
    for(const price of initial||readPrices(card))archive.set(price.model,price);pricingArchive.set(card,archive);
    const rows:PriceControls[]=[],containers:HTMLElement[]=[];
    for(const model of parseModels(field(card,'models').value)){
      const old:Partial<ModelPrice>=archive.get(model)||{};
      const row:PriceControls={model,enabled:document.createElement('input'),mode:document.createElement('select'),currency:document.createElement('select'),input:document.createElement('input'),output:document.createElement('input'),cache_read:document.createElement('input'),cache_write:document.createElement('input'),flat:document.createElement('input'),multiplier:document.createElement('input')};
      const section=document.createElement('div'),head=document.createElement('div'),name=document.createElement('code'),grid=document.createElement('div');
      section.className='ai-price-row';head.className='ai-price-heading';grid.className='ai-price-grid';name.textContent=model;
      const enabled=document.createElement('label');row.enabled.type='checkbox';row.enabled.checked=!!old.enabled;enabled.append(row.enabled,' 启用计价');head.append(name,enabled);section.append(head,grid);
      const wrap=(input:HTMLInputElement|HTMLSelectElement,label:string)=>{const element=document.createElement('label');input.setAttribute('aria-label',model+' · '+label);element.append(label,input);grid.append(element);return element;};
      row.mode.append(...[['tokens','每百万 Token'],['image','每张图片'],['request','每次成功请求']].map(([value,label])=>option(value!,label!)));row.mode.value=old.mode||(isImageProtocol(field(card,'protocol').value)?'image':'tokens');wrap(row.mode,'计价方式');
      row.currency.append(...['USD','CNY','EUR','GBP','JPY'].map(value=>option(value,value)));row.currency.value=old.currency||'USD';wrap(row.currency,'币种');
      const numberField=(key:PriceNumber,label:string)=>{const input=row[key];input.type='number';input.min=key==='multiplier'?'0.001':'0';input.max=key==='multiplier'?'1000':'1000000';input.step='any';input.placeholder='未配置';input.value=String(old[key]??(key==='multiplier'?1:''));return wrap(input,label);};
      numberField('multiplier','费用倍率');const tokenFields=([['input','输入单价'],['output','输出单价'],['cache_read','缓存读取'],['cache_write','缓存写入']] as const).map(([key,label])=>numberField(key,label)),flat=numberField('flat','每张 / 每次单价');
      const show=()=>{tokenFields.forEach(el=>el.hidden=row.mode.value!=='tokens');flat.hidden=row.mode.value==='tokens';};row.mode.addEventListener('change',show);show();rows.push(row);containers.push(section);
    }
    pricing.set(card,rows);field(card,'pricing-list').replaceChildren(...containers);
  }
  function readBalance(card:HTMLElement):BalanceConfig {
    return {has_token:saved?.providers.find(p=>p.id===card.dataset.providerId)?.balance.has_token||false,enabled:field(card,'balance-enabled').checked, format:field(card,'balance-format').value, currency:field(card,'balance-currency').value,
      path:field(card,'balance-path').value.trim(), auth:field(card,'balance-auth').value, cache_seconds:Number(field(card,'balance-cache').value),
      token:field(card,'balance-token').value.trim(), clear_token:field(card,'balance-clear').checked, user_id:field(card,'balance-user').value.trim(),
      remaining_path:field(card,'balance-remaining').value.trim(), used_path:field(card,'balance-used').value.trim(), total_path:field(card,'balance-total').value.trim(), divisor:Number(field(card,'balance-divisor').value)};
  }
  async function probeBalance(card:HTMLElement) {
    const button=field(card,'balance-probe'), status=field(card,'balance-status');
    if (!runtime.alive() || readOnly || busy || loading || button.disabled) return;
    if (dirty || !saved?.providers.some(p=>p.id===card.dataset.providerId)) { status.textContent='请先保存当前设置，再查询余额。'; return; }
    probing++; button.disabled=true; controls(); status.textContent='正在查询余额…';
    try { const data=balanceResult(await request('balance',{provider_id:card.dataset.providerId})); if(!runtime.alive()||!card.isConnected||dirty)return;status.textContent=`剩余 ${data.currency} ${data.remaining===undefined?'未知':data.remaining.toLocaleString('zh-CN',{maximumFractionDigits:6})} · ${data.cached?'缓存结果':'已更新'}`; }
    catch(error) { if(runtime.alive())status.textContent=failure(error).message; }
    finally { probing--; button.disabled=false; controls(); }
  }
  function addProvider(provider:Partial<AIProvider> = {}) {
    const card = $('template').content.firstElementChild?.cloneNode(true);if(!(card instanceof HTMLElement))throw new Error('AI 接口模板缺失');
    card.dataset.providerId = provider.id || crypto.randomUUID();
    field(card, 'name').value = provider.name || '';
    field(card, 'protocol').value = provider.protocol || '';
    if (!Object.hasOwn(protocols, provider.protocol || '')) throw new Error('当前主题不支持此接口协议，请同步更新主题和服务端。');
    field(card, 'api-version').value = provider.api_version || '2024-10-21';
    field(card, 'base').value = provider.base_url || '';field(card,'key').value=provider.api_key||'';
    field(card, 'allow-insecure').checked = Boolean(provider.allow_insecure);
    field(card, 'models').value = (provider.models || []).join('\n');
    field(card, 'key-state').textContent = provider.has_key ? 'Key 已保存' : provider.id ? '已保存 · 无 Key' : '尚未保存';
    const requireKey = () => {
      const existing=saved?.providers.find(p=>p.id===card.dataset.providerId);
      const sameTarget=field(card,'base').value.trim()===existing?.base_url&&field(card,'protocol').value===(existing?.protocol||'');
      const approved = field(card, 'allow-insecure').checked;
      field(card, 'key').required = existing?.has_key ? !sameTarget : !approved;
      field(card, 'key').placeholder = existing?.has_key && sameTarget ? '已保存，留空保留；填写新 Key 可替换' : approved && !existing?.has_key ? '无鉴权服务可留空' : '填写服务商提供的 Key';
    };
    const resetApproval = () => {
      field(card, 'allow-insecure').checked = false;
      field(card, 'discovered').hidden = true;
      field(card, 'options').replaceChildren();
      field(card, 'status').textContent = '地址已修改；如需 HTTP／本地网络，请重新勾选允许。';
      field(card, 'status').dataset.tone = '';
      requireKey();
    };
    const describeProtocol = () => {
      const kind=choice(field(card,'protocol').value,protocolIDs),preset=protocols[kind];
      field(card, 'protocol-help').textContent = preset.help;
      field(card, 'models').placeholder = preset.models;
      field(card, 'probe').hidden = kind === 'seedream' || kind === 'azure_openai_legacy';
      field(card, 'api-version-field').hidden = kind !== 'azure_openai_legacy';
      field(card, 'api-version').disabled = kind !== 'azure_openai_legacy';
      field(card, 'base').placeholder = kind.startsWith('azure_') ? 'https://your-resource.openai.azure.com' : preset.base || 'https://api.example.com/v1';
    };
    describeProtocol(); requireKey();
    field(card, 'protocol').addEventListener('change', () => {
      const preset=protocols[choice(field(card,'protocol').value,protocolIDs)];
      if (!field(card, 'base').value.trim()) { field(card, 'base').value = preset.base; resetApproval(); }
      field(card, 'discovered').hidden = true; field(card, 'options').replaceChildren(); field(card, 'status').textContent = '';
      describeProtocol(); requireKey(); updateDefaults();
    });
    updateModels(card, provider.default_model);
    updatePrices(card, provider.pricing || []);
    const balance=provider.balance || {...defaultBalance};
    field(card,'balance-enabled').checked=!!balance.enabled;
    for (const [fieldName,key,fallback] of ([['format','format','custom'],['currency','currency','USD'],['path','path',''],['auth','auth','bearer'],['cache','cache_seconds',300],['user','user_id',''],['remaining','remaining_path',''],['used','used_path',''],['total','total_path',''],['divisor','divisor',1]] as const)) field(card,`balance-${fieldName}`).value=String(balance[key] ?? fallback);
    field(card,'balance-token').value=balance.token;field(card,'balance-clear').checked=balance.clear_token;
    field(card,'balance-key-state').textContent=balance.has_token?'独立查询令牌已保存，留空保留。':'未保存独立令牌；留空使用接口 Key，New API 需要独立访问令牌。';
    field(card,'balance-probe').addEventListener('click',()=>probeBalance(card));
    field(card,'balance-format').addEventListener('change',()=>{
      const format=field(card,'balance-format').value;
      field(card,'balance-divisor').value=format==='newapi'?'500000':'1';
      if (format==='siliconflow' || format==='deepseek') field(card,'balance-currency').value='CNY';
      else if (['newapi','openrouter','sub2api'].includes(format)) field(card,'balance-currency').value='USD';
    });
    field(card, 'models').addEventListener('input', () => updateModels(card));
    field(card, 'name').addEventListener('input', updateDefaults);
    field(card, 'base').addEventListener('input', resetApproval);
    field(card, 'allow-insecure').addEventListener('change', requireKey);
    field(card, 'remove').addEventListener('click', () => { card.remove(); updateDefaults(); changed(); });
    field(card, 'probe').addEventListener('click', () => probe(card));
    list.append(card); return card;
  }
  function render(data:AISettings) {
    list.replaceChildren();data.providers.forEach(addProvider);
    for(const key of ['enabled','details','costs','balances','charts'] as const)$(`ops-${key}`).checked=data.telemetry[key];
    $('ops-retention').value=String(data.telemetry.retention_days);$('ops-records').value=String(data.telemetry.max_records);
    updateDefaults();defaultProvider.value=data.default_provider;defaultImageProvider.value=data.default_image_provider;lastDraft=JSON.stringify(readDraft());controls();
  }
  async function load() {
    if(!runtime.alive()||busy||loading||probing)return;
    if(dirty&&!window.confirm('放弃尚未保存的 AI 设置，并重新加载？'))return;
    loading=true;const serial=editing;
    try {controls();say('正在加载 AI 设置…');const data=aiSettings(await request('settings'));if(serial!==editing){say('读取期间有新输入，已保留当前修改；请保存或重新加载核对。');return;}saved=data;dirty=false;render(data);say(data.providers.length?'接口已保存，可在 AI 编辑器中选择。':'添加第一个接口即可开始。');}
    catch(error){if(runtime.alive())say(failure(error).message,'error');}
    finally{loading=false;controls();}
  }
  async function probe(card:HTMLElement) {
    const button = field(card, 'probe'), status = field(card, 'status');
    if (!runtime.alive() || readOnly || button.disabled || busy || loading) return;
    const key = field(card, 'key').value.trim(), base = field(card, 'base').value.trim(), protocol = field(card, 'protocol').value;
    const existing = saved?.providers.find(provider => provider.id === card.dataset.providerId);
    const approved = field(card, 'allow-insecure').checked;
    const sameTarget = existing?.base_url === base && (existing.protocol || '') === protocol;
    let connection;
    if (!key && sameTarget && Boolean(existing.allow_insecure) === approved) connection = {provider_id: existing.id};
    else if (base && (key || approved && !existing?.has_key)) connection = {api_key: key, base_url: base, protocol, allow_insecure: approved};
    else { status.textContent = existing?.has_key && sameTarget ? '地址授权已修改，请先保存设置或重新填写 Key 后探测。' : '请填写 API 地址与 Key；无鉴权服务请勾选允许 HTTP／本地网络，已有 Key 的接口更换地址后需重新填写 Key。'; status.dataset.tone = 'error'; return; }
    const revision = [base, key, protocol, approved].join('\n');
    probing++; button.disabled = true; button.setAttribute('aria-busy', 'true'); controls();
    status.textContent = '正在探测可用模型…'; status.dataset.tone = '';
    try {
      const data=discoveredModels(await request('models',connection));if(!runtime.alive())return;
      if (!card.isConnected || revision !== [field(card, 'base').value.trim(), field(card, 'key').value.trim(), field(card, 'protocol').value, field(card, 'allow-insecure').checked].join('\n')) { status.textContent = '连接信息已修改，请重新探测。'; return; }
      
      const current = parseModels(field(card, 'models').value);
      field(card, 'options').replaceChildren(...data.models.map(model => {
        const label = document.createElement('label'), input = document.createElement('input'), text = document.createElement('span');
        input.type = 'checkbox'; input.value = model.id; input.checked = current.includes(model.id); text.textContent = model.id;
        input.addEventListener('change', () => {
          let models = parseModels(field(card, 'models').value);
          if (input.checked && !models.includes(input.value)) models.push(input.value);
          if (!input.checked) models = models.filter(value => value !== input.value);
          field(card, 'models').value = models.join('\n'); updateModels(card); changed();
        });
        label.append(input, text); return label;
      }));
      field(card, 'discovered').hidden = false;
      status.textContent = `找到 ${data.models.length} 个模型，请勾选常用模型后保存。`; status.dataset.tone = 'success';
    } catch (error) { if(runtime.alive()&&card.isConnected){status.textContent=failure(error).message;status.dataset.tone='error';} }
    finally { probing--; button.disabled = false; button.removeAttribute('aria-busy'); controls(); }
  }
  form.addEventListener('input', changed);
  form.addEventListener('change', changed);
  $('add').addEventListener('click', () => { if(!runtime.alive()||readOnly||loading||!saved||cards().length>=20)return;const card = addProvider(); updateDefaults(); changed(); field(card, 'name').focus(); });
  $('reload').addEventListener('click', load);
  function readDraft():AISettings {
    const telemetry:TelemetryConfig={...telemetryDefaults,retention_days:Number($('ops-retention').value),max_records:Number($('ops-records').value)};
    for(const key of ['enabled','details','costs','balances','charts'] as const)telemetry[key]=$(`ops-${key}`).checked;
    return {telemetry,revision:saved?.revision||'',default_provider:defaultProvider.value,default_image_provider:defaultImageProvider.value,providers:cards().map(card=>({
      id:card.dataset.providerId||'',name:field(card,'name').value.trim(),base_url:field(card,'base').value.trim(),protocol:choice(field(card,'protocol').value,protocolIDs),pricing:readPrices(card),balance:readBalance(card),allow_insecure:field(card,'allow-insecure').checked,
      api_version:field(card,'protocol').value==='azure_openai_legacy'?field(card,'api-version').value.trim():'',api_key:field(card,'key').value.trim(),has_key:saved?.providers.find(p=>p.id===card.dataset.providerId)?.has_key||false,models:parseModels(field(card,'models').value),default_model:field(card,'default').value,
    }))};
  }
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(!runtime.alive()||busy||loading||probing||readOnly||!saved||!form.reportValidity())return;
    const sent=readDraft(),baseline=saved,serial=editing;busy=true;controls();say('正在保存 AI 设置…');
    try {const result=savedAISettings(await request('settings',sent),sent,baseline);if(!runtime.alive())return;const keep=editing!==serial,current=keep?mergeAISettings(readDraft(),sent,result):result;saved=result;dirty=keep;render(current);say(keep?'本次设置已保存；后续输入仍待保存。':'AI 接口与模型已保存。','success');}
    catch(error){if(runtime.alive())say(failure(error).message,'error');}
    finally{busy=false;controls();}
  });
  const siteBar = document.querySelector<HTMLElement>('[data-settings-savebar]');
  const toggleBar = () => { if (siteBar) siteBar.hidden = !root.hidden || !!document.querySelector('[data-verification-settings]:not([hidden]), [data-access-settings]:not([hidden])'); };
  document.addEventListener('appearance-preview-section', toggleBar); toggleBar();
  window.addEventListener('beforeunload', event => { if (dirty || busy) { event.preventDefault(); event.returnValue = ''; } });
  void load();
  }
})();
