export const mockupDevices = [
  { id: 'desktop', group: 'desktopDevice', label: '桌面显示器', icon: 'monitor', width: 1920, height: 1080 },
  { id: 'laptop', group: 'laptopDevice', label: '笔记本', icon: 'laptop', width: 1440, height: 900 },
  { id: 'tablet', group: 'tabletDevice', label: '平板', icon: 'tablet', width: 768, height: 1024 },
  { id: 'phone', group: 'phoneDevice', label: '手机', icon: 'phone', width: 390, height: 844 },
] as const;
export type MockupDevice = typeof mockupDevices[number];
export type MockupDeviceID = MockupDevice['id'];
export type MockupResult = {
  id: MockupDeviceID;
  status?: number | null;
  title?: string;
  warnings?: string[];
  error?: string;
};
export type MockupEvent =
  | { type: 'session'; id: string; interval: number }
  | { type: 'frame'; id: MockupDeviceID; screenshot: string; capturedAt: number }
  | ({ type: 'device-ready' | 'device-error' } & MockupResult)
  | { type: 'ready'; count: number }
  | { type: 'state' | 'heartbeat'; paused: boolean }
  | { type: 'error'; error: string }
  | { type: 'stopped' };

// Four fixed viewports use deviceScaleFactor=1. This also accommodates a
// worst-case 1920 x 1080 RGBA PNG without allowing an unbounded partial line.
export const maxMockupLineLength = 12 * 1024 * 1024;
const invalid = () => new Error('预览服务返回的数据无效，请重新连接。');
function record(input: unknown): Record<string, unknown> {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw invalid();
  return input as Record<string, unknown>;
}
function integer(input: unknown, min: number, max: number): number {
  if (typeof input !== 'number' || !Number.isSafeInteger(input) || input < min || input > max) throw invalid();
  return input;
}
function text(input: unknown, max = 16384): string {
  if (typeof input !== 'string' || input.length > max) throw invalid();
  return input;
}
function bool(input: unknown): boolean {
  if (typeof input !== 'boolean') throw invalid();
  return input;
}
export function mockupError(input: unknown, fallback: string): string {
  if (input instanceof Error) return input.message || fallback;
  if (!input || typeof input !== 'object' || Array.isArray(input)) return fallback;
  const value = input as Record<string, unknown>;
  if (typeof value.error === 'string' && value.error) return value.error.slice(0, 16384);
  if (value.error && typeof value.error === 'object' && 'message' in value.error && typeof value.error.message === 'string') return value.error.message.slice(0, 16384) || fallback;
  return fallback;
}
export function parseMockupHealth(input: unknown): boolean {
  const value = record(input);
  if (value.service !== 'device-mockup') throw invalid();
  return bool(value.ready);
}
export function parseMockupControl(input: unknown, expectedPaused: boolean): boolean {
  const value = record(input), paused = bool(value.paused), stopped = bool(value.stopped);
  if (stopped || paused !== expectedPaused) throw new Error('预览控制未确认，请重新连接。');
  return paused;
}
export function normalizedMockupURL(value: string): string | null {
  const raw = value.trim();
  if (!raw || raw.length > 2048) return null;
  const candidate = raw.includes('://') ? raw : /^(localhost|127\.0\.0\.1)(:|\/|$)/i.test(raw) ? 'http://' + raw : 'https://' + raw;
  try {
    const url = new URL(candidate);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.href.length > 2048) return null;
    return url.href;
  } catch { return null; }
}
export function parseMockupEvent(input: unknown): MockupEvent {
  const value = record(input);
  switch (value.type) {
    case 'session': {
      const id = text(value.id, 36);
      if (!/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/i.test(id)) throw invalid();
      return { type: 'session', id, interval: integer(value.interval, 1, 60000) };
    }
    case 'frame':
    case 'device-ready':
    case 'device-error': {
      const device = mockupDevices.find(item => item.id === value.id);
      if (!device) throw invalid();
      if (value.type === 'device-error') return { type: value.type, id: device.id, error: text(value.error) || '页面加载失败' };
      if (value.width !== device.width || value.height !== device.height) throw invalid();
      if (value.type === 'device-ready') {
        const finalUrl = new URL(text(value.finalUrl, 8192));
        if (!['http:', 'https:'].includes(finalUrl.protocol) || finalUrl.username || finalUrl.password) throw invalid();
        if (!Array.isArray(value.warnings) || value.warnings.length > 256) throw invalid();
        integer(value.blockedRequests, 0, Number.MAX_SAFE_INTEGER);
        return { type: value.type, id: device.id, title: text(value.title), status: value.status === null ? null : integer(value.status, 100, 599), warnings: value.warnings.map(item => text(item)) };
      }
      const screenshot = text(value.screenshot, maxMockupLineLength - 1024);
      if (screenshot.length < 44 || screenshot.length % 4 !== 0 || !/^[A-Za-z0-9+/]+={0,2}$/.test(screenshot)) throw invalid();
      const header = Uint8Array.from(atob(screenshot.slice(0, 44)), char => char.charCodeAt(0));
      const view = new DataView(header.buffer);
      // Mobile page scaling can round Chromium's screenshot by one device
      // pixel (for example, the 768 x 1024 tablet returns a 768 x 1023 PNG).
      if (view.getUint32(0) !== 0x89504e47 || view.getUint32(4) !== 0x0d0a1a0a || view.getUint32(8) !== 13 || view.getUint32(12) !== 0x49484452 || Math.abs(view.getUint32(16) - device.width) > 1 || Math.abs(view.getUint32(20) - device.height) > 1) throw invalid();
      return { type: value.type, id: device.id, screenshot, capturedAt: integer(value.capturedAt, 1, Number.MAX_SAFE_INTEGER) };
    }
    case 'ready': return { type: value.type, count: integer(value.count, 0, mockupDevices.length) };
    case 'state':
    case 'heartbeat': return { type: value.type, paused: bool(value.paused) };
    case 'error': return { type: value.type, error: text(value.error) || '预览连接已中断，请重新连接。' };
    case 'stopped': return { type: value.type };
    default: throw invalid();
  }
}
export function createMockupStream(onEvent: (event: MockupEvent) => void) {
  let buffer = '';
  return {
    push(chunk: string) {
      let start = 0;
      while (start < chunk.length) {
        const newline = chunk.indexOf('\n', start), end = newline < 0 ? chunk.length : newline;
        if (buffer.length + end - start > maxMockupLineLength) throw new Error('预览画面过大，请重新连接。');
        buffer += chunk.slice(start, end);
        if (newline < 0) break;
        const line = buffer.trim();
        buffer = '';
        if (line) onEvent(parseMockupEvent(JSON.parse(line) as unknown));
        start = end + 1;
      }
    },
    finish() {
      if (buffer.trim()) throw new Error('预览数据未接收完整，请重新连接。');
    },
  };
}
