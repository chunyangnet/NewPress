// Runs before styles so saved appearance is applied before the first paint.
import type { ColorMode, ColorModeAPI, ColorModeChange, ColorTheme } from './color-types';

(() => {
  'use strict';
  const root = document.documentElement;
  const key = 'newpress.colorMode';
  const system = window.matchMedia?.('(prefers-color-scheme: dark)');
  const compact = window.matchMedia?.('(max-width: 760px)');
  const valid = (value: unknown): ColorMode => value === 'dark' || value === 'light' ? value : 'system';
  let mode: ColorMode = 'system';
  let revision = 0;
  const read = () => { try { mode = valid(localStorage.getItem(key)); } catch { /* Retain session preference. */ } };
  read();
  const themeColor = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]');
  const lightColor = themeColor?.content || '#f7f9fc';
  const controls = () => document.querySelectorAll<HTMLElement>('[data-color-mode-control]');
  const resolveTheme = (): ColorTheme => mode === 'system' ? (system?.matches ? 'dark' : 'light') : mode;
  const apply = () => {
    const theme = resolveTheme();
    root.dataset.theme = theme;
    root.dataset.colorMode = mode;
    if (themeColor) themeColor.content = theme === 'dark' ? '#111721' : lightColor;
    controls().forEach(control => {
      const focused = control.contains(document.activeElement);
      control.hidden = false;
      control.querySelectorAll<HTMLButtonElement>('[data-color-mode]').forEach(button => {
        const selected = button.dataset.colorMode === theme;
        button.setAttribute('aria-pressed', String(selected));
        if (focused && compact?.matches && !selected) button.focus({ preventScroll: true });
      });
    });
    document.dispatchEvent(new CustomEvent<ColorModeChange>('chunnel:color-mode', { detail: { mode, theme } }));
  };
  const sync = (origin?: HTMLElement) => {
    if (window.ChunnelColorReveal) window.ChunnelColorReveal(root.dataset.theme !== resolveTheme() && origin, apply);
    else apply();
  };
  const set = (value: unknown, origin?: HTMLElement, ready?: Promise<void> | false | null) => {
    mode = valid(value);
    const current = ++revision;
    try { localStorage.setItem(key, mode); } catch { /* Keep controls usable in restricted browsers. */ }
    if (ready) ready.then(() => { if (current === revision) sync(origin); });
    else sync(origin);
  };
  const api: ColorModeAPI = { set: value => set(value), get mode() { return mode; }, get theme() { return root.dataset.theme; } };
  window.ChunnelColorMode = Object.freeze(api);
  apply();
  const systemChanged = () => { if (mode === 'system') sync(); };
  if (system?.addEventListener) system.addEventListener('change', systemChanged);
  else system?.addListener?.(systemChanged);
  const reload = () => { revision++; read(); sync(); };
  window.addEventListener('storage', event => {
    if (event.key === key || event.key === null) reload();
  });
  window.addEventListener('pageshow', event => { if (event.persisted) reload(); });
  const bind = () => {
    apply();
    let ready: Promise<void> | null | undefined;
    const prepare = (control: HTMLElement) => {
      const entry = control.dataset.colorModeControl;
      if (!entry || window.ChunnelColorReveal || !document.startViewTransition || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;
      if (!ready) ready = new Promise<void>(resolve => {
        const script = document.createElement('script');
        const timer = setTimeout(resolve, 200);
        script.src = entry;
        script.onload = () => { clearTimeout(timer); resolve(); };
        script.onerror = () => { clearTimeout(timer); script.remove(); ready = null; resolve(); };
        document.head.append(script);
      });
      return ready;
    };
    controls().forEach(control => {
      const preload = () => prepare(control);
      control.addEventListener('pointerdown', preload);
      control.addEventListener('focusin', preload);
      control.addEventListener('click', event => {
        const button = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('button[data-color-mode]') : null;
        if (!button || !control.contains(button)) return;
        button.focus({ preventScroll: true });
        set(button.dataset.colorMode, button, button.dataset.colorMode !== root.dataset.theme && prepare(control));
      });
    });
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', bind, { once: true });
  else bind();
})();
