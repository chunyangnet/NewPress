import type { SiteTime } from '../shared/site-time';
import type { NewPressClientAPI } from '../shared/newpress-client';
import type { ChoiceMenuAPI } from '../shared/choice-types';
import type { ColorModeAPI, ColorModeChange, ColorReveal } from '../shared/color-types';
import type { AdminAPI, AdminUI, AdminRegionChange } from '../core/admin-types';
import type { MediaPicker, ComponentMediaSelection, EditorSourceSelection } from '../core/media-types';
import type { ScrollAPI } from '../shared/scroll-types';
import type { MotionAPI } from '../shared/motion-types';
import type { CookieConsentAPI } from '../shared/consent-types';

declare global {
  interface Window {
    ChunnelTime: SiteTime;
    NewPressClient?: NewPressClientAPI;
    NewPressAnalytics?: { mount: () => void };
    NewPressSelects?: boolean;
    NewPressChoiceMenu?: ChoiceMenuAPI;
    ChunnelColorMode?: ColorModeAPI;
    ChunnelColorReveal?: ColorReveal;
    ChunnelAdminUI?: AdminUI;
    ChunnelAdmin?: AdminAPI;
    ChunnelScroll?: ScrollAPI;
    NewPressMediaPicker?: MediaPicker;
    NewPressComponentMedia?: (launcher: HTMLElement, onSelect: ComponentMediaSelection) => void;
    ChunnelEmoji?: {
      close(): void;
      render(root?: ParentNode): void;
      fragment(text: string): DocumentFragment;
      enhance(root: Element): void;
      validateReplacement(input: HTMLInputElement | HTMLTextAreaElement, text: string, range?: EditorSourceSelection): string;
      replaceSelection(input: HTMLInputElement | HTMLTextAreaElement, text: string, range?: EditorSourceSelection): string;
    };
    Chunnel?: {
      showToast(message: string, tone?: string): void;
      createIcon(name: string): SVGElement;
      setOverlay(element: HTMLElement, open: boolean): void;
    };
    ChunnelMotion?: MotionAPI;
    ChunnelSearchReady?: boolean;
    NewPressCookieConsent?: CookieConsentAPI;
    __newpressAnalyticsLoaded?: boolean;
  }
  interface DocumentEventMap {
    'chunnel:color-mode': CustomEvent<ColorModeChange>;
    'chunnel:admin-before-update': CustomEvent<AdminRegionChange>;
    'chunnel:admin-before-replace': CustomEvent<AdminRegionChange>;
    'chunnel:admin-updated': CustomEvent<AdminRegionChange>;
    'chunnel:overlay': CustomEvent<{ open: boolean }>;
    'newpress:cookie-consent': CustomEvent<{ analytics: boolean }>;
  }
}
