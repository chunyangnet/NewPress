import { responseError } from './editor-tool-data';

export const make = <K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, className?: string) => {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
};
export async function readJSON(url: string, signal?: AbortSignal): Promise<unknown> {
  const controller=new AbortController();
  const abort=()=>controller.abort();
  if(signal?.aborted)abort();else signal?.addEventListener('abort',abort,{once:true});
  const timeout=setTimeout(abort,15000);
  try {
    const response = await fetch(url, {credentials:'same-origin',cache:'no-store',headers:{Accept:'application/json'},signal:controller.signal});
    let body: unknown;
    try {body=await response.json();} catch(error) {if(controller.signal.aborted)throw error;throw new Error('响应格式不正确，请重新登录后重试。');}
    if (controller.signal.aborted) throw new DOMException('Request interrupted', 'AbortError');
    if (!response.ok) throw new Error(responseError(body, '暂时无法读取，请重试。'));
    return body;
  } finally {clearTimeout(timeout);signal?.removeEventListener('abort',abort);}
}
