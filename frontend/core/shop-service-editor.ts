import type { shopServices } from '../shared/shop-services';
import { serviceConfig, serviceLibrary, collectServices, ServiceInputError } from '../shared/shop-service-data';
import type { ServiceConfig, ServiceModule, ServiceLibrary, ServiceEditorOptions, ServiceProduct } from '../shared/shop-service-types';

export interface ShopServiceEditor { reset(product: ServiceProduct): void; refreshPreview(): void; collect(): ServiceConfig | null; destroy(): void }
const editors = new WeakMap<HTMLElement, ShopServiceEditor>();
  const esc = (value: unknown) => String(value ?? '').replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char] || char));
  const icon = (name: string) => `<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#icon-${esc(name)}"></use></svg>`;
  const links = [['', '不跳转'], ['#shop-product-description', '商品详情'], ['#shop-product-reviews', '买家评价'], ['#shop-product-specifications', '商品参数'], ['#shop-product-rules', '购买须知'], ['/shop/orders/', '我的订单']];
  const tokens = ['结算货币', '运费', '成团人数', '拼团时限', '发售时间', '尾款截止', '分期期数', '付款间隔', '每期费率', '开奖时间', '中奖概率', '中奖名额'];
  export function create(options: ServiceEditorOptions): ShopServiceEditor {
    const root = options.root;
    const shared: typeof shopServices = window.NewPressShopServices;
    editors.get(root)?.destroy();
    const lifecycle = new AbortController();
    let suspended = false;
    const $ = <T extends HTMLElement = HTMLElement>(selector: string): T => {
      const element = root.querySelector<T>(selector);
      if (!element) throw new Error('缺少服务说明编辑控件：' + selector);
      return element;
    };
    let config: Required<ServiceConfig> = { mode: 'auto', items: [] };
    let category = '', query = '', opened = -1, initialized = false;
    let libraryInput: unknown, parsedLibrary: ServiceLibrary | undefined;
    const library = () => {
      const input = options.getLibrary();
      if (!parsedLibrary || input !== libraryInput) { parsedLibrary = serviceLibrary(input); libraryInput = input; }
      return parsedLibrary;
    };
    const locked = () => suspended || lifecycle.signal.aborted || options.isLocked();
    const resolve = (items: ServiceModule[]) => { const product = options.getProduct(); return product ? shared.resolve({ ...product, services: { mode: 'custom', items } }, options.getContext()) : items; };
    const feedback = (text: string) => { $('[data-service-feedback]').textContent = text; };
    function preview() {
      const product = options.getProduct();
      if (!product || suspended || lifecycle.signal.aborted) return;
      const items = shared.resolve({ ...product, services: config }, options.getContext()), host = $('[data-service-preview]');
      $('[data-service-preview-count]').textContent = config.mode === 'hidden' ? '已关闭' : `${items.length} 项`;
      host.dataset.columns = String(Math.min(3, items.length));
      host.innerHTML = items.length ? items.map(item => `<div class="sa-service-preview-item">${icon(item.icon)}<b>${esc(item.title || '填写标题')}</b><small>${esc(item.description || '')}</small></div>`).join('') : `<p class="sa-service-empty">${config.mode === 'hidden' ? '商品页不显示这块服务说明。' : '勾选下方模板，或添加自己的说明。'}</p>`;
    }
    function renderLibrary() {
      const source = library(), selected = new Set(config.items.map(item => item.template)), groups = [...new Set(source.templates.map(item => item.category))];
      $('[data-service-categories]').innerHTML = ['', ...groups].map(value => `<button type="button" data-service-category="${esc(value)}" aria-pressed="${category === value}">${esc(value || '全部')}<span>${value ? source.templates.filter(item => item.category === value).length : source.templates.length}</span></button>`).join('');
      const display = resolve(source.templates), items = source.templates.map((item, index) => ({ item, display: display[index] || item })).filter(({ item, display }) => (!category || item.category === category) && `${display.title} ${display.description} ${item.category}`.toLowerCase().includes(query.toLowerCase()));
      $('[data-service-library-count]').textContent = `${source.templates.length} 个模板 · 当前 ${items.length} 个`;
      const host = $('[data-service-templates]'), scroll = host.scrollTop;
      host.innerHTML = items.map(({ item, display }) => `<label class="sa-service-template"><input type="checkbox" data-service-template="${esc(item.template)}" ${selected.has(item.template) ? 'checked' : ''} ${!selected.has(item.template) && config.items.length >= source.limit ? 'disabled' : ''}>${icon(item.icon)}<span><b>${esc(display.title)}</b><small>${esc(display.description)}</small></span></label>`).join('') || '<p class="sa-service-empty">没有匹配的模板，换个关键词试试。</p>';
      host.scrollTop = scroll;
    }
    function rowFields(item: ServiceModule, index: number) {
      return `<div class="sa-service-item-fields" data-service-item-fields>
        <details class="sa-service-icon-picker"><summary>${icon(item.icon)}<span>更换图标</span>${icon('chevron-down')}</summary><div class="sa-service-icons" role="group" aria-label="选择模块图标">${library().icons.map(entry => `<button type="button" data-service-icon="${esc(entry.key)}" aria-label="${esc(entry.title)}" title="${esc(entry.title)}" aria-pressed="${item.icon === entry.key}">${icon(entry.key)}<span>${esc(entry.title)}</span></button>`).join('')}</div></details>
        <div class="sa-grid"><label class="sa-field"><span>标题</span><input data-service-field="title" value="${esc(item.title)}" maxlength="24" placeholder="如订单交付" aria-label="第 ${index + 1} 项标题"></label><label class="sa-field"><span>说明</span><input data-service-field="description" value="${esc(item.description)}" maxlength="100" placeholder="填写一行具体说明" aria-label="第 ${index + 1} 项说明"></label></div>
        <details class="sa-service-variables"><summary>插入商品信息</summary><p>插入后会随当前商品的配置显示。先点选标题或说明，再选择需要的信息。</p><div>${tokens.map(token => `<button type="button" data-service-token="${token}">${token}</button>`).join('')}</div></details>
        <div class="sa-service-link-choices" role="group" aria-label="模块跳转位置">${links.map(([value, title]) => `<button type="button" data-service-link="${value}" aria-pressed="${item.link === value}">${title}</button>`).join('')}</div><label class="sa-field"><span>跳转地址（可选）</span><input data-service-field="link" value="${esc(item.link || '')}" maxlength="2000" placeholder="站内路径或 https:// 地址" aria-label="第 ${index + 1} 项跳转地址"></label>
      </div>`;
    }
    function renderRows() {
      $('[data-service-selected-count]').textContent = `${config.items.length} / ${library().limit}`;
      $<HTMLButtonElement>('[data-service-add]').disabled = config.items.length >= library().limit || locked();
      $('[data-service-selected]').innerHTML = config.items.map((item, index) => `<article class="sa-service-row" data-service-row="${index}"><header><button class="sa-service-edit" type="button" data-service-edit="${index}" aria-expanded="${opened === index}" aria-controls="shop-service-fields-${index}"><span data-service-row-icon>${icon(item.icon)}</span><span><b data-service-row-title>${esc(item.title || '自定义说明')}</b><small>${item.template ? '模板模块' : '自定义模块'}</small></span>${icon('chevron-down')}</button><div class="sa-service-row-actions"><button type="button" data-service-move="-1" aria-label="上移第 ${index + 1} 项" ${index === 0 ? 'disabled' : ''}>${icon('arrow-up')}</button><button type="button" data-service-move="1" aria-label="下移第 ${index + 1} 项" ${index === config.items.length - 1 ? 'disabled' : ''}>${icon('arrow-down')}</button><button type="button" data-service-remove aria-label="移除第 ${index + 1} 项">${icon('x')}</button></div></header><div id="shop-service-fields-${index}" ${opened !== index ? 'hidden' : ''}>${opened === index ? rowFields(item, index) : ''}</div></article>`).join('') || '<p class="sa-service-empty">还没有选择模块。</p>';
    }
    function render() {
      root.querySelectorAll<HTMLElement>('[data-service-mode]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.serviceMode === config.mode)));
      $('[data-service-custom]').hidden = config.mode !== 'custom'; preview(); renderLibrary(); renderRows();
    }
    function changed() { options.onChange(); preview(); }
    function focusRow(index: number, selector = '[data-service-edit]') { root.querySelector<HTMLElement>(`[data-service-row="${index}"] ${selector}`)?.focus({ preventScroll: true }); }
    root.addEventListener('click', event => {
      const targetElement = event.target;
      if (!(targetElement instanceof Element) || suspended || lifecycle.signal.aborted) return;
      const group = targetElement.closest<HTMLElement>('[data-service-category]');
      if (group) { category = group.dataset.serviceCategory || ''; renderLibrary(); [...root.querySelectorAll<HTMLElement>('[data-service-category]')].find(button => button.dataset.serviceCategory === category)?.focus({ preventScroll: true }); return; }
      const edit = targetElement.closest<HTMLElement>('[data-service-edit]');
      if (edit) { const index = Number(edit.dataset.serviceEdit); if (!Number.isInteger(index) || !config.items[index]) return; opened = opened === index ? -1 : index; renderRows(); focusRow(index); return; }
      if (locked()) return;
      const mode = targetElement.closest<HTMLElement>('[data-service-mode]');
      if (mode) {
        const value = mode.dataset.serviceMode;
        if (value !== 'auto' && value !== 'custom' && value !== 'hidden') return;
        config.mode = value;
        if (config.mode === 'custom' && !initialized) { const product = options.getProduct(); config.items = product ? shared.defaults(product, options.getContext().products) : []; initialized = true; }
        feedback(''); render(); changed(); return;
      }
      if (targetElement.closest<HTMLElement>('[data-service-add]')) {
        if (config.items.length >= library().limit) { feedback(`最多添加 ${library().limit} 项服务说明。`); return; }
        config.items.push({ icon: 'shield-check', title: '', description: '', link: '' }); opened = config.items.length - 1;
        renderLibrary(); renderRows(); changed(); focusRow(opened, '[data-service-field="title"]'); $(`[data-service-row="${opened}"]`)?.scrollIntoView({ block: 'nearest', behavior: 'instant' }); return;
      }
      const row = targetElement.closest<HTMLElement>('[data-service-row]'); if (!row) return;
      const index = Number(row.dataset.serviceRow), item = config.items[index];
      if (!Number.isInteger(index) || !item) return;
      const move = targetElement.closest<HTMLElement>('[data-service-move]'), remove = targetElement.closest<HTMLElement>('[data-service-remove]'), chosenIcon = targetElement.closest<HTMLElement>('[data-service-icon]'), link = targetElement.closest<HTMLElement>('[data-service-link]'), token = targetElement.closest<HTMLElement>('[data-service-token]');
      if (move) {
        const direction = Number(move.dataset.serviceMove);
        if (direction !== -1 && direction !== 1) return;
        const target = index + direction, other = config.items[target]; if (!other) return;
        [config.items[index], config.items[target]] = [other, item];
        if (opened === index) opened = target; else if (opened === target) opened = index;
        renderRows(); changed(); focusRow(target);
      } else if (remove) {
        config.items.splice(index, 1); if (opened === index) opened = -1; else if (opened > index) opened--;
        renderLibrary(); renderRows(); changed(); if (config.items.length) focusRow(Math.min(index, config.items.length - 1)); else $('[data-service-add]').focus();
      } else if (chosenIcon) {
        const key = chosenIcon.dataset.serviceIcon; if (!key || !library().icons.some(icon => icon.key === key)) return;
        item.icon = key; renderRows(); changed(); focusRow(index, '.sa-service-icon-picker summary');
      } else if (link) {
        const value = link.dataset.serviceLink; if (value === undefined || !links.some(([candidate]) => candidate === value)) return;
        item.link = value; const input = row.querySelector<HTMLInputElement>('[data-service-field="link"]'); if (input) input.value = value;
        row.querySelectorAll<HTMLElement>('[data-service-link]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.serviceLink === item.link))); changed();
      } else if (token) {
        const field = row.dataset.lastTextField === 'title' ? 'title' : 'description';
        if (!tokens.includes(token.dataset.serviceToken || '')) return;
        const input = row.querySelector<HTMLInputElement>(`[data-service-field="${field}"]`);
        if (!input) return;
        const value = `{${token.dataset.serviceToken}}`, start = input.selectionStart ?? input.value.length, end = input.selectionEnd ?? start;
        if (input.value.length - (end - start) + value.length > input.maxLength) { feedback('当前文字已达长度上限，请缩短后插入。'); return; }
        input.setRangeText(value, start, end, 'end'); input.dispatchEvent(new Event('input', { bubbles: true })); input.focus();
      }
    }, { signal: lifecycle.signal });
    root.addEventListener('focusin', event => {
      const input = event.target;
      if (!(input instanceof HTMLInputElement) || !['title', 'description'].includes(input.dataset.serviceField || '')) return;
      const row = input.closest<HTMLElement>('[data-service-row]');
      if (row) row.dataset.lastTextField = input.dataset.serviceField;
    }, { signal: lifecycle.signal });
    root.addEventListener('input', event => {
      const targetElement = event.target;
      if (!(targetElement instanceof HTMLInputElement) || suspended || lifecycle.signal.aborted) return;
      if (targetElement.matches('[data-service-search]')) { query = targetElement.value.trim(); renderLibrary(); return; }
      const field = targetElement.dataset.serviceField, row = targetElement.closest<HTMLElement>('[data-service-row]'); if (!row || locked() || (field !== 'title' && field !== 'description' && field !== 'link')) return;
      const item = config.items[Number(row.dataset.serviceRow)]; if (!item) return;
      item[field] = targetElement.value;
      const title = row.querySelector('[data-service-row-title]');
      if (field === 'title' && title) title.textContent = targetElement.value || '自定义说明';
      if (field === 'link') row.querySelectorAll<HTMLElement>('[data-service-link]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.serviceLink === targetElement.value)));
      feedback(''); changed();
    }, { signal: lifecycle.signal });
    root.addEventListener('change', event => {
      const targetElement = event.target;
      if (!(targetElement instanceof HTMLInputElement)) return;
      if (!targetElement.matches('[data-service-template]') || locked()) return;
      const key = targetElement.dataset.serviceTemplate, index = config.items.findIndex(item => item.template === key);
      if (targetElement.checked && index < 0) {
        if (config.items.length >= library().limit) { targetElement.checked = false; feedback(`最多添加 ${library().limit} 项服务说明。`); return; }
        const template = library().templates.find(item => item.template === key); if (!template) { targetElement.checked = false; return; }
        const { category: ignoredCategory, ...item } = template; config.items.push(structuredClone(item));
      } else if (!targetElement.checked && index >= 0) { config.items.splice(index, 1); if (opened === index) opened = -1; else if (opened > index) opened--; }
      renderLibrary(); renderRows(); changed(); [...root.querySelectorAll<HTMLInputElement>('[data-service-template]')].find(input => input.dataset.serviceTemplate === key)?.focus({ preventScroll: true });
    }, { signal: lifecycle.signal });
    window.addEventListener('pagehide', () => {
      suspended = true;
      root.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input,button').forEach(control => { control.disabled = true; });
    }, { signal: lifecycle.signal });
    window.addEventListener('pageshow', event => { if (event.persisted) feedback('请刷新页面后编辑服务说明。'); }, { signal: lifecycle.signal });
    const editor: ShopServiceEditor = {
      reset(product) {
        if (suspended || lifecycle.signal.aborted) return;
        config = serviceConfig(product.services);
        initialized = config.mode === 'custom' || config.items.length > 0; category = ''; query = ''; opened = -1;
        $<HTMLInputElement>('[data-service-search]').value = ''; feedback(''); render();
      },
      refreshPreview: preview,
      collect() {
        if (suspended || lifecycle.signal.aborted) throw new Error('请刷新页面后编辑服务说明。');
        try { return collectServices(config, library()); }
        catch (error) {
          if (error instanceof ServiceInputError) { opened = error.index; config.mode = 'custom'; render(); focusRow(opened, '[data-service-field="title"]'); }
          throw error;
        }
      },
      destroy() { lifecycle.abort(); if (editors.get(root) === editor) editors.delete(root); },
    };
    editors.set(root, editor);
    return editor;
  }
declare global { interface Window { NewPressShopServiceEditor: { create: typeof create } } }
window.NewPressShopServiceEditor = { create };
