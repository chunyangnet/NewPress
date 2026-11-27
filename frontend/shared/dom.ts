export function node<K extends keyof HTMLElementTagNameMap>(tag: K, className = '', text?: string | null): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = text;
  return element;
}
