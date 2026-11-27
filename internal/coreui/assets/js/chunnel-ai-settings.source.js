(() => {
  'use strict';
  const parseModels = text => [...new Set(String(text || '').split(/[\n,，]+/).map(value => value.trim()).filter(Boolean))];
  const imageProtocols = ['openai_images', 'gemini_images', 'gemini_interactions', 'seedream'];
  const isImageProtocol = protocol => imageProtocols.includes(protocol);
  if (typeof module !== 'undefined' && module.exports) module.exports = {parseModels, isImageProtocol};
  if (typeof document === 'undefined') return;
  const root = document.querySelector('[data-ai-config]');
  if (!root) return;
  const $ = name => root.querySelector(`[data-ai-config-${name}]`);
  const startupError = () => {
    if ($('status')) { $('status').textContent = 'AI 设置页面与脚本版本不一致，请刷新页面；若仍失败，请同步更新主题文件并重启服务。'; $('status').dataset.tone = 'error'; }
    if ($('save')) $('save').disabled = true;
    if ($('add')) $('add').disabled = true;
    if ($('reload')) { $('reload').disabled = false; $('reload').textContent = '刷新页面'; $('reload').addEventListener('click', () => window.location.reload()); }
  };
  // Cached HTML from older themes lacks fields used by the current controller.
  // Report that mismatch before accessing DOM properties or starting a request.
  if (['form', 'fields', 'list', 'default', 'image-default', 'status', 'save', 'add', 'reload', 'empty', 'template', 'ops-enabled', 'ops-details', 'ops-costs', 'ops-balances', 'ops-charts', 'ops-retention', 'ops-records'].some(name => !$(name)) ||
      ['api-version', 'allow-insecure'].some(name => !$('template').content?.querySelector(`[data-provider-${name}]`))) { startupError(); return; }
  try { initialize(); } catch { startupError(); }
  function initialize() {
  const form = $('form'), list = $('list'), defaultProvider = $('default'), defaultImageProvider = $('image-default');
  const protocols = {
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
  let saved = null, busy = false, loading = false, dirty = false, probing = 0;
  const controllers = new Set();
  const pricing = new WeakMap(), pricingArchive = new WeakMap();
  const telemetryDefaults = {enabled:true, details:true, costs:true, balances:false, charts:true, retention_days:30, max_records:5000};
  const say = (message, tone = '') => { $('status').textContent = message; $('status').dataset.tone = tone; };
  const cards = () => [...list.querySelectorAll('.ai-provider-card')];
  const field = (card, name) => card.querySelector(`[data-provider-${name}]`);
  const option = (value, label) => { const el = document.createElement('option'); el.value = value; el.textContent = label; return el; };
  function controls() {
    $('save').disabled = readOnly || busy || loading || probing > 0 || !saved || !dirty;
    $('save').textContent = busy ? '保存中…' : '保存 AI 设置';
    $('add').disabled = readOnly || busy || loading || !saved || cards().length >= 20;
    $('reload').disabled = busy || loading || probing > 0;
    $('reload').textContent = dirty ? '放弃修改并重新加载' : '重新加载';
    $('fields').disabled = readOnly || busy || loading;
    for (const [select, image] of [[defaultProvider, false], [defaultImageProvider, true]]) select.disabled = readOnly || busy || loading || !cards().some(card => isImageProtocol(field(card, 'protocol').value) === image);
    form.setAttribute('aria-busy', String(busy || loading));
  }
  function changed() { dirty = true; say('AI 设置有未保存的修改。'); controls(); }
  async function jsonRequest(path, body, controller) {
    const options = {credentials: 'same-origin', redirect: 'error', cache: 'no-store', signal: controller.signal, headers: {Accept: 'application/json'}};
    if (body) {
      const sessionResponse = await fetch('/api/v1/admin/session', options);
      const session = await sessionResponse.json();
      if (!sessionResponse.ok || !session.authenticated || session.read_only || String(session.user_id) !== form.dataset.userId || !session.csrf_token) throw new Error('登录身份已变化，请重新登录原账号后保存。');
      options.method = 'POST';
      options.headers = {...options.headers, 'Content-Type': 'application/json', 'X-CSRF-Token': session.csrf_token};
      options.body = JSON.stringify(body);
    }
    const response = await fetch(`/api/v1/admin/ai/${path}`, options);
    let data;
    try { data = await response.json(); } catch { throw new Error('服务器响应无效，请稍后重试。'); }
    if (!response.ok) throw new Error(data?.error?.message || '请求失败，请重试。');
    return data;
  }
  async function request(path, body = null) {
    const controller = new AbortController(); controllers.add(controller);
    const timer = setTimeout(() => controller.abort(), 30000);
    try { return await jsonRequest(path, body, controller); }
    catch (error) { if (error.name === 'AbortError') throw new Error('请求超时或已取消，请重试。'); throw error; }
    finally { clearTimeout(timer); controllers.delete(controller); }
  }
  function updateDefaults() {
    const providers = cards();
    for (const [select, image] of [[defaultProvider, false], [defaultImageProvider, true]]) {
      const current = select.value, available = providers.filter(card => isImageProtocol(field(card, 'protocol').value) === image);
      select.replaceChildren(...available.map((card, i) => option(card.dataset.providerId, field(card, 'name').value.trim() || `接口 ${i + 1}`)));
      if (!available.length) select.append(option('', image ? '尚未配置生图接口' : '尚未配置写作接口'));
      if (available.some(card => card.dataset.providerId === current)) select.value = current;
    }
    providers.forEach((card, i) => {
      card.querySelector('.ai-provider-number').textContent = String(i + 1).padStart(2, '0');
      field(card, 'title').textContent = field(card, 'name').value.trim() || '新接口';
    });
    $('empty').hidden = providers.length > 0; controls();
  }
  function updateModels(card, selected) {
    const models = parseModels(field(card, 'models').value), select = field(card, 'default');
    const prior = selected || select.value;
    select.replaceChildren(...models.map(value => option(value, value)));
    if (!models.length) select.append(option('', '先添加模型'));
    if (models.includes(prior)) select.value = prior;
    updatePrices(card);
    card.querySelectorAll('[data-provider-options] input').forEach(input => { input.checked = models.includes(input.value); });
  }
  function readPrices(card) {
    return (pricing.get(card) || []).map(row => {
      const result = {model:row.model, enabled:row.enabled.checked, currency:row.currency.value, mode:row.mode.value, multiplier:Number(row.multiplier.value)};
      for (const key of ['input','output','cache_read','cache_write','flat']) if (row[key].value.trim() !== '') result[key] = Number(row[key].value);
      return result;
    });
  }
  function updatePrices(card, initial) {
    const archive = pricingArchive.get(card) || new Map();
    for (const price of initial || readPrices(card)) archive.set(price.model,price);
    pricingArchive.set(card,archive);
    const previous = [...archive.values()], rows = [], containers = [];
    for (const model of parseModels(field(card,'models').value)) {
      const old = previous.find(price => price.model === model) || {}, row = {model};
      const section = document.createElement('div'), head = document.createElement('div'), name = document.createElement('code'), grid = document.createElement('div');
      section.className = 'ai-price-row'; head.className = 'ai-price-heading'; grid.className = 'ai-price-grid'; name.textContent = model;
      const enabled = document.createElement('label'); row.enabled = document.createElement('input'); row.enabled.type = 'checkbox'; row.enabled.checked = !!old.enabled; enabled.append(row.enabled, ' 启用计价'); head.append(name,enabled); section.append(head,grid);
      const control = (key, label, choices, fallback) => {
        const wrap = document.createElement('label'), input = document.createElement(choices ? 'select' : 'input'); row[key] = input;
        if (choices) { input.append(...choices.map(([value,text]) => option(value,text))); input.value = old[key] ?? fallback; }
        else { input.type='number'; input.min=key==='multiplier'?'0.001':'0'; input.max=key==='multiplier'?'1000':'1000000'; input.step='any'; input.placeholder='未配置'; input.value=old[key] ?? fallback ?? ''; }
        input.setAttribute('aria-label', `${model} · ${label}`); wrap.append(label,input); grid.append(wrap); return wrap;
      };
      control('mode','计价方式',[['tokens','每百万 Token'],['image','每张图片'],['request','每次成功请求']],isImageProtocol(field(card,'protocol').value)?'image':'tokens');
      control('currency','币种',['USD','CNY','EUR','GBP','JPY'].map(value=>[value,value]),'USD');
      control('multiplier','费用倍率',null,1);
      const tokenFields = [['input','输入单价'],['output','输出单价'],['cache_read','缓存读取'],['cache_write','缓存写入']].map(([key,label])=>control(key,label));
      const flat = control('flat','每张 / 每次单价');
      const show = () => { tokenFields.forEach(el=>el.hidden=row.mode.value!=='tokens'); flat.hidden=row.mode.value==='tokens'; };
      row.mode.addEventListener('change',show); show(); rows.push(row); containers.push(section);
    }
    pricing.set(card,rows); field(card,'pricing-list').replaceChildren(...containers);
  }
  function readBalance(card) {
    return {enabled:field(card,'balance-enabled').checked, format:field(card,'balance-format').value, currency:field(card,'balance-currency').value,
      path:field(card,'balance-path').value.trim(), auth:field(card,'balance-auth').value, cache_seconds:Number(field(card,'balance-cache').value),
      token:field(card,'balance-token').value.trim(), clear_token:field(card,'balance-clear').checked, user_id:field(card,'balance-user').value.trim(),
      remaining_path:field(card,'balance-remaining').value.trim(), used_path:field(card,'balance-used').value.trim(), total_path:field(card,'balance-total').value.trim(), divisor:Number(field(card,'balance-divisor').value)};
  }
  async function probeBalance(card) {
    const button=field(card,'balance-probe'), status=field(card,'balance-status');
    if (busy || loading || button.disabled) return;
    if (dirty || !saved?.providers.some(p=>p.id===card.dataset.providerId)) { status.textContent='请先保存当前设置，再查询余额。'; return; }
    probing++; button.disabled=true; controls(); status.textContent='正在查询余额…';
    try { const data=await request('balance',{provider_id:card.dataset.providerId}); status.textContent=`剩余 ${data.currency} ${Number(data.remaining).toLocaleString('zh-CN',{maximumFractionDigits:6})} · ${data.cached?'缓存结果':'已更新'}`; }
    catch(error) { status.textContent=error.message; }
    finally { probing--; button.disabled=false; controls(); }
  }
  function addProvider(provider = {}) {
    const card = $('template').content.firstElementChild.cloneNode(true);
    card.dataset.providerId = provider.id || crypto.randomUUID();
    field(card, 'name').value = provider.name || '';
    field(card, 'protocol').value = provider.protocol || '';
    if (!Object.hasOwn(protocols, provider.protocol || '')) throw new Error('当前主题不支持此接口协议，请同步更新主题和服务端。');
    field(card, 'api-version').value = provider.api_version || '2024-10-21';
    field(card, 'base').value = provider.base_url || '';
    field(card, 'allow-insecure').checked = Boolean(provider.allow_insecure);
    field(card, 'models').value = (provider.models || []).join('\n');
    field(card, 'key-state').textContent = provider.has_key ? 'Key 已保存' : provider.id ? '已保存 · 无 Key' : '尚未保存';
    const requireKey = () => {
      const sameTarget = field(card, 'base').value.trim() === provider.base_url && field(card, 'protocol').value === (provider.protocol || '');
      const approved = field(card, 'allow-insecure').checked;
      field(card, 'key').required = provider.has_key ? !sameTarget : !approved;
      field(card, 'key').placeholder = provider.has_key && sameTarget ? '已保存，留空保留；填写新 Key 可替换' : approved && !provider.has_key ? '无鉴权服务可留空' : '填写服务商提供的 Key';
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
      const kind = field(card, 'protocol').value, preset = protocols[kind];
      field(card, 'protocol-help').textContent = preset.help;
      field(card, 'models').placeholder = preset.models;
      field(card, 'probe').hidden = kind === 'seedream' || kind === 'azure_openai_legacy';
      field(card, 'api-version-field').hidden = kind !== 'azure_openai_legacy';
      field(card, 'api-version').disabled = kind !== 'azure_openai_legacy';
      field(card, 'base').placeholder = kind.startsWith('azure_') ? 'https://your-resource.openai.azure.com' : preset.base || 'https://api.example.com/v1';
    };
    describeProtocol(); requireKey();
    field(card, 'protocol').addEventListener('change', () => {
      const preset = protocols[field(card, 'protocol').value];
      if (!field(card, 'base').value.trim()) { field(card, 'base').value = preset.base; resetApproval(); }
      field(card, 'discovered').hidden = true; field(card, 'options').replaceChildren(); field(card, 'status').textContent = '';
      describeProtocol(); requireKey(); updateDefaults();
    });
    updateModels(card, provider.default_model);
    updatePrices(card, provider.pricing || []);
    const balance=provider.balance || {};
    field(card,'balance-enabled').checked=!!balance.enabled;
    for (const [fieldName,key,fallback] of [['format','format','custom'],['currency','currency','USD'],['path','path',''],['auth','auth','bearer'],['cache','cache_seconds',300],['user','user_id',''],['remaining','remaining_path',''],['used','used_path',''],['total','total_path',''],['divisor','divisor',1]]) field(card,`balance-${fieldName}`).value=balance[key] ?? fallback;
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
  function render(data) {
    if (!data || typeof data.revision !== 'string' || !Array.isArray(data.providers)) throw new Error('AI 设置格式无效，请重新加载。');
    // Validate first so a failed reload never becomes a writable partial configuration.
    if (data.providers.some(provider => !provider || !Object.hasOwn(protocols, provider.protocol || '') || !Array.isArray(provider.models))) throw new Error('AI 设置协议或格式不兼容，请同步更新主题和服务端后重新加载。');
    saved = null; list.replaceChildren();
    data.providers.forEach(addProvider);
    saved = data;
    const telemetry={...telemetryDefaults,...data.telemetry};
    for(const key of ['enabled','details','costs','balances','charts']) $(`ops-${key}`).checked=!!telemetry[key];
    $('ops-retention').value=String(telemetry.retention_days); $('ops-records').value=String(telemetry.max_records);
    updateDefaults(); defaultProvider.value = data.default_provider; defaultImageProvider.value = data.default_image_provider || '';
    dirty = false; controls();
  }
  async function load() {
    if (busy || loading || probing) return;
    if (dirty && !window.confirm('放弃尚未保存的 AI 设置，并重新加载？')) return;
    loading = true;
    try { controls(); say('正在加载 AI 设置…'); render(await request('settings')); say(saved.providers.length ? '接口已保存，可在 AI 编辑器中选择。' : '添加第一个接口即可开始。'); }
    catch (error) { say(error.message, 'error'); }
    finally { loading = false; controls(); }
  }
  async function probe(card) {
    const button = field(card, 'probe'), status = field(card, 'status');
    if (button.disabled || busy) return;
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
      const data = await request('models', connection);
      if (!card.isConnected || revision !== [field(card, 'base').value.trim(), field(card, 'key').value.trim(), field(card, 'protocol').value, field(card, 'allow-insecure').checked].join('\n')) { status.textContent = '连接信息已修改，请重新探测。'; return; }
      if (!Array.isArray(data.models) || data.models.some(model => typeof model.id !== 'string')) throw new Error('未收到有效的模型列表，可手动填写模型 ID。');
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
    } catch (error) { status.textContent = error.message; status.dataset.tone = 'error'; }
    finally { probing--; button.disabled = false; button.removeAttribute('aria-busy'); controls(); }
  }
  form.addEventListener('input', changed);
  form.addEventListener('change', changed);
  $('add').addEventListener('click', () => { const card = addProvider(); updateDefaults(); changed(); field(card, 'name').focus(); });
  $('reload').addEventListener('click', load);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (busy || loading || probing || readOnly || !saved || !form.reportValidity()) return;
    const telemetry={retention_days:Number($('ops-retention').value), max_records:Number($('ops-records').value)};
    for(const key of ['enabled','details','costs','balances','charts']) telemetry[key]=$(`ops-${key}`).checked;
    const payload = {telemetry, revision: saved.revision, default_provider: defaultProvider.value, default_image_provider: defaultImageProvider.value, providers: cards().map(card => ({
      id: card.dataset.providerId, name: field(card, 'name').value.trim(), base_url: field(card, 'base').value.trim(),
      protocol: field(card, 'protocol').value, pricing:readPrices(card), balance:readBalance(card),
      allow_insecure: field(card, 'allow-insecure').checked,
      api_version: field(card, 'protocol').value === 'azure_openai_legacy' ? field(card, 'api-version').value.trim() : '',
      api_key: field(card, 'key').value.trim(), models: parseModels(field(card, 'models').value), default_model: field(card, 'default').value,
    }))};
    busy = true; controls(); say('正在保存 AI 设置…');
    try { render(await request('settings', payload)); say('AI 接口与模型已保存。', 'success'); }
    catch (error) { say(error.message, 'error'); }
    finally { busy = false; controls(); }
  });
  const siteBar = document.querySelector('[data-settings-savebar]');
  const toggleBar = () => { if (siteBar) siteBar.hidden = !root.hidden || !!document.querySelector('[data-verification-settings]:not([hidden]), [data-access-settings]:not([hidden])'); };
  document.addEventListener('appearance-preview-section', toggleBar); toggleBar();
  window.addEventListener('beforeunload', event => { if (dirty || busy) { event.preventDefault(); event.returnValue = ''; } });
  window.addEventListener('pagehide', () => { controllers.forEach(controller => controller.abort()); cards().forEach(card => { field(card, 'key').value = ''; field(card,'balance-token').value = ''; }); });
  window.addEventListener('pageshow', event => { if (event.persisted && !dirty && !busy && !loading && !probing) void load(); });
  void load();
  }
})();
