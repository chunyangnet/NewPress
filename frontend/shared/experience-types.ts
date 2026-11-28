import type { InteractionAPI } from './account-types';

export interface ExperienceDOM {
  $<T extends Element = HTMLElement>(selector: string, root?: ParentNode): T;
  optional<T extends Element = HTMLElement>(selector: string, root?: ParentNode): T | null;
  $$<T extends Element = HTMLElement>(selector: string, root?: ParentNode): T[];
  node<K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K];
}
export interface CommentPayload { parent_id: number; author_name: string; email: string; website: string; content: string }
export interface CommentRequest extends CommentPayload { expected_user_id?: number; form_token?: string; verification_token?: string }
export interface CommentSubmission { version: 1; nonce: string; owner: number; payload: CommentPayload; uncertain: boolean }
export interface CommentDraft { content: string; parent: string; reply: string; at: number }
export interface PublicBadge {
  key: string; kind: string; name: string; icon: string; color: string; style: string;
  description?: string; background?: string; image?: string; granted_at?: string; expires_at?: string; level?: number;
}
export interface PublicComment {
  id: number; post_id: number; parent_id: number; author_name: string;
  origin?: { location?: string; precision?: string; flag?: string; device?: string; kind?: string };
  badges?: PublicBadge[]; support?: { supported: boolean; label?: string };
}
export interface CommentReceipt { status: 'approved' | 'pending' | 'received'; notice?: string; replayed?: boolean; submission_key?: string; comment?: PublicComment | null }
export interface SavedEntry { key: string; title: string; url: string }
export interface GuestPreferences { saved: SavedEntry[]; following: string[]; following_ids: number[]; likes: string[] }
export interface CommunityPending { signature: string; nonce: string; guestContent: string | null }
export interface CommunityDraft { content: string; pending: CommunityPending | null }
export interface CommunityState { owner: number | null; generation: number; busy: boolean; pending: CommunityPending | null }
export interface ExperienceDependencies extends Pick<InteractionAPI, 'api' | 'getSession' | 'signIn' | 'info' | 'run' | 'storage' | 'announce'> {
  dom: ExperienceDOM;
  showToast(message: string, tone?: string): void;
  createIcon(name: string): SVGElement;
  setOverlay(element: HTMLElement, open: boolean): void;
  emoji: NonNullable<Window['ChunnelEmoji']>;
}
declare global {
  interface Window {
    NewPressRecognition?: {
      decorateComment(meta: HTMLElement, item: PublicComment): void;
      badge(value: Partial<PublicBadge>, options?: { interactive?: boolean; decorative?: boolean }): HTMLElement;
      colors(element: HTMLElement): void;
    }
  }
}
