export function required<T>(value: T | null | undefined): T {
  if (value === null || value === undefined) throw new Error('AI 页面缺少所需控件或数据，请重新载入。');
  return value;
}
