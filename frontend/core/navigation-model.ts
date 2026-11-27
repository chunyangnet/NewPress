import type { NavigationItem } from './navigation-types';

const typeLabels: Readonly<Record<string, string>> = {
  custom: '自定义', post: '文章', page: '页面', category: '分类', tag: '标签',
};

export function navigationTypeLabel(type: string): string {
  return Object.hasOwn(typeLabels, type) ? typeLabels[type] ?? '自定义' : '自定义';
}

export function exploreItemRoles(items: ReadonlyArray<Pick<NavigationItem, 'id' | 'parent_id'>>): string[] {
  const parents = new Set(items.map(item => item.parent_id));
  return items.map(item => item.parent_id ? '分组内入口' : parents.has(item.id) ? '分组（标题可点击）' : '独立入口');
}
