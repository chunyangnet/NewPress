import type { NavigationBadge, NavigationBadgesAPI } from './navigation-types';

const styles = new Set(['bubble', 'solid', 'pill', 'outline', 'soft', 'gradient', 'ribbon']);
const defaults = { style: 'bubble', placement: 'corner', background: '#D14343', color: '#FFFFFF', border_color: '#D14343', gradient_to: '#EF7C3E', font_size: 11, radius: 4, offset_x: 0, offset_y: 0 };
const colors = { background: 'bg', color: 'color', border_color: 'border', gradient_to: 'end' } as const;
const numbers = { font_size: ['size', 10, 16], radius: ['radius', 0, 20], offset_x: ['x', -12, 12], offset_y: ['y', -12, 12] } as const;
const colorKeys = ['background', 'color', 'border_color', 'gradient_to'] as const;
const numberKeys = ['font_size', 'radius', 'offset_x', 'offset_y'] as const;
const field = (card: Element, key: string) => card.querySelector<HTMLInputElement | HTMLSelectElement>(`[data-nav-field="badge_${key}"]`);

export function readCard(card: Element): { badge?: NavigationBadge } {
  const text = (field(card, 'text')?.value || '').trim();
  if (!text) return {};
  const badge: NavigationBadge = { text, ...defaults };
  for (const key of ['style', 'placement', ...colorKeys] as const) {
    badge[key] = field(card, key)?.value || defaults[key];
  }
  for (const key of colorKeys) badge[key] = badge[key].toUpperCase();
  for (const key of numberKeys) {
    const value = field(card, key)?.value;
    badge[key] = value === '' || value == null ? defaults[key] : Number(value);
  }
  if (card.querySelector<HTMLInputElement>('[data-nav-field="badge_show_on_mobile"]')?.checked === false) badge.hide_on_mobile = true;
  return { badge };
}

export function paint(node: HTMLElement, badge?: Partial<NavigationBadge>): void {
  node.className = 'nav-badge';
  node.textContent = badge?.text || '';
  node.hidden = !badge?.text;
  node.dataset.style = badge?.style && styles.has(badge.style) ? badge.style : defaults.style;
  node.dataset.placement = badge?.placement === 'inline' ? 'inline' : 'corner';
  for (const key of colorKeys) {
    const candidate = badge?.[key];
    const value = typeof candidate === 'string' && /^#[\da-f]{6}$/i.test(candidate) ? candidate : defaults[key];
    node.style.setProperty(`--nav-badge-${colors[key]}`, value);
  }
  for (const key of numberKeys) {
    const [token, min, max] = numbers[key], candidate = badge?.[key];
    const value = typeof candidate === 'number' && Number.isFinite(candidate) ? Math.max(min, Math.min(max, candidate)) : defaults[key];
    node.style.setProperty(`--nav-badge-${token}`, `${value}px`);
  }
}

export function previewCard(card: HTMLElement, label: string): void {
  const node = card.querySelector<HTMLElement>('[data-badge-preview]');
  if (!node) return;
  const style = field(card, 'style'), color = field(card, 'color'), background = field(card, 'background');
  const current = style?.value || defaults.style, previous = card.dataset.badgePreviousStyle;
  // Adjust preset text colors, while retaining deliberate custom colors.
  if (previous && previous !== current && color && background) {
    if (['outline', 'soft'].includes(current) && color.value.toUpperCase() === '#FFFFFF') color.value = background.value;
    else if (['outline', 'soft'].includes(previous) && !['outline', 'soft'].includes(current) && color.value === background.value) color.value = '#FFFFFF';
  }
  card.dataset.badgePreviousStyle = current;
  const previewLabel = card.querySelector('[data-badge-preview-label]');
  if (previewLabel) previewLabel.textContent = label;
  paint(node, readCard(card).badge);
  const endLabel = field(card, 'gradient_to')?.closest('label');
  if (endLabel) endLabel.hidden = current !== 'gradient';
  const radiusLabel = field(card, 'radius')?.closest('label');
  if (radiusLabel) radiusLabel.hidden = ['pill', 'ribbon'].includes(current);
}

window.NewPressNavigationBadges = Object.freeze({ readCard, paint, previewCard } satisfies NavigationBadgesAPI);
