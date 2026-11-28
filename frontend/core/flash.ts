import { mountFlashEditor } from './flash-editor';
import { mountFlashEngagement } from './flash-engagement';

const status = document.querySelector<HTMLElement>('[data-flash-status]');
const tell = (text: string) => { if (status) status.textContent = text; };
const form = document.querySelector<HTMLFormElement>('[data-flash-editor]');
if (form) { try { mountFlashEditor(form, tell); } catch (error) { tell(error instanceof Error ? error.message : '快报页面暂不可用'); } }
mountFlashEngagement(tell);
for (const body of document.querySelectorAll<HTMLElement>('.flash-timeline .flash-body')) {
  if ((body.textContent || '').length < 240 || body.dataset.flashCollapse === 'true') continue;
  body.dataset.flashCollapse = 'true';
  const button = document.createElement('button'); button.type = 'button'; button.className = 'flash-more'; button.textContent = '展开正文'; button.setAttribute('aria-expanded', 'false');
  const collapse = () => { body.style.maxHeight = '9.25em'; body.style.overflow = 'hidden'; }; collapse();
  button.addEventListener('click', () => {
    const open = button.getAttribute('aria-expanded') === 'true'; button.setAttribute('aria-expanded', String(!open)); button.textContent = open ? '展开正文' : '收起正文';
    if (open) collapse(); else { body.style.maxHeight = 'none'; body.style.overflow = 'visible'; }
  }); body.after(button);
}
