export type Control = HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
export function required<T extends HTMLElement = HTMLElement>(root: ParentNode, selector: string): T {
  const value = root.querySelector<T>(selector); if (!value) throw new Error('页面缺少控件：' + selector); return value;
}
export function control(form: HTMLFormElement, name: string): Control {
  const value = form.elements.namedItem(name);
  if (!(value instanceof HTMLInputElement || value instanceof HTMLSelectElement || value instanceof HTMLTextAreaElement)) throw new Error('页面缺少表单字段：' + name);
  return value;
}
export function input(form: HTMLFormElement, name: string): HTMLInputElement {
  const value = control(form, name); if (!(value instanceof HTMLInputElement)) throw new Error('页面字段类型不符：' + name); return value;
}
export function readOnly(form: HTMLFormElement, name: string, value: boolean): void {
  const field = control(form, name); if ('readOnly' in field) field.readOnly = value;
}
export function values(form: HTMLFormElement) {
  return [...form.querySelectorAll<Control>('input,select,textarea')].map(field => [field.name, field instanceof HTMLInputElement && field.type === 'checkbox' ? field.checked : field.value]);
}
