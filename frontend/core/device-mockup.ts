import { mockupDevices as devices, mockupError, normalizedMockupURL as normalizedUrl, parseMockupHealth, parseMockupControl, createMockupStream } from './device-mockup-data';
import type { MockupDevice, MockupDeviceID, MockupResult, MockupEvent } from './device-mockup-data';
interface LiveSession {
  controller: AbortController;
  results: Map<MockupDeviceID, MockupResult>;
  frames: Map<MockupDeviceID, number>;
  userPaused: boolean;
  serverPaused: boolean;
  lastEvent: number;
  id?: string;
  initialized?: boolean;
  controlPending?: boolean;
  controlQueue?: Promise<void>;
}
(() => {
      'use strict';
      const foundRoot = document.querySelector<HTMLElement>('[data-device-mockup]');
      if (!foundRoot) return;
      const root = foundRoot;
      const apiBase = '/api/v1/admin/tools/device-mockup/';
      const csrfToken = root.dataset.csrf || '';
      function $<T extends Element = HTMLElement>(id: string): T {
        const element = root!.querySelector<T>('#' + id);
        if (!element) throw new Error('设备预览缺少控件：' + id);
        return element;
      }
      const NS = 'http://www.w3.org/2000/svg';
      const captures = new Map<MockupDeviceID, string>();
      const objectUrls = new Set<string>();
      let busy = false;
      let filePrefix = 'website-demo';
      let timer: ReturnType<typeof setInterval> | undefined;
      let currentAddress = '';
      let live: LiveSession | null = null;
      let pageActive = true;
      let healthRequest = 0;
      let exporting = false;
      const downloading = new Set<MockupDeviceID>();

      function svgUrl(markup: string) {
        const bytes = new TextEncoder().encode(markup);
        let binary = '';
        for (const byte of bytes) binary += String.fromCharCode(byte);
        return 'data:image/svg+xml;base64,' + btoa(binary);
      }

      // A self-contained illustration keeps the initial demo crisp at every size.
      function demoScreen(width: number, height: number) {
        const phone = width < 500;
        const tablet = width >= 500 && width < 1000;
        const compact = phone || tablet;
        const unit = phone ? width / 390 : tablet ? width / 768 : width / 1440;
        const w = width / unit, h = height / unit;
        const margin = phone ? 24 : tablet ? 42 : 70;
        const navH = phone ? 76 : 100;
        const sceneY = phone ? 297 : tablet ? 355 : 138;
        const sceneX = compact ? margin : 740;
        const sceneW = phone ? w - margin * 2 : tablet ? w - margin * 2 : 630;
        const sceneH = phone ? 265 : tablet ? 470 : 560;
        const titleSize = phone ? 47 : tablet ? 71 : 79;
        const titleY = phone ? 141 : tablet ? 182 : 261;
        const contentY = compact ? sceneY + sceneH + 35 : 775;
        const scene = '<g transform="translate(' + sceneX + ' ' + sceneY + ') scale(' + sceneW / 630 + ' ' + sceneH / 560 + ')">' +
          '<rect width="630" height="560" rx="2" fill="#d5d8c4"/><path d="M0 560V0h630v560" fill="url(#wall)"/>' +
          '<path d="M105 560V203a210 210 0 0 1 420 0v357" fill="#bcc5a6"/><path d="M124 560V204a191 191 0 0 1 382 0v356" fill="url(#arch)"/>' +
          '<path d="M0 420 630 306v254H0" fill="#c5c7b0"/><path d="m0 420 236 140h394V415L407 346" fill="#b7bca3" opacity=".6"/>' +
          '<ellipse cx="334" cy="509" rx="175" ry="24" fill="#686e52" opacity=".18"/><path d="m171 367 175-38 128 53-175 43Z" fill="#eee7d7"/><path d="M171 367v127l128 66V425Z" fill="#d8ccb8"/><path d="M299 425v135l175-63V382Z" fill="#b9ad98"/>' +
          '<ellipse cx="328" cy="365" rx="90" ry="24" fill="#676d43" opacity=".22"/><path d="M292 235c-29 14-49 54-42 88 7 40 44 61 79 61 43 0 80-32 77-71-2-34-20-64-45-79l-4-28h-61Z" fill="url(#vase)"/><ellipse cx="326" cy="205" rx="31" ry="9" fill="#747b4e"/><ellipse cx="326" cy="205" rx="24" ry="5" fill="#303d27"/>' +
          '<path d="M327 204c0-70 8-119 36-161M328 155c-31-17-42-51-56-82m66 57c42-14 59-36 76-63" fill="none" stroke="#4c6242" stroke-width="3"/>' +
          '<path d="M350 71c-7-41 15-58 40-59-1 31-15 48-40 59ZM333 132c-33-24-40-52-26-77 20 21 32 47 26 77ZM279 86c-33-4-52-26-53-54 32 5 50 24 53 54ZM369 112c22-35 48-47 74-39-16 25-39 38-74 39Z" fill="#6c8058"/>' +
          '<path d="M426 0 630 0v369L195 560h-87Z" fill="#fff9d8" opacity=".1"/><rect width="630" height="560" rx="2" fill="none" stroke="#b2b7a1" stroke-opacity=".2"/></g>';
        const nav = '<text x="' + margin + '" y="' + (phone ? 44 : 58) + '" font-family="Georgia,serif" font-size="' + (phone ? 24 : 29) + '" letter-spacing="-1" fill="#333a2a">forma.</text>' +
          (phone ? '<path d="M338 33h25m-25 7h25" stroke="#4d5245" stroke-width="1.5"/>' :
            '<g fill="#838775" font-size="12"><text x="' + (w / 2 - 94) + '" y="55">Collection</text><text x="' + (w / 2 + 14) + '" y="55">Our story</text><text x="' + (w - margin - 54) + '" y="55">Explore</text></g>');
        const hero = '<text x="' + margin + '" y="' + (titleY - (phone ? 48 : tablet ? 72 : 85)) + '" font-size="' + (phone ? 8 : 10) + '" letter-spacing="2.5" fill="#92947e">OBJECTS FOR A SLOWER LIFE</text>' +
          '<g font-family="Georgia,serif" font-size="' + titleSize + '" letter-spacing="-2.5" fill="#363e2b"><text x="' + margin + '" y="' + titleY + '">Less, but</text><text x="' + margin + '" y="' + (titleY + titleSize * 1.05) + '">better.</text></g>' +
          (!compact ? '<text x="' + margin + '" y="493" font-size="16" fill="#91917c">Thoughtfully made. Quietly beautiful.</text><text x="' + margin + '" y="520" font-size="16" fill="#91917c">A little room for what matters.</text><rect x="' + margin + '" y="567" width="170" height="48" rx="24" fill="#576247"/><text x="' + (margin + 28) + '" y="596" font-size="12" fill="#f5f4e9">Discover collection</text>' :
            '<text x="' + margin + '" y="' + (phone ? 226 : 283) + '" font-size="' + (phone ? 10 : 14) + '" fill="#91917c">Thoughtfully made. Quietly beautiful.</text><text x="' + margin + '" y="' + (phone ? 262 : 326) + '" font-size="' + (phone ? 10 : 13) + '" fill="#586646">Discover collection</text>');
        const lower = '<text x="' + margin + '" y="' + contentY + '" font-size="' + (phone ? 8 : 10) + '" letter-spacing="2" fill="#92927d">MADE TO FEEL AT HOME</text>' +
          '<text x="' + margin + '" y="' + (contentY + (phone ? 37 : 49)) + '" font-family="Georgia,serif" font-size="' + (phone ? 27 : 43) + '" fill="#434a35">Everyday, considered.</text>' +
          '<g transform="translate(' + margin + ' ' + (contentY + 79) + ')"><rect width="' + ((w - margin * 2 - 30) / 2) + '" height="175" fill="#e9e4d7"/><rect x="' + ((w - margin * 2 + 30) / 2) + '" width="' + ((w - margin * 2 - 30) / 2) + '" height="175" fill="#dce0d0"/></g>';
        return svgUrl('<svg xmlns="' + NS + '" width="' + width + '" height="' + height + '" viewBox="0 0 ' + w + ' ' + h + '"><defs><linearGradient id="wall"><stop stop-color="#e1e2d1"/><stop offset="1" stop-color="#c4ccb1"/></linearGradient><linearGradient id="arch" x1="0" x2="1" y2=".6"><stop stop-color="#9eae8d"/><stop offset="1" stop-color="#d1d4bc"/></linearGradient><linearGradient id="vase"><stop stop-color="#425532"/><stop offset=".45" stop-color="#71815a"/><stop offset="1" stop-color="#35492b"/></linearGradient></defs><rect width="' + w + '" height="' + h + '" fill="#f7f6ef"/><g font-family="Verdana,sans-serif">' + nav + '<path d="M' + margin + ' ' + navH + 'H' + (w - margin) + '" stroke="#dedfd2" stroke-width="1"/>' + scene + hero + lower + '</g></svg>');
      }

      function stateScreen(device: MockupDevice, text: string) {
        return svgUrl('<svg xmlns="' + NS + '" width="' + device.width + '" height="' + device.height + '"><rect width="100%" height="100%" fill="#f7f5ef"/><text x="50%" y="50%" text-anchor="middle" fill="#aaa18f" font-family="Microsoft YaHei,sans-serif" font-size="' + (device.id === 'phone' ? 18 : device.id === 'tablet' ? 25 : 36) + '">' + text + '</text></svg>');
      }

      function buildDeviceList() {
        for (const device of devices) {
          const item = document.createElement('div');
          item.className = 'dm-device-item';
          item.dataset.device = device.id;
          item.dataset.state = 'idle';
          item.innerHTML = '<div class="dm-device-icon"><svg class="dm-icon" aria-hidden="true"><use href="#dm-i-' + device.icon + '"/></svg></div><div class="dm-device-info"><div class="dm-device-name">' + device.label + '<i class="dm-device-state" aria-hidden="true"></i></div><div class="dm-device-size">' + device.width + ' × ' + device.height + '</div><span class="dm-readout-status">示例画面</span></div><button class="dm-device-download" type="button" aria-label="下载' + device.label + ' PNG" title="下载' + device.label + '"><svg class="dm-icon" aria-hidden="true"><use href="#dm-i-download"/></svg></button>';
          const button = item.querySelector('button')!;
          button.addEventListener('click', () => downloadDevice(device, button));
          $('deviceList').append(item);
        }
      }

      function setDeviceState(device: MockupDevice, state: string, text: string) {
        const item = root.querySelector<HTMLElement>('.dm-device-item[data-device="' + device.id + '"]');
        if (!item) return;
        item.dataset.state = state;
        item.querySelector<HTMLElement>('.dm-readout-status')!.textContent = text;
        item.title = device.label + ' · ' + text;
        item.querySelector('button')!.disabled = busy || downloading.has(device.id) || !captures.has(device.id);
      }

      function setService(ready: boolean, text: string) {
        $('serviceState').dataset.ready = String(ready);
        $('serviceText').textContent = text;
      }

      const apiError = mockupError;

      async function checkService() {
        const request = ++healthRequest;
        if (location.protocol === 'file:') {
          $('serviceNotice').hidden = false;
          setService(false, '服务未连接');
          return;
        }
        try {
          const response = await fetch(apiBase + 'health', { credentials: 'same-origin', cache: 'no-store', signal: AbortSignal.timeout(5000) });
          const data: unknown = await response.json();
          if (request !== healthRequest || !pageActive || live) return;
          if (!response.ok) showMessage(apiError(data, '无法检查截图服务，请重新登录后台。'));
          const ready = response.ok && parseMockupHealth(data);
          setService(ready, ready ? '渲染服务已就绪' : '服务未就绪');
          $('serviceNotice').hidden = ready;
        } catch {
          if (request !== healthRequest || !pageActive || live) return;
          $('serviceNotice').hidden = false;
          setService(false, '服务未连接');
        }
      }


      function showMessage(text: string, kind = 'error') {
        $('renderMessage').textContent = text;
        $('renderMessage').dataset.kind = kind;
        $('renderMessage').hidden = !text;
      }

      function setBackground(color: string, name: string) {
        $('stageBackground').setAttribute('fill', color);
        $('stageViewport').style.background = color;
        $('backgroundName').textContent = name;
        $<HTMLInputElement>('customColor').value = color;
        root.querySelectorAll<HTMLElement>('.dm-swatch').forEach((button) => button.setAttribute('aria-pressed', String(button.dataset.color === color)));
      }

      function exportScale(): number {
        const scale = Number($<HTMLSelectElement>('exportScale').value);
        return [1, 2, 3].includes(scale) ? scale : 2;
      }

      function safeName() {
        return filePrefix.replace(/[\\/:*?"<>|\u0000-\u001f]+/g, '-').replace(/\s+/g, '-').slice(0, 60) || 'website';
      }

      function saveBlob(blob: Blob, name: string) {
        if (!pageActive) return;
        const href = URL.createObjectURL(blob);
        objectUrls.add(href);
        const link = document.createElement('a');
        link.href = href; link.download = name;
        document.body.append(link); link.click(); link.remove();
        setTimeout(() => { URL.revokeObjectURL(href); objectUrls.delete(href); }, 30000);
      }

      function rasterize(source: string, width: number, height: number): Promise<Blob> {
        return new Promise<Blob>((resolve, reject) => {
          const image = new Image();
          const timeout = setTimeout(() => { image.onload = image.onerror = null; image.src = ""; reject(new Error("图片导出超时，请重新尝试。")); }, 30000);
          image.onload = () => {
            try {
              const canvas = document.createElement('canvas');
              canvas.width = width; canvas.height = height;
              const context = canvas.getContext('2d');
              if (!context) throw new Error('浏览器无法创建图片画布。');
              context.drawImage(image, 0, 0, width, height);
              canvas.toBlob((blob) => { clearTimeout(timeout); blob ? resolve(blob) : reject(new Error('PNG 生成失败，请重试。')); }, 'image/png');
            } catch (error) { clearTimeout(timeout); reject(error); }
          };
          image.onerror = () => { clearTimeout(timeout); reject(new Error('图片加载失败，请重新生成截图。')); };
          image.src = source;
        });
      }

      async function exportSvg(device?: MockupDevice) {
        const svg = $<SVGSVGElement>('composition');
        const scale = exportScale();
        let copy: SVGSVGElement, width: number, height: number;
        if (device) {
          const group = $<SVGGElement>(device.group);
          const bounds = group.getBBox();
          const pad = 42;
          copy = document.createElementNS(NS, 'svg');
          copy.setAttribute('viewBox', [bounds.x - pad, bounds.y - pad, bounds.width + pad * 2, bounds.height + pad * 2].join(' '));
          copy.append(svg.querySelector('defs')!.cloneNode(true), group.cloneNode(true));
          width = Math.ceil((bounds.width + pad * 2) * scale);
          height = Math.ceil((bounds.height + pad * 2) * scale);
        } else {
          copy = svg.cloneNode(true) as SVGSVGElement;
          width = 1600 * scale; height = 1000 * scale;
        }
        copy.removeAttribute('class'); copy.removeAttribute('role');
        copy.setAttribute('width', String(width)); copy.setAttribute('height', String(height));
        return rasterize(svgUrl(new XMLSerializer().serializeToString(copy)), width, height);
      }

      async function downloadDevice(device: MockupDevice, button: HTMLButtonElement) {
        if (busy || downloading.has(device.id) || !captures.has(device.id)) return;
        downloading.add(device.id);
        button.disabled = true;
        try {
          const framed = $<HTMLInputElement>('includeFrame').checked;
          const name = safeName() + '-' + device.id + (framed ? '-framed' : '') + '.png';
          const blob = framed ? await exportSvg(device) : await rasterize(captures.get(device.id)!, device.width, device.height);
          saveBlob(blob, name);
        } catch (error) { showMessage(mockupError(error, '导出失败，请重新尝试。')); }
        finally { downloading.delete(device.id); button.disabled = busy || !captures.has(device.id); }
      }

      $<HTMLButtonElement>('saveCompositeButton').addEventListener('click', async () => {
        if (busy || exporting || captures.size === 0) return;
        exporting = true;
        const button = $<HTMLButtonElement>('saveCompositeButton');
        button.disabled = true;
        const text = button.querySelector('span')!;
        text.textContent = '正在导出…';
        const name = safeName() + '-four-devices.png';
        try { saveBlob(await exportSvg(), name); }
        catch (error) { showMessage(mockupError(error, '导出失败，请重新尝试。')); }
        finally { exporting = false; button.disabled = busy || captures.size === 0; text.textContent = '导出合成图'; }
      });

      $('backgroundPicker').addEventListener('click', (event) => {
        const button = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-color]') : null;
        if (button) setBackground(button.dataset.color || '#f4f1ec', button.dataset.name || '自定义');
      });
      $<HTMLInputElement>('customColor').addEventListener('input', (event) => setBackground($<HTMLInputElement>('customColor').value, '自定义'));
      $('finishPicker').addEventListener('click', (event) => {
        const button = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-finish]') : null;
        if (!button) return;
        const colors = button.dataset.finish === 'silver' ? ['#f0f1f2', '#adb5ba', '#e1e5e8', '#959fa8'] : ['#4c5154', '#232629', '#444a4d', '#22272a'];
        $('frameMetal').querySelectorAll('stop').forEach((stop, index) => stop.setAttribute('stop-color', colors[index] || '#232629'));
        // Keep the glass bezel dark while changing the metal edge of each device.
        const rim = button.dataset.finish === 'silver' ? ['#eef0f2', '#c0c8ce', '#f1f3f5', '#aab4bc', '#e0e5e9'] : ['#b3b7b8', '#6f7476', '#b4b8b9', '#555b5e', '#a3a9aa'];
        $('rim').querySelectorAll('stop').forEach((stop, index) => stop.setAttribute('stop-color', rim[index] || '#555b5e'));
        root.querySelectorAll('[data-finish]').forEach((item) => item.setAttribute('aria-pressed', String(item === button)));
      });
      $<HTMLSelectElement>('exportScale').addEventListener('change', () => {
        const scale = Number($<HTMLSelectElement>('exportScale').value);
        $('exportDimensions').textContent = (1600 * scale) + ' × ' + (1000 * scale) + ' px · 无水印';
      });
      $('zoomButton').addEventListener('click', (event) => {
        const zoomed = $('stageViewport').classList.toggle('is-zoomed');
        $<HTMLButtonElement>('zoomButton').setAttribute('aria-pressed', String(zoomed));
        $<HTMLButtonElement>('zoomButton').setAttribute('aria-label', zoomed ? '适合窗口' : '放大画布');
        $<HTMLButtonElement>('zoomButton').title = zoomed ? '适合窗口' : '放大画布';
      });
      $('helpButton').addEventListener('click', () => $<HTMLDialogElement>('helpDialog').showModal());
      $('closeHelpButton').addEventListener('click', () => $<HTMLDialogElement>('helpDialog').close());
      $<HTMLDialogElement>('helpDialog').addEventListener('click', (event) => { if (event.target === $<HTMLDialogElement>('helpDialog')) $<HTMLDialogElement>('helpDialog').close(); });

      function releaseLive() {
        clearInterval(timer);
        if (live) live.controller.abort();
        live = null;
      }

      function updateLiveUi(session: LiveSession) {
        if (live !== session) return;
        const paused = session.userPaused || document.hidden;
        $('previewBadge').dataset.mode = busy ? 'loading' : paused ? 'paused' : 'live';
        $('previewBadge').textContent = busy ? '正在载入' : paused ? '已暂停' : '实时预览';
        $<HTMLButtonElement>('pauseLiveButton').disabled = busy || !!session.controlPending;
        $<HTMLButtonElement>('pauseLiveButton').setAttribute('aria-pressed', String(session.userPaused));
        $<HTMLButtonElement>('pauseLiveButton').setAttribute('aria-label', session.userPaused ? '继续实时预览' : '暂停实时预览');
        $('pauseLiveText').textContent = session.userPaused ? '继续' : '暂停';
        $('pauseLiveIcon').setAttribute('href', session.userPaused ? '#dm-i-play' : '#dm-i-pause');
        $('captureTitle').textContent = new URL(currentAddress).host + ' · ' + captures.size + ' 个视口 · ' + (busy ? '正在载入' : paused ? '画面已暂停' : '约每秒更新');
        $('outputSummary').textContent = captures.size + ' / 4 个视口' + (busy ? '已载入' : paused ? '已暂停' : '持续预览中');
        $<HTMLButtonElement>('saveCompositeButton').disabled = busy || exporting || captures.size === 0;
        for (const device of devices) {
          const result = session.results.get(device.id);
          if (result?.error) setDeviceState(device, 'error', result.error);
          else if (!captures.has(device.id)) setDeviceState(device, 'loading', '正在加载');
          else if ((result?.status ?? 0) >= 400) setDeviceState(device, 'error', 'HTTP ' + result!.status);
          else setDeviceState(device, busy ? 'loading' : paused ? 'paused' : 'ready', busy ? '已载入，等待稳定' : paused ? '画面已暂停' : '实时预览');
        }
      }

      function finishLive(message = '') {
        const session = live;
        if (!session) return;
        releaseLive(); busy = false;
        setService(true, '渲染服务已就绪');
        $('stageLoading').hidden = true;
        $<HTMLButtonElement>('pauseLiveButton').hidden = true; $<HTMLButtonElement>('stopLiveButton').hidden = true;
        $('renderButtonText').textContent = '重新连接预览';
        $('previewBadge').dataset.mode = 'paused';
        $('previewBadge').textContent = captures.size ? '已定格' : '未连接';
        $('captureTitle').textContent = captures.size ? new URL(currentAddress).host + ' · 已保留最后画面' : '预览已结束，可输入网址重新连接';
        $('outputSummary').textContent = captures.size ? captures.size + ' / 4 个视口可导出' : '尚未载入网站';
        $<HTMLButtonElement>('saveCompositeButton').disabled = exporting || captures.size === 0;
        for (const device of devices) {
          if (captures.has(device.id)) setDeviceState(device, 'paused', '已保留最后画面');
          else {
            $('image-' + device.id).setAttribute('href', stateScreen(device, message ? '载入失败' : '预览已结束'));
            setDeviceState(device, 'error', session.results.get(device.id)?.error || (message ? '载入失败' : '预览已结束'));
          }
        }
        if (message) showMessage(message);
      }

      function syncPause(session: LiveSession) {
        session.controlQueue = (session.controlQueue || Promise.resolve()).then(async () => {
          if (live !== session || !session.id) return;
          const paused = session.userPaused || document.hidden;
          if (session.serverPaused === paused) return;
          const response = await fetch(apiBase + 'live/control', {
            method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
            body: JSON.stringify({ id: session.id, action: paused ? 'pause' : 'resume' }),
            signal: AbortSignal.any([session.controller.signal, AbortSignal.timeout(5000)]),
          });
          const result: unknown = await response.json();
          if (live !== session) return;
          if (!response.ok) throw new Error(apiError(result, '预览控制失败，请重新连接。'));
          session.serverPaused = parseMockupControl(result, paused);
        }).catch((error) => {
          if (live === session) finishLive(mockupError(error, '预览连接已中断，请重新连接。'));
        });
        return session.controlQueue;
      }

      $<HTMLButtonElement>('pauseLiveButton').addEventListener('click', async () => {
        const session = live;
        if (!session || busy || session.controlPending) return;
        session.userPaused = !session.userPaused;
        session.controlPending = true;
        updateLiveUi(session);
        await syncPause(session);
        session.controlPending = false;
        updateLiveUi(session);
      });
      $<HTMLButtonElement>('stopLiveButton').addEventListener('click', () => finishLive());
      document.addEventListener('visibilitychange', () => {
        if (!live) return;
        updateLiveUi(live);
        void syncPause(live);
      });

      function handleLiveEvent(session: LiveSession, item: MockupEvent) {
        if (live !== session) return;
        session.lastEvent = performance.now();
        if (item.type !== 'session' && !session.id) throw new Error('预览会话未确认，请重新连接。');
        if (item.type === 'session') {
          if (session.id) throw new Error('预览会话发生变化，请重新连接。');
          session.id = item.id;
          setService(true, '实时预览已连接');
          $('serviceNotice').hidden = true;
          void syncPause(session);
        } else if (item.type === 'frame') {
          const device = devices.find((device) => device.id === item.id);
          if (!device || session.userPaused || document.hidden) return;
          if (item.capturedAt <= (session.frames.get(device.id) ?? 0)) return;
          session.frames.set(device.id, item.capturedAt);
          const first = !captures.has(device.id);
          const source = 'data:image/png;base64,' + item.screenshot;
          captures.set(device.id, source);
          $('image-' + device.id).setAttribute('href', source);
          $('image-' + device.id).dataset.frameAt = String(item.capturedAt);
          if (first) updateLiveUi(session);
        } else if (item.type === 'device-ready' || item.type === 'device-error') {
          session.results.set(item.id, item);
          if (item.error) {
            captures.delete(item.id);
            $('image-' + item.id).setAttribute('href', stateScreen(devices.find((device) => device.id === item.id)!, '载入失败'));
          }
          const warnings = new Set<string>();
          for (const device of devices) {
            const result = session.results.get(device.id);
            if (result?.error) warnings.add(device.label + '：' + result.error);
            for (const warning of result?.warnings || []) warnings.add(warning);
          }
          if (item.title) $('captureDetail').textContent = item.title;
          showMessage([...warnings].join(' '), 'info');
          updateLiveUi(session);
        } else if (item.type === 'ready') {
          if (session.initialized || session.results.size !== devices.length || [...session.results.values()].filter(result => !result.error).length !== item.count) throw new Error('设备加载结果不完整，请重新连接。');
          busy = false; session.initialized = true;
          $('stageLoading').hidden = true;
          updateLiveUi(session);
        } else if (item.type === 'state' || item.type === 'heartbeat') {
          session.serverPaused = item.paused;
        } else if (item.type === 'error') {
          throw new Error(item.error || '实时预览已中断，请重新连接。');
        } else if (item.type === 'stopped') finishLive();
      }

      $('captureForm').addEventListener('submit', async (event) => {
        event.preventDefault();
        const input = $<HTMLInputElement>('siteUrl');
        const address = normalizedUrl(input.value);
        if (!address) { input.setCustomValidity('请输入有效的 HTTP 或 HTTPS 网站地址，不要包含账号密码。'); input.reportValidity(); return; }
        if (location.protocol === 'file:') { $('serviceNotice').hidden = false; return; }
        if (!csrfToken) { showMessage('管理员会话已失效，请重新登录后台。'); return; }
        input.setCustomValidity(''); input.value = address;
        releaseLive();
        const session: LiveSession = { controller: new AbortController(), results: new Map(), frames: new Map(), userPaused: false, serverPaused: false, lastEvent: performance.now() };
        live = session; currentAddress = address; filePrefix = new URL(address).hostname;
        busy = true; captures.clear();
        $('stageLoading').hidden = false;
        $<HTMLButtonElement>('pauseLiveButton').hidden = false; $<HTMLButtonElement>('stopLiveButton').hidden = false;
        $('renderButtonText').textContent = '重新连接预览';
        $('captureDetail').textContent = '';
        $('loadingText').textContent = '正在打开网站…';
        showMessage('');
        for (const device of devices) {
          $('image-' + device.id).setAttribute('href', stateScreen(device, '正在加载'));
          delete $('image-' + device.id).dataset.frameAt;
        }
        updateLiveUi(session);
        const started = performance.now();
        timer = setInterval(() => {
          if (live !== session) return;
          if (busy) $('loadingText').textContent = captures.size + ' / 4 个视口已载入 · ' + Math.floor((performance.now() - started) / 1000) + ' 秒';
          const now = performance.now();
          if ((!session.initialized && now - started > 90000) || now - session.lastEvent > 30000) {
            finishLive('预览连接超时，请检查网络后重新连接。');
          }
        }, 1000);
        try {
          const response = await fetch(apiBase + 'live', {
            method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
            body: JSON.stringify({ url: address, waitMs: Number($<HTMLSelectElement>('loadDelay').value) * 1000 }),
            signal: session.controller.signal,
          });
          if (!response.ok) {
            const payload = await response.json();
            throw new Error(apiError(payload, '网站无法载入，请检查网址。'));
          }
          if (live !== session) { await response.body?.cancel(); return; }
          if (!/^application\/x-ndjson(?:\s*;|$)/i.test(response.headers.get('Content-Type') || '') || !response.body) throw new Error('预览流格式无效，请重新连接。');
          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          const stream = createMockupStream(item => handleLiveEvent(session, item));
          try {
            while (live === session) {
              const { done, value } = await reader.read();
              if (live !== session) break;
              if (done) { stream.push(decoder.decode()); stream.finish(); throw new Error('实时预览连接已结束，请重新连接。'); }
              stream.push(decoder.decode(value, { stream: true }));
            }
          } finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
        } catch (error) {
          if (live === session) {
            finishLive(error instanceof TypeError ? '无法连接预览服务，请确认服务正在运行。' : mockupError(error, '网站无法加载，请检查地址与网络。'));
            void checkService();
          }
        }
      });
      $<HTMLInputElement>('siteUrl').addEventListener('input', () => $<HTMLInputElement>('siteUrl').setCustomValidity(''));
      buildDeviceList();
      for (const device of devices) {
        const source = demoScreen(device.width, device.height);
        $('image-' + device.id).setAttribute('href', source);
        captures.set(device.id, source);
      }
      $<HTMLButtonElement>('saveCompositeButton').disabled = false;
      const initialUrl = new URLSearchParams(location.search).get('url');
      if (initialUrl && normalizedUrl(initialUrl)) $<HTMLInputElement>('siteUrl').value = initialUrl;
      window.addEventListener('pagehide', () => { pageActive = false; healthRequest++; finishLive(); objectUrls.forEach((url) => URL.revokeObjectURL(url)); objectUrls.clear(); });
      window.addEventListener('pageshow', event => { if (event.persisted) { pageActive = true; void checkService(); } });
      checkService();
    })();
