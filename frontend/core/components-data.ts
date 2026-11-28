import { parseComponentConfig } from './component-config';
import type { ComponentConfig } from './component-config';

export interface ComponentState {
  counts: number[];
  total: number;
  average: number;
  participated: boolean;
  choice: number;
  active: boolean;
  claimed: number;
  pending: number;
  pending_self: boolean;
}
export interface ComponentSnapshot {
  state: ComponentState;
  csrf_token: string;
  authenticated: boolean;
  owner: boolean;
  fingerprint: string;
  config: string;
  currency_name: string;
}
export type ComponentAction = 'vote' | 'claim' | 'activate' | 'pause';
export interface ComponentAttempt { action: ComponentAction; value: number; fingerprint: string; token: string; }
export function validComponentTarget(target: string): boolean {
  return /^(?:[1-9][0-9]{0,18}|shop:[a-z0-9][a-z0-9_-]{0,63})$/.test(target);
}
const kinds = Object.fromEntries('callout quote code download gift redpacket gallery image-card audio video table accordion tabs card badges progress counter poll rating compare carousel annotation glossary ai mention chart'.split(' ').map(kind => [kind, true]));
const invalid = () => new Error('组件返回的数据无效，请刷新页面后核对结果。');
function record(input: unknown): Record<string, unknown> {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw invalid();
  return input as Record<string, unknown>;
}
function text(input: unknown, max = 65536): string {
  if (typeof input !== 'string' || input.length > max) throw invalid();
  return input;
}
function bool(input: unknown): boolean { if (typeof input !== 'boolean') throw invalid(); return input; }
function integer(input: unknown, min = 0, max = 50000): number {
  if (typeof input !== 'number' || !Number.isSafeInteger(input) || input < min || input > max) throw invalid();
  return input;
}
export function componentError(input: unknown, fallback = '暂时无法完成，请稍后重试'): string {
  if (input instanceof Error) return input.message || fallback;
  if (!input || typeof input !== 'object' || Array.isArray(input)) return fallback;
  const value = input as Record<string, unknown>;
  if (typeof value.message === 'string' && value.message) return value.message.slice(0, 4096);
  if (typeof value.error === 'string' && value.error) return value.error.slice(0, 4096);
  if (value.error && typeof value.error === 'object' && 'message' in value.error && typeof value.error.message === 'string') return value.error.message.slice(0, 4096) || fallback;
  return fallback;
}
export function decodePublicComponent(encoded: string, expectedKind: string): ComponentConfig {
  if (encoded.length > 87384 || !/^[A-Za-z0-9_-]+$/.test(encoded)) throw invalid();
  const bytes = Uint8Array.from(atob(encoded.replace(/-/g, '+').replace(/_/g, '/')), char => char.charCodeAt(0));
  if (bytes.length > 65536) throw invalid();
  const config = parseComponentConfig(JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)) as unknown, kinds);
  if (config.kind !== expectedKind) throw invalid();
  if (config.kind === 'poll' && (config.items.length < 2 || config.items.length > 12)) throw invalid();
  if (config.kind === 'rating' && config.max !== 5 && config.max !== 10) throw invalid();
  if (config.kind === 'redpacket') { integer(config.count, 1, 10000); if (!/^[0-9]{1,12}(\.[0-9]{1,6})?$/.test(config.amount)) throw invalid(); }
  return config;
}
export function parseComponentState(input: unknown, config: ComponentConfig): ComponentState {
  const value = record(input), length = config.kind === 'rating' ? config.max + 1 : config.items.length;
  if (!['poll', 'rating', 'redpacket'].includes(config.kind) || !Array.isArray(value.counts) || value.counts.length !== length) throw invalid();
  const state: ComponentState = {
    counts: value.counts.map(count => integer(count)), total: integer(value.total), average: typeof value.average === 'number' ? value.average : NaN,
    participated: bool(value.participated), choice: integer(value.choice, -1, Math.max(length - 1, 0)), active: bool(value.active),
    claimed: integer(value.claimed, 0, 10000), pending: integer(value.pending, 0, 10000), pending_self: bool(value.pending_self),
  };
  if (!Number.isFinite(state.average) || state.counts.reduce((a, b) => a + b, 0) !== state.total) throw invalid();
  const average = state.total ? state.counts.reduce((sum, count, index) => sum + count * index, 0) / state.total : 0;
  if (Math.abs(state.average - average) > 1e-9) throw invalid();
  if (config.kind === 'redpacket') {
    if (state.total || state.choice !== -1 || state.claimed + state.pending > config.count || (state.participated && (!state.claimed || state.pending_self)) || (state.pending_self && !state.pending)) throw invalid();
  } else {
    if (state.claimed || state.pending || state.pending_self || (state.participated ? state.choice < (config.kind === 'rating' ? 1 : 0) || !state.counts[state.choice] : state.choice !== -1)) throw invalid();
    if (config.kind === 'rating' && state.counts[0] !== 0) throw invalid();
  }
  return state;
}
export function parseComponentSnapshot(input: unknown, config: ComponentConfig, encoded: string): ComponentSnapshot {
  const value = record(input);
  if (value.config !== encoded) throw new Error('组件内容已更新，请刷新页面后参与。');
  const snapshot: ComponentSnapshot = {
    state: parseComponentState(value.state, config), csrf_token: text(value.csrf_token, 4096),
    authenticated: bool(value.authenticated), owner: bool(value.owner), fingerprint: text(value.fingerprint, 64),
    config: encoded, currency_name: text(value.currency_name, 200),
  };
  if (!/^[a-f0-9]{64}$/.test(snapshot.fingerprint) || (snapshot.owner && !snapshot.authenticated) || (snapshot.authenticated && !snapshot.csrf_token)) throw invalid();
  return snapshot;
}
export function componentActionConfirmed(state: ComponentState, attempt: Pick<ComponentAttempt, 'action' | 'value'>): boolean {
  switch (attempt.action) {
    case 'vote': return state.participated && state.choice === attempt.value;
    case 'claim': return state.participated && !state.pending_self && state.claimed > 0;
    case 'activate': return state.active;
    case 'pause': return !state.active;
  }
}
export function parseComponentReceipt(input: unknown, config: ComponentConfig, attempt: ComponentAttempt): ComponentState {
  const state = parseComponentState(record(input).state, config);
  if (!componentActionConfirmed(state, attempt)) throw new Error('操作结果尚未确认，请再次核对；不会改写原操作。');
  return state;
}
export function parseComponentProfile(input: unknown) {
  const value = record(input);
  bool(value.cached);
  return { name: text(value.name, 1024), bio: text(value.bio), meta: text(value.meta, 4096), avatar: text(value.avatar, 2048), url: text(value.url, 2048) };
}
