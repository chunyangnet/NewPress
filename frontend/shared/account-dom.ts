export function required<T extends Element = HTMLElement>(root: ParentNode, selector: string): T {
  const element = root.querySelector<T>(selector);
  if (!element) throw new Error(`账号页面缺少元素：${selector}`);
  return element;
}
export function input(form: HTMLFormElement, name: string): HTMLInputElement {
  const field = form.elements.namedItem(name);
  if (!(field instanceof HTMLInputElement)) throw new Error(`账号表单缺少输入项：${name}`);
  return field;
}
export function selection(form: HTMLFormElement, name: string): HTMLSelectElement {
  const field = form.elements.namedItem(name);
  if (!(field instanceof HTMLSelectElement)) throw new Error(`账号表单缺少选择项：${name}`);
  return field;
}
export function valueField(form: HTMLFormElement, name: string): HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement {
  const field = form.elements.namedItem(name);
  if (!(field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement || field instanceof HTMLSelectElement)) throw new Error(`账号表单缺少输入项：${name}`);
  return field;
}
