import type { ClientSession, RequestOptions, APIObject } from './newpress-client';

export type Action = () => unknown | Promise<unknown>;
export type SocialAPI = (path: string, options?: RequestOptions) => Promise<APIObject>;
export interface Contact { id: number; name: string; username: string; available: boolean }
export interface Message { id: number; sender_id: number; recipient_id: number; content: string; nonce: string; created_at: string; read_at: string | null; status?: undefined; error?: undefined }
export interface OutgoingMessage { nonce: string; content: string; created_at: string; status: 'sending' | 'failed'; error: string; retryAt: number; id?: undefined; read_at?: undefined }
export interface Conversation { peer_id: number; contact: Contact; last?: Message; unread: number }
export interface MessageThread {
  peer: number; owner: number; contact: Contact; messages: Map<number, Message>;
  loaded: boolean; hasMore: boolean; loading: boolean; reading?: boolean;
  readThrough: number; readSent: number; syncedThrough: number; draft: string; outbox: OutgoingMessage[];
}
export type ThreadMode = 'refresh' | 'older' | 'initial' | 'send';
export interface NoticeBoundary { message_id: number; activity_id: number }
export interface Notice { id: string; kind: string; title: string; url: string; peer_id?: number; created_at: string; read: boolean }
export type NoticeFilter = 'all' | 'interaction' | 'activity' | 'unread';
export interface SocialState {
  session: ClientSession | null; epoch: number;
  inbox: Map<number, Conversation>; inboxCursor: number; inboxMore: boolean; messageUnread: number;
  peer: number; threads: Map<number, MessageThread>;
  notices: Notice[]; noticeFilter: NoticeFilter; noticeCursor: string; noticeMore: boolean; noticeThrough: NoticeBoundary | null; noticeUnread: number;
}
export interface SocialStorage { get(key: string, fallback: unknown): unknown; set(key: string, value: unknown): void; remove(key: string): void }
export interface SocialDOM {
  $<T extends Element = HTMLElement>(selector: string, root?: ParentNode): T;
  optional<T extends Element = HTMLElement>(selector: string, root?: ParentNode): T | null;
  $$<T extends Element = HTMLElement>(selector: string, root?: ParentNode): T[];
  node<K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K];
  button(text: string, className: string, action?: Action | null, icon?: string): HTMLButtonElement;
  empty(container: HTMLElement, title: string, description: string, action?: Action | null, label?: string, icon?: string): void;
  dateLabel(at: string): string;
  fullDate(at: string): string;
  createIcon(name: string): SVGElement;
}
