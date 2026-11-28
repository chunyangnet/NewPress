import { normalizeHistory, rememberDocument, historyKey } from './learning-state';
import { copyText, announce } from './shared';

export function initLearning() {
  const root = document.documentElement;
  document.querySelectorAll<HTMLButtonElement>('[data-favorite],[data-reaction],[data-follow]').forEach(button => {
    button.addEventListener('click', () => {
      const interactions = window.ChunnelInteractions, engagement = window.ChunnelEngagement;
      if (!interactions || !engagement) { announce('账号服务尚未就绪，请稍后重试'); return; }
      const kind = button.hasAttribute('data-follow') ? 'follow' : button.hasAttribute('data-reaction') ? 'like' : 'bookmark';
      interactions.run(() => interactions.signIn(() => engagement.toggle(button, kind)));
    });
  });
  const focus = document.querySelector<HTMLElement>('[data-docs-focus]');
  if (focus) {
    focus.hidden = false;
    const apply = (enabled: boolean) => {
      root.dataset.docsFocus = String(enabled);
      focus.setAttribute('aria-pressed', String(enabled));
      const label = focus.querySelector('span'); if (label) label.textContent = enabled ? '退出专注' : '专注阅读';
    };
    focus.addEventListener('click', () => apply(root.dataset.docsFocus !== 'true'));
    document.addEventListener('keydown', event => { if(event.key === 'Escape') apply(false); });
  }
  const print = document.querySelector<HTMLElement>('[data-docs-print]');
  if (print) { print.hidden = false; print.addEventListener('click', () => window.print()); }
  document.querySelectorAll('[data-share]').forEach(button => button.addEventListener('click', async () => {
    try {
      if (navigator.share) await navigator.share({ title:document.title, url:location.href });
      else { await copyText(location.href); announce('链接已复制'); }
    } catch(error) { if(!(error instanceof Error) || error.name !== 'AbortError') announce('分享未完成，请复制地址栏中的链接'); }
  }));
  const history = document.querySelector<HTMLElement>('[data-docs-history]');
  const read = () => { try { return normalizeHistory(JSON.parse(localStorage.getItem(historyKey) || 'null'), location.origin); } catch { return []; } };
  const record = document.querySelector<HTMLElement>('[data-docs-history-record]');
  if (record && root.dataset.appearancePreview !== 'true') {
    try { localStorage.setItem(historyKey, JSON.stringify(rememberDocument(read(), {url:record.dataset.url || '',title:record.dataset.title || '',at:Date.now()}, location.origin))); } catch { /* Reading works when storage is unavailable. */ }
  }
  if (!history) return;
  const render = () => {
    const items = read().slice(0,3), list = history.querySelector('[data-docs-history-list]');
    if (!list) return; list.replaceChildren(); history.hidden = items.length === 0;
    for(const item of items) {
      const li=document.createElement('li'), a=document.createElement('a'), title=document.createElement('strong'), time=document.createElement('small');
      a.href=item.url; title.textContent=item.title; time.textContent=new Date(item.at).toLocaleDateString(root.lang||'zh-CN'); a.append(title,time); li.append(a); list.append(li);
    }
  }
  history.querySelector('[data-docs-history-clear]')?.addEventListener('click', () => {
    try { localStorage.removeItem(historyKey); render(); announce('已清除当前浏览器的阅读记录'); }
    catch { announce('浏览器未允许清除存储，请在浏览器设置中清理'); }
  });
  window.addEventListener('storage', event => { if(event.key === historyKey || event.key === null) render(); });
  render();
}
