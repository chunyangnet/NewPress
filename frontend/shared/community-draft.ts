import { record, string } from './ledger-data';

export const composeFields = ['title', 'body', 'tags', 'kind', 'format'] as const;
export type ComposeValues = Partial<Record<typeof composeFields[number], string>>;
export function composeInputs(form: HTMLFormElement): (HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement)[] {
  return composeFields.flatMap(name => {
    const field = form.elements.namedItem(name);
    return field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement || field instanceof HTMLSelectElement ? [field] : [];
  });
}
export function composeSnapshot(form: HTMLFormElement): ComposeValues {
  return Object.fromEntries(composeInputs(form).map(field => [field.name, field.value]));
}
export function composeDraft(value: unknown, inputs: ReturnType<typeof composeInputs>): ComposeValues {
  const raw = record(value), result: ComposeValues = {};
  if (typeof raw.body !== 'string') throw new Error('草稿正文缺失');
  for (const field of inputs) {
    const name = field.name as typeof composeFields[number]; if (raw[name] === undefined) continue;
    const value = string(raw[name]), limit = field instanceof HTMLSelectElement ? 100 : field.maxLength > 0 ? field.maxLength : 20000;
    if (value.length > limit || (field instanceof HTMLSelectElement && ![...field.options].some(option => option.value === value))) throw new Error('保存的草稿字段与当前表单不一致');
    result[name] = value;
  }
  return result;
}
export function composeEqual(a: ComposeValues, b: ComposeValues): boolean { return composeFields.every(key => a[key] === b[key]); }
export function composeSaved(form: HTMLFormElement, owner: number, values: ComposeValues): void {
  form.dispatchEvent(new CustomEvent('community:saved', { detail: { owner, values } }));
}
