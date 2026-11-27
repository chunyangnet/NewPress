import {record, failure} from './ledger-data';
import type {} from './account-types';
interface Options { method?: string; body?: unknown; verify?: boolean }
export function transport(owner: string, initialCSRF: string, onLost: () => void) {
  let csrf = initialCSRF || '', epoch = 0, lost = false;
  const controllers = new Set<AbortController>();
  function lose(): void { if (lost) return; lost = true; epoch++; controllers.forEach(controller => controller.abort()); onLost(); }
  async function request(path: string, { method = 'GET', body, verify = true }: Options = {}): Promise<Record<string, unknown>> {
    if (lost) throw new Error('登录状态已变化，请重新打开页面');
    if (method !== 'GET' && verify) await check();
    if (lost) throw new Error('登录状态已变化，请重新打开页面');
    const stamp = epoch, controller = new AbortController(), timer = setTimeout(() => controller.abort(), 12000); controllers.add(controller);
    try {
      const response = await fetch(path, { method, credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        headers: { Accept: 'application/json', ...(body !== undefined ? { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf } : {}) }, ...(body !== undefined ? { body: JSON.stringify(body) } : {}) });
      const raw: unknown = await response.json().catch(() => null);
      if (stamp !== epoch || lost) throw new Error('登录状态已变化，请重新打开页面');
      if (controller.signal.aborted) throw new Error('连接超时，原操作已保留，请核对后重试');
      if (!response.ok) {
        let problem: Record<string, unknown> = {}; try { problem = record(record(raw).error); } catch { /* Non-JSON error pages remain an uncertain result. */ }
        const error = failure({ message: problem.message || '服务暂不可用，请稍后重试原操作', code: problem.code, status: response.status });
        if ([401, 403].includes(response.status) && error.code?.startsWith('auth.')) lose();
        throw error;
      }
      return record(raw);
    } catch (error) { if (controller.signal.aborted && !lost) throw new Error('连接超时，原操作已保留，请核对后重试'); throw error; }
    finally { clearTimeout(timer); controllers.delete(controller); }
  }
  async function check() {
    const value = await request('/api/v1/social/session', { verify: false });
    if (value.authenticated !== true || String(value.id) !== owner || typeof value.csrf_token !== 'string' || !value.csrf_token || csrf && csrf !== value.csrf_token) { lose(); throw new Error('登录账号已变化，请重新打开页面'); }
    csrf = value.csrf_token; return value;
  }
  document.addEventListener('chunnel:session', event => { if (event.detail?.authenticated !== true || String(event.detail.id || '') !== owner || csrf && event.detail.csrf_token !== csrf) lose(); });
  window.addEventListener('pagehide', lose); window.addEventListener('pageshow', event => { if (event.persisted) lose(); });
  return Object.freeze({ request, check, lose, get lost() { return lost; } });
}
