import { record, text, integer } from './collection-data';
import { safeLink } from './shop-services';
import type { ServiceConfig, ServiceLibrary, ServiceModule, ServiceTemplate } from './shop-service-types';

const key = (value: unknown): string => {
  const result = text(value);
  if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(result)) throw new Error('服务说明标识无效');
  return result;
};
function item(value: unknown): ServiceModule {
  const v = record(value);
  return { icon: key(v.icon), title: text(v.title), description: text(v.description), link: v.link === undefined ? '' : text(v.link), ...(v.template === undefined || v.template === '' ? {} : { template: key(v.template) }) };
}

/** Nil and omitted Go items represent an empty configuration, not an invalid response. */
export function serviceConfig(value: unknown): Required<ServiceConfig> {
  if (value === null || value === undefined) return { mode: 'auto', items: [] };
  const v = record(value);
  if (v.mode !== 'auto' && v.mode !== 'custom' && v.mode !== 'hidden') throw new Error('服务说明模式无效');
  if (v.items !== null && v.items !== undefined && (!Array.isArray(v.items) || v.items.length > 60)) throw new Error('服务说明最多 60 项');
  return { mode: v.mode, items: Array.isArray(v.items) ? v.items.map(item) : [] };
}

export function serviceLibrary(value: unknown): ServiceLibrary {
  if (value === null || value === undefined) return { templates: [], icons: [], limit: 60 };
  const v = record(value), limit = integer(v.limit, 1);
  if (limit > 60 || !Array.isArray(v.templates) || !Array.isArray(v.icons) || v.templates.length > 500 || v.icons.length > 500) throw new Error('服务说明模板库无效');
  const icons = v.icons.map(value => { const v = record(value); return { key: key(v.key), title: text(v.title) }; });
  const templates: ServiceTemplate[] = v.templates.map(value => {
    const v = record(value), result = { ...item(v), template: key(v.template), category: text(v.category) };
    if (!icons.some(icon => icon.key === result.icon) || result.link && !safeLink(result.link)) throw new Error('服务说明模板无效');
    return result;
  });
  if (new Set(icons.map(v => v.key)).size !== icons.length || new Set(templates.map(v => v.template)).size !== templates.length) throw new Error('服务说明模板标识重复');
  return { templates, icons, limit };
}

export function collectServices(config: Required<ServiceConfig>, library: ServiceLibrary): ServiceConfig | null {
  const result = serviceConfig(config), selected = new Set<string>();
  if (result.items.length > library.limit) throw new Error(`最多添加 ${library.limit} 项服务说明。`);
  for (const [index, item] of result.items.entries()) {
    item.title = item.title.trim(); item.description = item.description.trim(); item.link = (item.link || '').trim();
    const error = !item.title || [...item.title].length > 24 || /[<>\r\n\u0000]/.test(item.title) ? '标题需填写 1 至 24 字'
      : [...item.description].length > 100 || /[<>\u0000]/.test(item.description) ? '说明最多 100 字，不能包含标签'
      : item.link && !safeLink(item.link) ? '链接请使用站内路径或 HTTPS 地址'
      : !library.icons.some(icon => icon.key === item.icon) ? '请选择有效图标'
      : item.template && (selected.has(item.template) || !library.templates.some(template => template.template === item.template)) ? '模板不存在或重复' : '';
    if (error) throw new ServiceInputError(index, `服务说明第 ${index + 1} 项：${error}。`);
    if (item.template) selected.add(item.template);
  }
  return result.mode === 'auto' && !result.items.length ? null : result;
}
export class ServiceInputError extends Error { constructor(readonly index: number, message: string) { super(message); } }
