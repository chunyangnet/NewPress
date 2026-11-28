import { required } from './ai-writing-dom';
import type {} from './ai-types';
import type {} from './composer-types';
import type {} from '../shared/account-types';
import type { EditorSourceSelection } from './media-types';
import { record } from '../shared/ledger-data';
import { writingJSON,writingFailure,writingSession,writingConnections,writingPreference,aiMedia,imageDone,mediaURL } from '../shared/ai-writing-data';
import type { WritingPreference,WritingConnections,AIMedia } from '../shared/ai-writing-data';
import { IMAGE_PROTOCOLS,imageProfile,chooseImageConnection } from '../shared/ai-image-helpers';
type ImageDraft=AIMedia&{editAlt?:string;editCaption?:string};
type Fields={'dialog':HTMLDialogElement;'close':HTMLButtonElement;'form':HTMLFormElement;'fields':HTMLFieldSetElement;'prompt':HTMLTextAreaElement;'from-title':HTMLButtonElement;'refresh':HTMLButtonElement;'provider':HTMLSelectElement;'model':HTMLSelectElement;'ratio':HTMLSelectElement;'quality-field':HTMLElement;'quality':HTMLSelectElement;'resolution-field':HTMLElement;'resolution':HTMLSelectElement;'model-help':HTMLElement;'empty-config':HTMLElement;'generate':HTMLButtonElement;'stop':HTMLButtonElement;'canvas':HTMLElement;'placeholder':HTMLElement;'stage-title':HTMLElement;'stage-detail':HTMLElement;'preview':HTMLImageElement;'loading':HTMLElement;'phase':HTMLElement;'elapsed':HTMLElement;'gallery':HTMLElement;'info':HTMLElement;'alt':HTMLInputElement;'caption':HTMLInputElement;'status':HTMLElement;'cover':HTMLButtonElement;'insert':HTMLButtonElement};
(() => {
if(typeof document==='undefined')return;
  const dialogNode = document.querySelector<HTMLDialogElement>('[data-ai-image-dialog]'), editorNode = document.querySelector<HTMLFormElement>('[data-content-editor]');
  if(!dialogNode||!editorNode)return;const dialog=dialogNode,editor=editorNode;
  const field=(name:string)=>required(editor.querySelector<HTMLInputElement|HTMLTextAreaElement|HTMLSelectElement>(`[name="${name}"]`));
  const sourceNode = editor.querySelector<HTMLTextAreaElement>('[data-content-source]'), cover = editor.querySelector<HTMLInputElement>('[name="cover_url"]');
  const coreNode=window.ChunnelAICore,blockEditorNode=window.ChunnelBlockEditor;
  if (!sourceNode || !coreNode || !blockEditorNode) return;const source=sourceNode,core=coreNode,blockEditor=blockEditorNode;
  const $ = <K extends keyof Fields>(name: K): Fields[K] => required(dialog.querySelector<Fields[K]>(`[data-ai-image-${name}]`));
  const form = $('form'), provider = $('provider'), model = $('model'), preview = $('preview');
  const preferenceKey = `newpress:ai-image-choice:${location.host}:${editor.dataset.editorUserId}:${editor.dataset.editorLogin}`;
  let preference: WritingPreference = {}, connections: WritingConnections = {providers: []}, images: ImageDraft[] = [], selected: ImageDraft | null = null;
  let loading = false, generating = false, applying = false, imageReady = false, loadController:AbortController|null = null, generationController:AbortController|null = null, applyController:AbortController|null = null;
  let selection: EditorSourceSelection | null = null, coverSnapshot = '', launcher: HTMLButtonElement | null = null, returnFocus = true, elapsedTimer = 0, started = 0, mode = 'body';
  try { preference = writingPreference(JSON.parse(localStorage.getItem(preferenceKey) || '{}') as unknown); } catch { /* Preferences are optional. No keys are stored. */ }
  const option = (value: string, label: string) => { const element = document.createElement('option'); element.value = value; element.textContent = label; return element; };
  const say = (text: string, tone = '') => { $('status').textContent = text; $('status').dataset.tone = tone; };
  const owner=editor.dataset.editorUserId||'',login=editor.dataset.editorLogin||'',csrf=field('csrf_token').value;let retired=false;
  const alive=()=>!retired&&editor.isConnected&&dialog.isConnected;
  function retire(){if(retired)return;retired=true;generationController?.abort();loadController?.abort();applyController?.abort();clearInterval(elapsedTimer);images=[];selected=null;selection=null;preview.removeAttribute('src');preview.onload=null;preview.onerror=null;$('gallery').replaceChildren();$('prompt').value='';$('alt').value='';$('caption').value='';dialog.close();controls();}
  document.addEventListener('chunnel:session',event=>{const current=event.detail;if(current?.authenticated!==true||String(current.id)!==owner||current.csrf_token!==csrf)retire();});
  document.addEventListener('chunnel:admin-before-replace',event=>{if(event.detail.regions.some(region=>region.contains(editor)))retire();});
  window.addEventListener('pagehide',retire);window.addEventListener('pageshow',event=>{if(event.persisted)retire();});
  const readOnly = () => !alive() || source.disabled || source.readOnly || editor.getAttribute('aria-busy') === 'true';
  const currentProvider = () => connections.providers.find(item => item.id === provider.value);
  function controls() {
    if(!alive()){dialog.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement|HTMLButtonElement>('input,select,textarea,button').forEach(input=>{input.disabled=true;});return;}
    const busy = generating || applying;
    $('fields').disabled = busy || loading;
    $('generate').disabled = busy || loading || !provider.value || !model.value || readOnly();
    required($('generate').querySelector('span')).textContent = generating ? '生成中…' : selected ? '再生成一张' : '生成图片';
    $('stop').hidden = !generating;
    $('cover').disabled = busy || !selected || !imageReady || readOnly() || !cover || cover.disabled || cover.readOnly;
    $('insert').disabled = busy || !selected || !imageReady || readOnly();
    $('alt').disabled = busy; $('caption').disabled = busy;
    $('gallery').querySelectorAll('button').forEach(button => { button.disabled = busy; });
    $('canvas').setAttribute('aria-busy', String(generating));
    dialog.classList.toggle('is-generating', generating);
    $('loading').hidden = !generating;
    $('placeholder').hidden = !generating && !!selected;
    preview.hidden = generating || !selected;
    $('stage-title').textContent = generating ? '正在生成图片' : '生成文章配图';
    $('stage-detail').textContent = generating ? '模型正在处理构图与细节，完成后会自动保存。' : '从构图、光线和细节开始。';
    form.setAttribute('aria-busy', String(busy || loading));
  }
  async function json(response:Response):Promise<unknown> {if(response.status===401||response.status===403)retire();const data=await writingJSON(response);if(!alive())throw new DOMException('页面已失效','AbortError');return data;}
  const get=(url:string,signal:AbortSignal)=>fetch(url,{credentials:'same-origin',redirect:'error',cache:'no-store',signal,headers:{Accept:'application/json'}});
  async function session(signal:AbortSignal){if(!alive())throw new DOMException('页面已失效','AbortError');const data=await json(await get('/api/v1/admin/session',signal));try{return writingSession(data,owner,login,csrf);}catch(error){retire();throw error;}}
  function remember() {
    if (!provider.value) return;
    preference = {provider_id: provider.value, models: {...preference?.models, [provider.value]: model.value}};
    try { localStorage.setItem(preferenceKey, JSON.stringify(preference)); } catch { /* Use the current page selection. */ }
  }
  function modelOptions() {
    const config = imageProfile(currentProvider()?.protocol, model.value);
    const prior = $('ratio').value;
    const labels: Record<string,string> = {'1:1': '1:1 · 方形', '3:2': '3:2 · 横幅', '2:3': '2:3 · 竖幅', '16:9': '16:9 · 宽屏', '9:16': '9:16 · 长图'};
    $('ratio').replaceChildren(...config.ratios.map(ratio => option(ratio, labels[ratio] || ratio)));
    $('ratio').value = config.ratios.includes(prior) ? prior : mode === 'cover' ? (config.ratios.includes('16:9') ? '16:9' : '3:2') : '1:1';
    $('quality-field').hidden = !config.quality;
    $('resolution-field').hidden = config.resolutions.length === 1;
    const resolution = $('resolution').value;
    $('resolution').replaceChildren(...config.resolutions.map(value => option(value, value || '模型默认')));
    if (config.resolutions.includes(resolution)) $('resolution').value = resolution;
    $('model-help').textContent = currentProvider()?.protocol === 'seedream' ? '使用已开通的 Seedream 模型；保留服务商水印。' : config.quality ? '画质越高，等待时间可能越长。' : '分辨率以模型能力为准；不确定时选择“模型默认”。';
    remember(); controls();
  }
  function selectProvider(preferred = '') {
    const current = currentProvider();
    model.replaceChildren(...(current?.models || []).map(id => option(id, id)));
    const candidate = preferred || preference.models?.[current?.id || ''] || current?.default_model;
    if (candidate && current?.models.includes(candidate)) model.value = candidate;
    modelOptions();
  }
  async function loadConnections() {
    if (generating || applying) return;
    loadController?.abort(); const controller = new AbortController(); loadController = controller;
    loading = true; controls();
    const timer = setTimeout(() => controller.abort(), 20000);
    try {
      const data = writingConnections(await json(await get('/api/v1/admin/ai/connections', controller.signal)));
      if (!Array.isArray(data.providers) || data.providers.some(item => typeof item.id !== 'string' || !Array.isArray(item.models))) throw new Error('接口列表无效，请在设置中检查配置。');
      if (controller.signal.aborted) return;
      connections = {...data, providers: data.providers.filter(item => IMAGE_PROTOCOLS.includes(item.protocol))};
      const choice = chooseImageConnection(connections, preference);
      provider.replaceChildren(...connections.providers.map(item => option(item.id, item.name)));
      if (!connections.providers.length) provider.append(option('', '尚未配置生图接口'));
      provider.value = choice.provider_id; selectProvider(choice.model);
      $('empty-config').hidden = connections.providers.length > 0;
      if (!connections.providers.length) say('先添加生图接口，再回到这里点击刷新。');
    } catch (cause) { const error=writingFailure(cause); if(loadController!==controller)return; if (dialog.open) say(controller.signal.aborted ? '接口加载已取消或超时，点击刷新重试。' : error.message, 'error'); }
    finally { clearTimeout(timer); if (loadController === controller) { loadController = null; loading = false; controls(); } }
  }
  function renderGallery() {
    $('gallery').replaceChildren(...images.map(item => {
      const button = document.createElement('button'), thumbnail = document.createElement('img');
      button.type = 'button'; button.setAttribute('aria-label', `选择图片：${item.title || item.filename}`); button.setAttribute('aria-pressed', String(selected?.id === item.id));
      thumbnail.src = mediaURL(item); thumbnail.alt = ''; thumbnail.loading = 'lazy'; button.append(thumbnail);
      button.addEventListener('click', () => { if (!generating && !applying) selectImage(item); }); return button;
    }));
  }
  function selectImage(item: ImageDraft) {
    if (!mediaURL(item)) throw new Error('图片尚未保存为有效的站内媒体。');
    if (selected) { selected.editAlt = $('alt').value; selected.editCaption = $('caption').value; }
    selected = item; imageReady = false;
    preview.onload = () => { if (selected?.id === item.id) { imageReady = true; controls(); } };
    preview.onerror = () => { if (selected?.id === item.id) { imageReady = false; say('图片预览加载失败，可到媒体库检查资源后重新选择。', 'error'); controls(); } };
    preview.src = mediaURL(item);
    $('alt').value = item.editAlt ?? item.alt_text ?? ''; $('caption').value = item.editCaption ?? item.caption ?? '';
    $('info').textContent = `${item.width} × ${item.height} · 已保存到媒体库`;
    renderGallery(); controls();
  }
  function receiveImage(value: unknown) {
    const item = aiMedia(value);
    if (!mediaURL(item)) throw new Error('服务端返回的图片无效，请到媒体库检查。');
    const known = images.find(image => image.id === item.id);
    if (known) selectImage(known);
    else { images.push(item); if (images.length > 12) images.shift(); selectImage(item); }
  }
  function stop() {
    if (!generating) return;
    generationController?.abort();
    say('已停止等待。服务商可能仍在处理；已保存的图片可在媒体库查看。');
  }
  async function generate(event: Event) {
    event.preventDefault();
    if (generating || loading || applying || readOnly() || !form.reportValidity()) return;
    const current = currentProvider();
    if (!current || !current.models.includes(model.value)) return;
    const controller = new AbortController(); generationController = controller;
    generating = true; started = Date.now(); $('phase').textContent = '正在连接生图服务'; $('elapsed').textContent = '0 秒'; controls();
    if (window.matchMedia('(max-width: 620px)').matches) $('canvas').scrollIntoView({block: 'center', behavior: 'instant'});
    const tick = () => { $('elapsed').textContent = `${Math.floor((Date.now() - started) / 1000)} 秒`; };
    elapsedTimer = setInterval(tick, 1000);
    const timeout = setTimeout(() => controller.abort('timeout'), 490000);
    say('正在生成，完成后可选择封面或正文用途。');
    try {
      const csrf = await session(controller.signal), config = imageProfile(current.protocol, model.value);
      const payload: {provider_id:string;model:string;prompt:string;aspect_ratio:string;title:string;quality?:string;resolution?:string} = {provider_id: current.id, model: model.value, prompt: $('prompt').value.trim(), aspect_ratio: $('ratio').value, title: String(field('title')?.value || 'AI 配图').slice(0, 160)};
      if (config.quality) payload.quality = $('quality').value;
      if (config.resolutions.length > 1) payload.resolution = $('resolution').value;
      const response = await fetch('/api/v1/admin/ai/images', {method: 'POST', credentials: 'same-origin', redirect: 'error', cache: 'no-store', signal: controller.signal, headers: {'Content-Type': 'application/json', Accept: 'text/event-stream', 'X-CSRF-Token': csrf}, body: JSON.stringify(payload)});
      if (!response.ok) await json(response);
      if (!response.headers.get('Content-Type')?.includes('text/event-stream')) throw new Error('生图服务未返回预期的数据流。');
      let count = 0; const received: AIMedia[]=[];
      await core.readStream(response, (name, value) => {
        if(controller.signal.aborted||!alive())throw new DOMException('生成已取消','AbortError');const data=record(value);
        if (name === 'ready' || name === 'progress') $('phase').textContent = typeof data.message === 'string' ? data.message : '正在生成画面';
        if (name === 'image') { const item=aiMedia(data);received.push(item);receiveImage(item); count++; }
        if (name === 'error') throw new Error(typeof data.message === 'string' ? data.message : '生成失败，请检查接口设置。');
        if (name === 'done') {
          const items=imageDone(data,payload.model,received);if(!count){items.forEach(receiveImage);count=items.length;}
          if (!count) throw new Error('服务商没有返回图片，请检查模型与画面描述。');
          say('图片已保存。检查画面后，可设为封面或填写图片说明并插入正文。', 'success');
        }
      });
    } catch (cause) { const error=writingFailure(cause);
      if (!alive())return;
      if (controller.signal.aborted) say(controller.signal.reason === 'timeout' ? '等待超时，请在服务商控制台核对任务；已保存的图片可在媒体库查看。' : '已停止等待。服务商可能仍在处理；已保存的图片可在媒体库查看。');
      else say(error.message.includes('正文') ? '生成连接中断；已保存的图片可在媒体库查看，本次不会自动重试。' : error.message, 'error');
    } finally { clearInterval(elapsedTimer); clearTimeout(timeout); generating = false; generationController = null; controls(); }
  }
  async function apply(target: 'body'|'cover') {
    if (!selected || !imageReady || generating || applying || readOnly()) return;
    if (target === 'body' && !$('alt').value.trim()) { $('alt').setCustomValidity('请填写简短的图片内容说明'); $('alt').reportValidity(); return; }
    applying = true; controls();
    const item = selected, controller = new AbortController(); applyController = controller;
    const timer = setTimeout(() => controller.abort(), 20000);
    try {
      await session(controller.signal);
      const latest = aiMedia(await json(await get(`/api/v1/admin/media/${item.id}`, controller.signal)),item.id);
      if(controller.signal.aborted||!dialog.open||!alive())return;
      const url = mediaURL(latest);
      if (!url) throw new Error('图片已移除或不可用，请从媒体库重新选择。');
      if (readOnly()) throw new Error('编辑器正在保存或已只读，请稍后重试。');
      if (target === 'cover') {
        if (!cover || cover.disabled || cover.readOnly) throw new Error('当前不能修改封面。');
        if (cover.value !== coverSnapshot) throw new Error('封面已变化，请关闭生图窗口，核对后重新打开。');
        cover.value = url; cover.dispatchEvent(new Event('input', {bubbles: true}));
        returnFocus = false; dialog.close(); cover.focus();
      } else {
        if (!selection || source.value !== selection.value) throw new Error('正文已变化，请关闭窗口，在正文重新选择插入位置后打开；生成的图片会保留。');
        const figure = document.createElement('figure'), img = document.createElement('img');
        img.setAttribute('src', url); img.setAttribute('alt', $('alt').value.trim());
        const width = Math.min(1200, latest.width);
        img.setAttribute('width', String(width)); img.setAttribute('height', String(Math.max(1, Math.round(width * latest.height / latest.width))));
        figure.append(img);
        if ($('caption').value.trim()) { const caption = document.createElement('figcaption'); caption.textContent = $('caption').value.trim(); figure.append(caption); }
        blockEditor.insert('\n' + figure.outerHTML + '\n', selection);
        returnFocus = false; dialog.close();
      }
      say(target === 'cover' ? '已设为封面，请保存文章。' : '图片已插入正文，请保存文章。', 'success');
    } catch (cause) { const error=writingFailure(cause); if (dialog.open) say(controller.signal.aborted ? '检查图片超时或已取消，请重试。' : error.message, 'error'); }
    finally { clearTimeout(timer); applying = false; applyController = null; controls(); }
  }
  function open(button: HTMLButtonElement) {
    if (readOnly() || dialog.open) return;
    launcher = button; returnFocus = true; mode = button.dataset.aiImageOpen || 'body';
    selection = blockEditor.capture() || {start: source.selectionStart, end: source.selectionEnd, value: source.value};
    coverSnapshot = cover?.value || '';
    if (button.hasAttribute('data-ai-image-suggestion')) {
      const suggestion = document.querySelector<HTMLTextAreaElement>('[data-ai-meta-cover]')?.value;
      if (suggestion) $('prompt').value = suggestion;
    }
    if (!generating) { $('ratio').replaceChildren(); say(selected ? '图片已保留，可再次使用或继续生成。' : '选择接口并描述你想要的画面。'); }
    button.setAttribute('aria-expanded', 'true'); dialog.showModal(); controls(); void loadConnections();
  }
  document.querySelectorAll<HTMLButtonElement>('[data-ai-image-open]').forEach(button => button.addEventListener('click', () => open(button)));
  $('close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('cancel', () => { stop(); });
  dialog.addEventListener('close', () => { stop(); loadController?.abort(); applyController?.abort(); launcher?.setAttribute('aria-expanded', 'false'); if (returnFocus) launcher?.focus(); });
  form.addEventListener('submit', generate);
  $('stop').addEventListener('click', stop); $('refresh').addEventListener('click', loadConnections);
  provider.addEventListener('change', () => selectProvider()); model.addEventListener('change', modelOptions);
  $('alt').addEventListener('input', () => $('alt').setCustomValidity(''));
  $('cover').addEventListener('click', () => apply('cover')); $('insert').addEventListener('click', () => apply('body'));
  $('from-title').addEventListener('click', () => {
    const title = field('title')?.value.trim();
    if (!title) { say('先为文章填写标题，或直接描述画面。'); return; }
    $('prompt').value = `为文章《${title}》创作一张配图。主体清晰，构图留白，不添加标题、文字或徽标。`;
    $('prompt').focus();
  });
  document.addEventListener('visibilitychange', () => dialog.classList.toggle('is-page-hidden', document.hidden));
  window.addEventListener('beforeunload', event => { if (generating || applying) { event.preventDefault(); event.returnValue = ''; } });
  window.addEventListener('pagehide', () => { generationController?.abort(); loadController?.abort(); applyController?.abort(); clearInterval(elapsedTimer); });
  window.ChunnelAIImagesReady = true;
})();
