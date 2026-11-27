/** Shared browser transport. No DOM, UI framework, polling or storage. */
export interface ClientSession {
  authenticated: boolean;
  configured?: boolean;
  id?: number;
  name?: string;
  csrf_token?: string;
  can_admin?: boolean;
  can_publish?: boolean;
}

/** Wire contract: GET /api/v1/social/session (docs/openapi.yaml). */
export interface SocialSession extends ClientSession {
  configured: boolean;
}

export type APIObject = Record<string, unknown>;
export interface ClientResponse {
  ok: boolean;
  status: number;
  headers: Pick<Headers, 'get'>;
  json(): Promise<unknown>;
}
export type ClientTransport = (path: string, options: RequestInit) => Promise<ClientResponse>;
export interface ClientOptions {
  fetch?: ClientTransport;
  session?: () => ClientSession | null;
  epoch?: () => number;
  onUnauthorized?: (error: APIError) => void;
}
export interface RequestOptions {
  method?: string;
  body?: unknown;
  anonymous?: boolean;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  timeout?: number;
}
export type SessionListener = (session: Readonly<Partial<ClientSession>>) => void;

const root = typeof window !== 'undefined' ? window : globalThis;
const isObject = (value: unknown): value is APIObject =>
  value !== null && typeof value === 'object' && !Array.isArray(value);
const aborted = () => new DOMException('会话已变化或请求已取消', 'AbortError');
const unconfirmed = () => new Error('服务返回结果未确认，内容已保留，请重试');

function isSocialSession(value: APIObject): value is APIObject & SocialSession {
  return typeof value.authenticated === 'boolean' && typeof value.configured === 'boolean'
    && (value.id === undefined || (typeof value.id === 'number' && Number.isSafeInteger(value.id) && value.id > 0))
    && ['name', 'csrf_token'].every(key => value[key] === undefined || typeof value[key] === 'string')
    && ['can_admin', 'can_publish'].every(key => value[key] === undefined || typeof value[key] === 'boolean');
}

function errorFields(data: unknown): APIObject | undefined {
  return isObject(data) && isObject(data.error) ? data.error : undefined;
}

export class APIError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: unknown;
  readonly retryAfter: number;

  constructor(message: string, response: Pick<ClientResponse, 'status' | 'headers'>, data: unknown) {
    super(message);
    this.name = 'APIError';
    this.status = response.status;
    const error = errorFields(data);
    this.code = typeof error?.code === 'string' ? error.code
      : isObject(data) && typeof data.error === 'string' ? data.error : '';
    this.details = error?.details;
    this.retryAfter = Math.max(0, Number(response.headers.get('Retry-After')) || 0);
  }
}

