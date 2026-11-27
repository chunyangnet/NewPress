import type { Savebar } from '../core/admin-types';
import {required,field as valueField,checked,pageRequests,failure} from './collection-runtime';
import {linkMetadata,linkReceipt} from './collection-data';
import type {LinkCommand} from './collection-data';
(() => {
  'use strict';
  const root = document.querySelector<HTMLElement>('[data-links-admin]');
  if (!root) return;
  const $ = <T extends Element = HTMLElement>(s: string, el: ParentNode = root): T => required<T>(el,s);
  const notice = $('[data-links-status]');
  let busy = false, dirty = false;
  const edits = new Map<HTMLFormElement, number>();
  const controllers = new Map<HTMLFormElement, Savebar>();
  const requests = pageRequests(Number(root.dataset.userId),()=>{root.querySelectorAll<HTMLButtonElement | HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('button,input,select,textarea').forEach(node=>node.disabled=true);notice.hidden=false;notice.textContent='登录状态已变化，请重新打开页面；当前输入仍保留。';});
  const deleteAttempts = new WeakMap<HTMLButtonElement, {signature:string;key:string}>();
  const mark = (form: HTMLFormElement | null) => { if (form) { const controller = controllers.get(form); if (controller && !controller.dirty()) edits.delete(form); else edits.set(form, (edits.get(form) || 0) + 1); controller?.refresh(); } dirty = edits.size > 0; };
  const show = (text: string) => { notice.hidden = false; notice.textContent = text; notice.scrollIntoView({ block: 'nearest' }); };
  const request = async (path: string, body: LinkCommand | {url: string}) => {
    try { return await requests.request(path,{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':root.dataset.csrf || ''},body:JSON.stringify({...body,expected_user_id:Number(root.dataset.userId)})}); }
    catch (problem) { const error=failure(problem); if (!requests.lost && error.status===409) { const box=$('[data-links-conflict]');box.hidden=false;$<HTMLAnchorElement>('a',box).href=location.href; } throw error; }
  };
  const perform = async (button: HTMLButtonElement, fn: () => Promise<void>, controller?: Savebar | null) => {
    if (busy || requests.lost || button.matches(':disabled')) return;
    if (controller && !controller.start()) return;
    busy = true; button.disabled = true; button.setAttribute('aria-busy', 'true');
    try { await fn(); } catch (problem) { if(requests.lost)return;const e=failure(problem);if (controller) controller.fail(e.message || '网络暂不可用，输入已保留。'); else show(e.message || '网络暂不可用，输入已保留。'); }
    finally { busy = false; if (requests.lost) return; if (controller) controller.refresh(true); else button.disabled = false; button.removeAttribute('aria-busy'); }
  };
  root.addEventListener('input', event => { mark(event.target instanceof Element ? event.target.closest<HTMLFormElement>('form') : null); });
  root.addEventListener('change', event => { mark(event.target instanceof Element ? event.target.closest<HTMLFormElement>('form') : null); });
  window.addEventListener('beforeunload', e => { if (dirty) { e.preventDefault(); e.returnValue = ''; } });
  root.querySelector<HTMLInputElement>('[data-links-preview-name]')?.addEventListener('input', e => { const name = root.querySelector('[data-links-live-name]'); if (name) name.textContent = (e.currentTarget as HTMLInputElement).value || '我的友链'; });
  const itemForm = root.querySelector<HTMLFormElement>('[data-links-form="save_item"]');
  const field = (name: string) => { if(!itemForm)throw new Error('缺少友链表单');return required<HTMLInputElement|HTMLTextAreaElement>(itemForm,`[name="${name}"]`); };
  const previewLogo = () => {
    if (!itemForm) return;
    const target = $('[data-links-logo-preview]');
    const logo = field('logo').value;
    const editor=target.closest<HTMLElement>('.links-logo-editor');
    if(editor)editor.hidden = !(logo || field('logo_url').value);
    target.replaceChildren();
    if (field('logo_url').value) {
      const img = document.createElement('img'); img.src = field('logo_url').value; img.alt = ''; target.append(img);
    } else if (logo) {
      const img = document.createElement('img'); img.src = 'data:image/png;base64,' + logo; img.alt = ''; target.append(img);
    } else { target.textContent = (field('name').value.trim() || '站').slice(0, 1); }
  };
  previewLogo();
  root.querySelector<HTMLButtonElement>('[data-links-logo-clear]')?.addEventListener('click', () => {
    field('logo').value = ''; field('logo_url').value = '';
    field('logo_url').dispatchEvent(new Event('input', { bubbles: true })); mark(itemForm); previewLogo();
  });
  itemForm?.querySelector('[name=logo_url]')?.addEventListener('input', () => { field('logo').value = ''; previewLogo(); });
  root.querySelector<HTMLButtonElement>('[data-links-fetch]')?.addEventListener('click', e => perform(e.currentTarget as HTMLButtonElement, async () => {
    const url = field('url').value.trim(); if (!url) throw new Error('先填写站点域名。');
    show('正在读取站点信息…');
    const data = linkMetadata(await request('/api/v1/admin/link-metadata', { url }));
    // A slow response must not apply to a different address or overwrite new typing.
    if (field('url').value.trim() !== url) { show('域名已经变化，已忽略上一次提取结果。'); return; }
    for (const [name, value] of [['name', data.title], ['description', data.description], ['logo', data.logo]] as const) if (!field(name).value.trim() && value && !(name === 'logo' && field('logo_url').value)) field(name).value = value;
    mark(itemForm); previewLogo(); show(data.notice || '信息已填入草稿，确认并修改后再保存。');
  }));
  root.querySelectorAll<HTMLFormElement>('[data-links-form]').forEach(form => {
    const controller = window.ChunnelAdminUI?.savebar(form, { allowClean: form.dataset.linksForm === 'create', onDiscard: () => { edits.delete(form); dirty = edits.size > 0; previewLogo(); } });
    if (controller) controllers.set(form, controller);
    let lastPayload = '', key = '';
    form.addEventListener('submit', e => {
      e.preventDefault(); const button = e.submitter instanceof HTMLButtonElement ? e.submitter : $<HTMLButtonElement>('button[type="submit"]', form);
      perform(button, async () => {
        const f = (name: string) => valueField(form,name);
        const action = form.dataset.linksForm || '';
        const body: LinkCommand = { action, version: Number(root.dataset.version) };
        let slug = root.dataset.slug || '';
        if (action === 'save_item') body.item = { id: Number(form.dataset.itemId), name: f('name').value.trim(), url: f('url').value.trim(), description: f('description').value.trim(), category: f('category').value.trim(), logo: f('logo').value, logo_url: f('logo_url').value, order: Number(f('order').value), enabled: checked(form,'enabled'), featured: checked(form,'featured') };
        else { slug = f('slug').value; body.settings = { name: f('name').value.trim(), description: f('description').value.trim(), style: f('style').value, accent: f('accent').value, enabled: checked(form,'enabled'), search: checked(form,'search'), switcher: checked(form,'switcher'), new_tab: checked(form,'new_tab') }; }
        const payload = JSON.stringify(body); if (lastPayload !== payload) { key = crypto.randomUUID(); lastPayload = payload; }
        const revision = edits.get(form) || 0, snapshot = controller?.snapshot();
        const data = linkReceipt(await request('/api/v1/admin/link-pages/' + encodeURIComponent(slug), { ...body, key }),slug,body);
        root.dataset.version = String(data.version);
        controller?.success(snapshot);
        if (controller?.dirty()) edits.set(form,(edits.get(form)||0)+1);
        else if (controller || (edits.get(form) || 0) === revision) edits.delete(form);
        dirty = edits.size > 0;
        // Keep the other editor's unsaved values when saving page settings.
        if (action === 'configure') { if (!controller) show('页面设置已保存。'); return; }
        if (action === 'save_item' && dirty) { form.dataset.itemId = String(data.item_id); show('友链已保存；其他未保存的输入仍保留。完成后刷新可更新下方清单。'); return; }
        if (action === 'save_item') { history.replaceState(null, '', '/admin/features/links/?edit=' + encodeURIComponent(data.slug) + '#link-editor'); location.reload(); return; }
        location.assign('/admin/features/links/?edit=' + encodeURIComponent(data.slug) + (action === 'save_item' ? '#link-editor' : ''));
      }, controller);
    });
  });
  root.querySelectorAll<HTMLButtonElement>('[data-links-delete]').forEach(button => button.addEventListener('click', () => {
    if (dirty) { show('请先保存表单中的修改，再删除友链。'); return; }
    if (!confirm('删除“' + button.dataset.name + '”？')) return;
    perform(button, async () => {
      const slug=root.dataset.slug||'',body:LinkCommand={action:'delete_item',item_id:Number(button.dataset.linksDelete),version:Number(root.dataset.version)};
      const signature=JSON.stringify(body);let pending=deleteAttempts.get(button);if(!pending||pending.signature!==signature){pending={signature,key:crypto.randomUUID()};deleteAttempts.set(button,pending);}
      linkReceipt(await request('/api/v1/admin/link-pages/'+encodeURIComponent(slug),{...body,key:pending.key}),slug,body);deleteAttempts.delete(button);dirty=false;location.reload();
    });
  }));
})();
