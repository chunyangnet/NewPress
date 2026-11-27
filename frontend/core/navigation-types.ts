export interface NavigationBadge {
  text: string;
  style: string;
  placement: string;
  background: string;
  color: string;
  border_color: string;
  gradient_to: string;
  font_size: number;
  radius: number;
  offset_x: number;
  offset_y: number;
  hide_on_mobile?: boolean;
}

export interface NavigationItem {
  id: number;
  parent_id: number;
  type: string;
  object_id: number;
  label: string;
  url: string;
  description: string;
  target: 'blank' | 'self';
  rel: string;
  icon: string;
  accent: string;
  presentation: string;
  badge?: NavigationBadge;
}

export interface NavigationSource {
  type?: string;
  objectId?: number;
  label?: string;
  url?: string;
  description?: string;
  icon?: string;
}

export interface IconPickerAPI { attach(root: ParentNode): void }
export interface NavigationBadgesAPI {
  readCard(card: Element): { badge?: NavigationBadge };
  paint(node: HTMLElement, badge?: Partial<NavigationBadge>): void;
  previewCard(card: HTMLElement, label: string): void;
}

declare global {
  interface Window {
    NewPressIconPicker?: IconPickerAPI;
    NewPressNavigationBadges?: NavigationBadgesAPI;
  }
}
