import type { HomeLoad } from './home-types';
import { $ } from './home-dom';

export function createHomeLoader(entry: URL): HomeLoad {
  const version = entry.search;
  const scripts = new Map<string, Promise<void>>();
  const load: HomeLoad = (name, ready = () => true) => {
    const style = name.endsWith('.css');
    if (!style && ready()) return Promise.resolve();
    const existing = scripts.get(name); if (existing) return existing;
    const task = new Promise<void>((resolve, reject) => {
      const script = document.createElement(style ? 'link' : 'script');
      if (style) {
        // The same discriminator selects the DOM tag above.
        const link = script as HTMLLinkElement;
        link.rel = 'stylesheet'; link.href = new URL('../css/' + name + version, entry).href;
      } else (script as HTMLScriptElement).src = new URL(name + version, entry).href;
      const done = (error?: Error | null) => {
        clearTimeout(timer); script.onload = script.onerror = null;
        if (error) { script.remove(); reject(error); } else resolve();
      };
      const timer = setTimeout(() => done(new Error()), 15000);
      script.onload = () => done(ready() ? null : new Error());
      script.onerror = () => done(new Error());
      if (style) document.head.insertBefore(script, $('link[href^="/theme-settings.css"]'));
      else document.head.append(script);
    }).catch((error) => { scripts.delete(name); throw error; });
    scripts.set(name, task);
    return task;
  };
  return load;
}
