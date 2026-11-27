export const MAX_CUSTOM_BLOCKS = 12, MAX_CUSTOM_BYTES = 65536;
export type HomeVisibility = 'both' | 'desktop' | 'mobile' | 'hidden';
export interface HomeCustomBlock { key: string; title: string; html: string; surface: boolean; visibility: HomeVisibility }
export const isHomeVisibility = (value: unknown): value is HomeVisibility => value === 'both' || value === 'desktop' || value === 'mobile' || value === 'hidden';
const text = (value: unknown): string => {
  if (value == null) return '';
  if (typeof value !== 'string') throw new Error('HTML 组件文本格式不正确');
  return value;
};
// Validate identity and shape before touching any existing rows. Size limits are
// reported separately so oversized drafts can still be edited down, never lost.
export function parseCustomBlocks(raw: string): HomeCustomBlock[] {
  if (raw === '') return [];
  let values: unknown;
  try { values = JSON.parse(raw); } catch { throw new Error('HTML 组件格式不正确，请撤销或放弃修改后重试。'); }
  if (!Array.isArray(values)) throw new Error('HTML 组件必须是列表，请撤销或放弃修改后重试。');
  const seen = new Set<string>();
  return values.map((value: unknown) => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('HTML 组件数据格式不正确');
    const block = value as Record<string, unknown>;
    if (Object.keys(block).some(key => !['key', 'title', 'html', 'surface', 'visibility'].includes(key))) throw new Error('HTML 组件包含未知字段');
    if (typeof block.key !== 'string' || !/^custom(?:_[a-z0-9]{1,32})?$/.test(block.key) || seen.has(block.key)) throw new Error('HTML 组件标识无效或重复');
    if (!isHomeVisibility(block.visibility)) throw new Error('HTML 组件显示设备无效');
    if (block.surface != null && typeof block.surface !== 'boolean') throw new Error('HTML 组件底色设置无效');
    seen.add(block.key);
    return { key: block.key, title: text(block.title), html: text(block.html), surface: block.surface ?? false, visibility: block.visibility };
  });
}
export function customBlocksError(blocks: HomeCustomBlock[], raw: string): string {
  if (new TextEncoder().encode(raw).length > MAX_CUSTOM_BYTES) return 'HTML 组件内容总量超过 64 KiB，请精简后保存。';
  if (blocks.length > MAX_CUSTOM_BLOCKS) return '最多添加 12 个 HTML 组件，请删除多余组件后保存。';
  if (blocks.some(block => [...block.title].length > 80 || [...block.html].length > 4096)) return 'HTML 组件标题最多 80 字，内容最多 4096 字。';
  return '';
}
