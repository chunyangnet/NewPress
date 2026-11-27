import { required } from './account-dom';
import type { ExperienceDependencies } from './experience-types';

// Both route entries use the same public dependencies and DOM helpers.
export function experienceContext(): ExperienceDependencies | null {
  if (!window.Chunnel || !window.ChunnelInteractions || !window.ChunnelEmoji) return null;
  const { showToast, createIcon, setOverlay } = window.Chunnel;
  const $ = <T extends Element = HTMLElement>(selector: string, root: ParentNode = document): T => required<T>(root, selector);
  const optional = <T extends Element = HTMLElement>(selector: string, root: ParentNode = document): T | null => root.querySelector<T>(selector);
  const $$ = <T extends Element = HTMLElement>(selector: string, root: ParentNode = document): T[] => [...root.querySelectorAll<T>(selector)];
  const node = <K extends keyof HTMLElementTagNameMap>(tag: K, className = '', text = '') => { const element = document.createElement(tag); element.className = className; element.textContent = text; return element; };
  return { ...window.ChunnelInteractions, showToast, createIcon, setOverlay, emoji: window.ChunnelEmoji, dom: { $, $$, optional, node } };
}