export function create(options: ClientOptions = {}) {
  const transport: ClientTransport = options.fetch || root.fetch.bind(root);
  let session: ClientSession | null = null;
  let epoch = 0;
  let sequence = 0;
  let sessionRequest: Promise<ClientSession> | null = null;
  const inflight = new Set<AbortController>();
  const writes = new Map<string, Promise<unknown>>();
  const listeners = new Set<SessionListener>();
  const current = () => options.session ? options.session() : session;
  const generation = () => options.epoch ? options.epoch() : epoch;

  function cancel(): void {
    for (const controller of inflight) controller.abort();
    writes.clear();
  }

  function setSession<T extends ClientSession | null>(next: T): T {
    const before = session;
    if ((before?.id || 0) !== (next?.id || 0)
      || before?.authenticated !== next?.authenticated || before?.csrf_token !== next?.csrf_token) {
      epoch++;
      sequence++;
      cancel();
    }
    session = next;
    for (const listener of listeners) listener({ ...next });
    return next;
  }

  // Endpoint-specific fields stay unknown until the consumer validates them.
  async function request(path: string, {
    method = 'GET', body, anonymous = false, headers = {}, signal, timeout = 12000,
  }: RequestOptions = {}): Promise<APIObject> {
    // Credentials and CSRF must never be forwarded to an external origin.
    if (typeof path !== 'string' || !path.startsWith('/') || path.startsWith('//')
      || path.includes('\\') || /[\u0000-\u0020\u007f]/.test(path)) {
      throw new TypeError('API path must be same-origin');
    }
    method = method.toUpperCase();
    const owner = generation();
    const controller = new AbortController();
    const multipart = typeof FormData !== 'undefined' && body instanceof FormData;
    const abort = () => controller.abort(signal?.reason);
    if (signal?.aborted) abort();
    else signal?.addEventListener('abort', abort, { once: true });
    const timer = setTimeout(() => controller.abort('timeout'), timeout);
    if (!anonymous) inflight.add(controller);
    try {
      const response = await transport(path, {
        method, credentials: 'same-origin', redirect: 'error', cache: 'no-store', signal: controller.signal,
        headers: {
          Accept: 'application/json',
          ...(body !== undefined && !multipart ? { 'Content-Type': 'application/json' } : {}),
          ...(!anonymous && method !== 'GET' && method !== 'HEAD' ? { 'X-CSRF-Token': current()?.csrf_token || '' } : {}),
          ...headers,
        },
        ...(body !== undefined ? { body: multipart ? body : JSON.stringify(body) } : {}),
      });
      let data: unknown;
      try { data = await response.json(); } catch { data = null; }
      if (controller.signal.aborted || (!anonymous && owner !== generation())) throw aborted();
      if (!response.ok) {
        const message = errorFields(data)?.message;
        const error = new APIError(typeof message === 'string' && message ? message : '服务暂时不可用，请稍后重试', response, data);
        if (response.status === 401 && !anonymous) {
          setSession({ authenticated: false, configured: true });
          options.onUnauthorized?.(error);
        }
        throw error;
      }
      if (!isObject(data)) throw unconfirmed();
      return data;
    } catch (error) {
      if (controller.signal.aborted) {
        if (controller.signal.reason === 'timeout') throw new Error('连接超时，内容已保留，请重试');
        // A current 401 invalidates its owner but must retain its APIError.
        if (error instanceof APIError && error.status === 401) throw error;
        throw aborted();
      }
      if (error instanceof TypeError) {
        throw new Error(root.navigator?.onLine === false ? '当前处于离线状态，联网后可重试' : '网络连接失败，内容已保留，请重试');
      }
      throw error;
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener('abort', abort);
      inflight.delete(controller);
    }
  }

  async function getSession(force = false): Promise<ClientSession> {
    if (sessionRequest && !force) return sessionRequest;
    if (session && !force) return session;
    const id = ++sequence;
    const owner = generation();
    const pending = request('/api/v1/social/session', { anonymous: true }).then(next => {
      if (id !== sequence || owner !== generation()) return session || { authenticated: false, configured: true };
      if (!isSocialSession(next)) throw unconfirmed();
      return setSession(next);
    }).finally(() => { if (sessionRequest === pending) sessionRequest = null; });
    sessionRequest = pending;
    return pending;
  }

  function once<T>(key: string, action: () => T | PromiseLike<T>): Promise<T> {
    // A key identifies one operation/result type until its promise settles.
    const existing = writes.get(key) as Promise<T> | undefined;
    if (existing) return existing;
    const owner = generation();
    const pending = Promise.resolve().then(() => {
      if (owner !== generation()) throw aborted();
      return action();
    }).finally(() => { if (writes.get(key) === pending) writes.delete(key); });
    writes.set(key, pending);
    return pending;
  }

  return Object.freeze({
    request, getSession, setSession, cancel, once,
    subscribe(listener: SessionListener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    snapshot(): ClientSession | null { return session ? { ...session } : null; },
    dispose() {
      sequence++;
      epoch++;
      cancel();
      listeners.clear();
    },
  });
}

export const api = Object.freeze({ version: 1 as const, create, APIError });
export type NewPressClientAPI = typeof api;
export type Client = ReturnType<typeof create>;
