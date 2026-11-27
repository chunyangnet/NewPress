import type { APIObject, ClientSession, RequestOptions } from './newpress-client';
import type { Action, Contact, SocialStorage } from './interaction-types';
import type { VerificationAPI } from './verification-types';

export type EngagementKind = 'bookmark' | 'like' | 'follow';
export interface EngagementState { target_id: number; count: number; active: boolean; collection: string }
export type EngagementStates = Record<EngagementKind, EngagementState[]>;
export interface GuestRecords { bookmarks: string[]; likes: number[]; following: number[]; skipped: number }
export interface RegistrationController { canLeave(): boolean; open(flow?: string): Promise<void> }
export interface InteractionAPI {
  api(path: string, options?: RequestOptions): Promise<APIObject>;
  getSession(force?: boolean): Promise<ClientSession>;
  run(action: () => unknown | Promise<unknown>): Promise<unknown>;
  signIn(action?: Action): Promise<unknown>;
  info(title: string, description: string): void;
  storage: SocialStorage;
  announce(message: string): void;
  openPeer(peer: number | string, contact?: Contact): Promise<unknown>;
  searchContacts(query?: string, append?: boolean): Promise<unknown>;
}
export interface EngagementAPI {
  readonly authenticated: boolean;
  refresh(): Promise<void>;
  paint(): void;
  toggle(button: HTMLButtonElement, kind: EngagementKind): Promise<void>;
  set(kind: EngagementKind, targetID: number, active: boolean, collection?: string): Promise<boolean>;
  preferences(): { saved: unknown[]; following: string[]; likes: string[] };
}

declare global {
  interface Window {
    ChunnelInteractions?: InteractionAPI;
    ChunnelEngagement?: EngagementAPI;
    ChunnelGuestImport?: { normalize(raw: unknown, origin: string): GuestRecords };
    ChunnelAccountNav?: () => void;
    ChunnelRegistration?: { mount(root: HTMLElement): RegistrationController | undefined };
    ChunnelVerification?: VerificationAPI;
    ChunnelNotificationSettings?: { load(): Promise<void> };
    ChunnelWallet?: { load(): Promise<void> };
    ChunnelProgression?: { load(): Promise<void> };
    NewPressIdentity?: { load(): Promise<void> };
  }
  interface HTMLElement { chunnelRegistration?: RegistrationController }
  interface DocumentEventMap {
    'chunnel:session': CustomEvent<ClientSession>;
    'chunnel:account-authenticated': CustomEvent<{ root: HTMLElement }>;
  }
}
