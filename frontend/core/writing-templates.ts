import { required } from './ai-writing-dom';
import type {} from '../shared/account-types';
import { writingJSON,writingFailure,writingSession,writingPrompts } from '../shared/ai-writing-data';
import type { WritingPrompt } from '../shared/ai-writing-data';
import { Mutation,bytes,MAX_IMPORT,templateReceipt,templateList,templateDetail,templateExport,importPreview,savedTemplate } from '../shared/writing-template-data';
import type { Template,TemplateSummary,TemplateCommand,Definition,Revision } from '../shared/writing-template-data';
type Pending=Mutation&{command:TemplateCommand;draft:Definition};
type Fields={'new':HTMLButtonElement;'count':HTMLElement;'reload':HTMLButtonElement;'list':HTMLElement;'form':HTMLFormElement;'heading':HTMLElement;'version':HTMLElement;'identity':HTMLElement;'fields':HTMLFieldSetElement;'title':HTMLInputElement;'base':HTMLSelectElement;'description':HTMLTextAreaElement;'instruction':HTMLTextAreaElement;'example':HTMLTextAreaElement;'save':HTMLButtonElement;'archive':HTMLButtonElement;'draft-export':HTMLButtonElement;'history':HTMLDetailsElement;'history-select':HTMLSelectElement;'history-preview':HTMLElement;'export':HTMLButtonElement;'restore':HTMLButtonElement;'import-file':HTMLInputElement;'import-document':HTMLTextAreaElement;'import-preview':HTMLButtonElement;'import-review':HTMLElement;'import-summary':HTMLElement;'import-confirm':HTMLInputElement;'import-save':HTMLButtonElement;'status':HTMLElement;'pending':HTMLElement;'retry':HTMLButtonElement;'dismiss':HTMLButtonElement};
(() => {
if(typeof document==='undefined')return;
  const rootNode = document.querySelector<HTMLElement>('[data-writing-templates]');
  if (!rootNode) return;const root=rootNode;
  const $ = <K extends keyof Fields>(name: K): Fields[K] => required(root.querySelector<Fields[K]>(`[data-wt-${name}]`));
  const form = $('form'), fields = $('fields'), readOnly = fields.disabled;
  const names = ['title', 'description', 'instruction', 'example'] as const;
  const node = <K extends keyof HTMLElementTagNameMap>(tag: K, text = '') => { const el = document.createElement(tag); el.textContent = text; return el; };
  const option = (value: string | number, label: string) => { const el = node('option', label); el.value = String(value); return el; };
  let current: Template | null = null, entries: TemplateSummary[] = [], builtins: WritingPrompt[] = [], ready = false, busy = false, dirty = false, pending: Pending | null = null, preview: Awaited<ReturnType<typeof importPreview>> | null = null, reloadNeeded = false;
  const currentVersion = () => current?.revisions.at(-1);
  const say = (message: string, tone = '') => { $('status').textContent = message; $('status').dataset.tone = tone; };
  const definition = (): Definition => ({base_prompt: $('base').value,title: $('title').value.trim(),description:$('description').value,instruction:$('instruction').value,example:$('example').value});
  function controls() {
    if(retired){root.querySelectorAll<HTMLInputElement|HTMLButtonElement|HTMLSelectElement|HTMLTextAreaElement|HTMLFieldSetElement>('input,button,select,textarea,fieldset').forEach(input=>{input.disabled=true;});$('pending').hidden=true;return;}
    const blocked = busy || !!pending, writable = !readOnly && ready && !blocked && !reloadNeeded;
    fields.disabled = !writable || !!currentVersion()?.archived;
    $('save').disabled = !writable || !!currentVersion()?.archived || (!dirty && !!current);
    $('archive').disabled = !writable || !current;
    $('archive').textContent = currentVersion()?.archived ? '重新启用' : '归档';
    $('new').disabled = readOnly || !ready || blocked;
    $('reload').disabled = busy;
    $('restore').disabled = !writable || !current;
    $('export').disabled = busy || !current;
    $('retry').disabled = busy;
    $('dismiss').disabled = busy;
    $('pending').hidden = !pending;
    $('import-file').disabled = !writable;
    $('import-document').disabled = !writable;
    $('import-preview').disabled = !writable;
    $('import-confirm').disabled = !writable;
    $('import-save').disabled = !writable || !preview || !$('import-confirm').checked;
    form.setAttribute('aria-busy', String(busy));
    for (const button of $('list').querySelectorAll('button')) button.disabled = blocked;
  }
  const confirmDiscard = () => !dirty || window.confirm('当前模板有未保存的输入。继续将放弃这些修改；可先下载当前输入。');
  function sourceLabel(revision: Revision) {
    const source = revision.source;
    const labels: Record<string,string> = {manual: '手动保存', archive: '归档', activate: '重新启用', history: '恢复历史', import: '文件导入'};
    return `${labels[source.kind] || source.kind}${source.template_id ? ` · ${source.template_id} / v${source.version}` : ''}${source.sha256 ? `\n来源文件 SHA-256：${source.sha256}` : ''}`;
  }
  function renderHistory() {
    const version = current?.revisions.find(v => v.version === Number($('history-select').value));
    if (!version) return;
    const d = version.definition;
    $('history-preview').textContent = `${d.title} · v${version.version} · ${version.archived ? '归档' : '启用'}\n${version.at} · ${version.actor}\n${sourceLabel(version)}\n基础模式：${d.base_prompt}\n\n${d.description}\n\n${d.instruction}\n\n示例素材：\n${d.example}`;
  }
  function fill(item: Template | null) {
    current = item;
    const revision = currentVersion(), d = revision?.definition || {base_prompt: builtins[0]?.id || '', title: '', description: '', instruction: builtins[0]?.instruction || '', example: ''};
    for (const name of names) $(name).value = d[name];
    $('base').value = d.base_prompt;
    $('heading').textContent = d.title || '新模板';
    $('version').textContent = revision ? `v${revision.version} · ${revision.archived ? '已归档' : '启用中'}` : '尚未保存';
    $('identity').textContent = item ? `${item.id}\n${required(revision).at} · ${required(revision).actor}` : '保存后，作者可在 AI 编辑器的「团队模板」分类中选择。';
    $('history').hidden = !item;
    $('history-select').replaceChildren(...(item?.revisions || []).slice().reverse().map(v => option(v.version, `v${v.version} · ${v.definition.title} · ${v.archived ? '归档' : '启用'} · ${v.at}`)));
    renderHistory(); dirty = false; reloadNeeded = false; renderList(); controls();
  }
  function renderList() {
    $('count').textContent = `${entries.length} / 100 个模板`;
    $('list').replaceChildren(...entries.map(item => {
      const button = node('button'); button.type = 'button'; button.className = 'wt-list-item'; button.dataset.wtId = item.id;
      button.setAttribute('aria-pressed', String(current?.id === item.id));
      button.append(node('strong', item.title), node('small', `v${item.version} · ${item.archived ? '已归档' : '启用中'}`));
      button.addEventListener('click', () => { if (!busy && !pending && confirmDiscard()) void loadOne(item.id); });
      return button;
    }));
    if (!entries.length) { const empty = node('p', '还没有团队模板。先保存一份常用写作提示词。'); empty.className = 'wt-empty'; $('list').append(empty); }
  }
  const owner=root.dataset.userId||'',login=root.dataset.username||'',csrf=root.dataset.csrf||'';let retired=false;const requests=new Set<AbortController>();
  const alive=()=>!retired&&root.isConnected;
  function retire(){if(retired)return;retired=true;for(const controller of requests)controller.abort();entries=[];builtins=[];current=null;preview=null;pending=null;dirty=false;ready=false;$('list').replaceChildren();$('history-preview').textContent='';$('history-select').replaceChildren();$('identity').textContent='';$('import-summary').textContent='';$('import-document').value='';$('import-file').value='';for(const name of names)$(name).value='';say('登录状态或页面已变化，请刷新后从模板历史核对操作结果。','error');controls();}
  document.addEventListener('chunnel:session',event=>{const session=event.detail;if(session?.authenticated!==true||String(session.id)!==owner||session.csrf_token!==csrf)retire();});
  document.addEventListener('chunnel:admin-before-replace',event=>{if(event.detail.regions.some(region=>region.contains(root)))retire();});
  window.addEventListener('pagehide',retire);window.addEventListener('pageshow',event=>{if(event.persisted)retire();});
  async function decode(response:Response):Promise<unknown>{if(response.status===401||response.status===403)retire();return writingJSON(response);}
  async function request(path:string, mutation:Mutation|null = null, body:unknown = null):Promise<unknown> {
    if(!alive())throw new DOMException('页面已失效','AbortError');
    const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),30000);requests.add(controller);
    const options:RequestInit={credentials:'same-origin',redirect:'error',cache:'no-store',signal:controller.signal,headers:{Accept:'application/json'}};
    try {
      let token:string;
      try{const data=await decode(await fetch('/api/v1/admin/session',options));try{token=writingSession(data,owner,login,csrf,!!mutation||!!body);}catch(error){retire();throw error;}}
      catch(cause){const error=writingFailure(cause);error.beforeSend=true;error.definite=false;throw error;}
      if(!alive()||controller.signal.aborted)throw new DOMException('页面已失效','AbortError');
      if(mutation||body){options.method='POST';options.headers={Accept:'application/json','Content-Type':'application/json','X-CSRF-Token':token,...(mutation?{'Idempotency-Key':mutation.key}:{})};options.body=mutation?mutation.body:JSON.stringify(body);}
      const data=await decode(await fetch('/api/v1/admin/ai/'+path,options));
      if(!alive()||controller.signal.aborted)throw new DOMException('页面已失效','AbortError');return data;
    }finally{requests.delete(controller);clearTimeout(timer);}
  }
  async function fetchList() {
    const data = await request('templates');
    entries = templateList(data); renderList();
  }
  async function loadOne(id: string) {
    busy = true; controls();
    try { fill(templateDetail(await request(`templates/${encodeURIComponent(id)}`),id)); say('已载入保存版本，可编辑或查看历史。'); }
    catch (cause) { const error=writingFailure(cause);if(!alive())return; say(error.message, 'error'); }
    finally { busy = false; controls(); }
  }
  async function reload() {
    if (busy) return;
    busy = true; controls();
    try {
      if (!builtins.length) {
        const data = await request('prompts');
        builtins = writingPrompts(data).filter(p => !p.id.startsWith('team:'));
        if (!builtins.length) throw new Error('基础写作模式暂不可用。');
        $('base').replaceChildren(...builtins.map(p => option(p.id, `${p.title}${p.needs_draft ? '（需要原稿）' : ''}`)));
      }
      await fetchList();
      if (!ready) fill(null);
      ready = true;
      say(pending ? '列表已刷新；原请求仍待核对。' : '列表已刷新。点击模板载入保存版本；当前输入保留。');
    } catch (cause) { const error=writingFailure(cause);if(!alive())return; say(error.message, 'error'); }
    finally { busy = false; controls(); }
  }
  async function submit(body?: TemplateCommand) {
    if (busy || readOnly || (!pending && (!ready || reloadNeeded))) return;
    if (!pending) {if(!body)return;pending=Object.assign(new Mutation(body,crypto.randomUUID()),{command:structuredClone(body),draft:definition()});}
    const attempt = pending;
    busy = true; controls(); say('正在保存团队模板…');
    try {
      const result = templateReceipt(await request('templates', attempt),attempt.command);
      if(!alive())return;let preserve=JSON.stringify(definition())!==JSON.stringify(attempt.draft);
      pending = null; dirty = preserve; reloadNeeded = true;
      try { await fetchList(); const saved=savedTemplate(await request(`templates/${result.id}`),result,attempt.command);preserve=JSON.stringify(definition())!==JSON.stringify(attempt.draft);dirty=preserve;if(!preserve)fill(saved); }
      catch { say(`已确认保存 v${result.version}，但读取当前模板失败。请刷新列表并重新选择模板。`, 'error'); return; }
      if (attempt.command.action === 'import' && !preserve) { $('import-document').value = ''; $('import-file').value = ''; invalidatePreview(); }
      if(preserve){say(`已确认保存 v${result.version}。后续输入仍保留；可先下载输入，再从列表载入保存版本。`,'success');return;}
      say(`${result.replayed ? '已核对原请求' : '已保存'} v${result.version}。${required(currentVersion()).version !== result.version ? `当前已是 v${required(currentVersion()).version}，可查看历史记录。` : '作者可刷新模板列表后选用。'}`, 'success');
    } catch (cause) { const error=writingFailure(cause);if(!alive())return;
      if (!attempt.retain(error)) pending = null;
      say(`${error.message}${pending ? ' 原请求已保留，请点击下方按钮核对；当前输入保留。' : ' 当前输入保留；版本冲突时请先下载输入，再从列表载入保存版本。'}`, 'error');
    } finally { busy = false; controls(); }
  }
  function download(raw: string, filename: string) {
    const url = URL.createObjectURL(new Blob([raw], {type: 'application/json;charset=utf-8'}));
    const link = node('a'); link.href = url; link.download = filename; document.body.append(link); link.click(); link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
  }
  function invalidatePreview() { preview = null; $('import-confirm').checked = false; $('import-review').hidden = true; controls(); }
  form.addEventListener('input', () => { dirty = true; say('当前模板有未保存的输入。'); controls(); });
  form.addEventListener('submit', event => { event.preventDefault(); if (pending || !form.reportValidity()) return; void submit({action: 'save', id: current?.id || '', expected_version: currentVersion()?.version || 0, definition: definition()}); });
  $('new').addEventListener('click', () => { if (!busy && !pending && confirmDiscard()) { fill(null); say('填写新模板后保存。'); $('title').focus(); } });
  $('reload').addEventListener('click', reload);
  $('archive').addEventListener('click', () => {
    if (busy || pending || !current || !confirmDiscard()) return;
    const action = required(currentVersion()).archived ? 'activate' : 'archive';
    if (window.confirm(action === 'archive' ? '归档后，作者将不能再选用此模板。确认归档？' : '确认将此模板重新启用？')) void submit({action, id: current.id, expected_version: required(currentVersion()).version});
  });
  $('history-select').addEventListener('change', renderHistory);
  $('restore').addEventListener('click', () => {
    if (busy || pending || !current || !confirmDiscard()) return;
    const restoreVersion = Number($('history-select').value);
    if (window.confirm(`将 v${restoreVersion} 的内容保存为新的启用版本，保留全部历史。确认继续？`)) void submit({action: 'restore', id: current.id, expected_version: required(currentVersion()).version, restore_version: restoreVersion});
  });
  $('export').addEventListener('click', async () => {
    if (busy || !current) return;
    busy = true; controls();
    try { const id=current.id,version=Number($('history-select').value);const data = templateExport(await request(`templates/${id}/export?version=${version}`),id,version); download(JSON.stringify(data, null, 2), `writing-template-${data.template_id}-v${data.version}.json`); say('已导出所选版本。'); }
    catch (cause) { const error=writingFailure(cause);if(!alive())return; say(error.message, 'error'); }
    finally { busy = false; controls(); }
  });
  $('draft-export').addEventListener('click', () => download(JSON.stringify({kind: 'newpress.writing-template-input', template_id: current?.id || '', expected_version: currentVersion()?.version || 0, definition: definition()}, null, 2), 'writing-template-unsaved-input.json'));
  $('import-document').addEventListener('input', invalidatePreview);
  $('import-file').addEventListener('change', async () => {
    const file = $('import-file').files?.[0]; invalidatePreview();
    if (!file || busy || pending) return;
    if (file.size > MAX_IMPORT) { say('导入文件超过 128 KiB。', 'error'); return; }
    busy = true; controls();
    try {const prior=$('import-document').value;const text=await file.text();if(!alive()||$('import-file').files?.[0]!==file||$('import-document').value!==prior)return;$('import-document').value=text; say('文件已读取，请先预览。'); }
    catch { say('无法读取文件，请重新选择。', 'error'); }
    finally { busy = false; controls(); }
  });
  $('import-preview').addEventListener('click', async () => {
    if (busy || pending || readOnly) return;
    invalidatePreview();
    const raw = $('import-document').value;
    if (!raw || bytes(raw) > MAX_IMPORT) { say('请选择或粘贴不超过 128 KiB 的模板导出文件。', 'error'); return; }
    busy = true; controls();
    try {
      const data = await importPreview(await request('template-import-preview', null, {document:raw}),raw);
      if(!alive()||$('import-document').value!==raw)return;preview=data; const d=data.document.definition;
      $('import-summary').textContent = `${d.title}\n来源：${data.document.template_id} / v${data.document.version}\n基础模式：${d.base_prompt}\nSHA-256：${data.sha256}\n\n${d.description}\n\n${d.instruction}\n\n示例素材：\n${d.example}`;
      $('import-review').hidden = false; say('预览已就绪。核对内容并勾选确认后导入。');
    } catch (cause) { const error=writingFailure(cause);if(!alive())return; say(error.message, 'error'); }
    finally { busy = false; controls(); }
  });
  $('import-confirm').addEventListener('change', controls);
  $('import-save').addEventListener('click', () => {
    if (!preview || !$('import-confirm').checked || preview.raw !== $('import-document').value || busy || pending || !confirmDiscard()) return;
    void submit({action: 'import', document: preview.raw, import_sha256: preview.sha256, confirm_import: true});
  });
  $('retry').addEventListener('click', () => { if (pending) void submit(); });
  $('dismiss').addEventListener('click', () => {
    if (busy || !pending) return;
    if (window.confirm('原请求可能已经保存。结束核对后请先刷新列表并检查保存内容；再次保存会成为新的请求。确认结束核对？')) { pending = null; reloadNeeded = true; say('已结束本页核对。当前输入保留，可先下载输入，再刷新列表并选择模板。'); controls(); }
  });
  window.addEventListener('beforeunload', event => { if (dirty || pending) { event.preventDefault(); event.returnValue = ''; } });
  controls(); void reload();
})();
