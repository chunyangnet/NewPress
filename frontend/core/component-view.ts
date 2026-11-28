import type { ComponentConfig, ComponentItem } from './component-config';
const node = <K extends keyof HTMLElementTagNameMap>(tag: K, cls = '', text?: string | null) => {
  const element = document.createElement(tag);
  if (cls) element.className = cls;
  if (text != null) element.textContent = text;
  return element;
};
export function drawChart(root: HTMLElement, c: ComponentConfig) {
  const mount = root.querySelector<HTMLElement>(".npw-chart");
  if (!mount || !c.items?.length || !(c.max > 0)) return () => {};
  mount.dataset.npwLayout = c.layout;
  const data = root.querySelector<HTMLDetailsElement>(".npw-chart-data");
  if (data) data.open = false;
  let previousWidth = 0;
  const render = () => {
    const available = Math.round(mount.clientWidth);
    if (!available || available === previousWidth) return;
    previousWidth = available;
    renderChart(mount, c, available);
  };
  render();
  const observer = typeof ResizeObserver === "function" ? new ResizeObserver(render) : null;
  observer?.observe(mount);
  return () => observer?.disconnect();
}

function renderChart(mount: HTMLElement, c: ComponentConfig, available: number) {
  const value = (item: ComponentItem) => Math.max(0, Math.min(c.max, Number(item.value) || 0));
  const standardNumber = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2 }),
    compactNumber = new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 2 }),
    number = (value: number) => (Math.abs(value) >= 10000 ? compactNumber : standardNumber).format(value);
  // Keep SVG units equal to CSS pixels. Dense series scroll inside the card.
  const width = c.layout === "radar" ? available : Math.max(available, 56 + c.items.length * 64),
    height = c.layout === "radar" ? Math.max(248, Math.min(350, available * 0.88)) : Math.max(200, Math.min(290, available * 0.55));
  const ns = "http://www.w3.org/2000/svg",
    svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  svg.style.minWidth = `${width}px`;
  svg.setAttribute("aria-hidden", "true");
  mount.tabIndex = width > available ? 0 : -1;
  mount.setAttribute("role", "region");
  mount.setAttribute("aria-label", `${c.title || "图表"}${width > available ? "，可横向滚动" : ""}`);
  const add = (name: string, attrs: Record<string, string | number>, text?: string) => {
    const e = document.createElementNS(ns, name);
    Object.entries(attrs).forEach(([k, v]) => e.setAttribute(k, String(v)));
    if (text != null) e.textContent = text;
    svg.append(e);
    return e;
  };
  const fullTitle = (element: Element, text: string) => {
    const title = document.createElementNS(ns, "title");
    title.textContent = text;
    element.append(title);
  };
  const fitLabel = (text: string, space: number) => {
    const chars = [...String(text || "")];
    let length = 0;
    const cut = chars.findIndex(ch => (length += /[^\x00-\x7f]/.test(ch) ? 12 : 7) > space);
    return cut < 0 ? chars.join("") : `${chars.slice(0, Math.max(1, cut - 1)).join("")}…`;
  };
  if (c.layout === "radar") {
    const n = c.items.length,
      cx = width / 2,
      cy = height / 2,
      r = Math.min(height / 2 - 46, width * 0.27),
      point = (i: number, v: number): [number, number] => [
        cx + Math.cos(-Math.PI / 2 + (i * 2 * Math.PI) / n) * r * v,
        cy + Math.sin(-Math.PI / 2 + (i * 2 * Math.PI) / n) * r * v,
      ];
    for (let step = 4; step >= 1; step--)
      add("polygon", {
        points: c.items.map((_, i) => point(i, step / 4).join(",")).join(" "),
        fill: "currentColor",
        "fill-opacity": step % 2 ? 0.025 : 0.015,
        stroke: "currentColor",
        "stroke-opacity": 0.15,
      });
    c.items.forEach((item, i) => {
      const [x, y] = point(i, 1),
        [tx, ty] = point(i, 1 + 17 / r);
      add("line", {
        x1: cx,
        y1: cy,
        x2: x,
        y2: y,
        stroke: "currentColor",
        "stroke-opacity": 0.15,
      });
      // A dense radar keeps its complete labels in the accessible data table.
      if (n > 10 && i % Math.ceil(n / 8)) return;
      const space = Math.min(84, width * 0.28),
        safeX = Math.max(space / 2 + 3, Math.min(width - space / 2 - 3, tx));
      const label = add(
        "text",
        {
          x: safeX,
          y: ty - 5,
          "text-anchor": "middle",
          "dominant-baseline": "middle",
        },
        fitLabel(item.title, space),
      );
      fullTitle(label, item.title || "");
      const amount = add("text", {
        x: safeX, y: ty + 11, "text-anchor": "middle",
        "dominant-baseline": "middle", class: "npw-chart-value",
      }, fitLabel(`${number(item.value)}${c.unit ? " " + c.unit : ""}`, space));
      fullTitle(amount, `${item.value} ${c.unit || ""}`);
    });
    add("polygon", {
      points: c.items
        .map((item, i) => point(i, value(item) / c.max).join(","))
        .join(" "),
      fill: "currentColor",
      "fill-opacity": 0.17,
      stroke: "currentColor",
      "stroke-width": 2,
      "stroke-linejoin": "round",
    });
    for (let step = 1; step < 4; step++)
      add("text", { x: cx + 7, y: cy - r * step / 4 - 3, class: "npw-chart-tick" }, number(c.max * step / 4));
    c.items.forEach((item, i) => {
      const [x, y] = point(i, value(item) / c.max);
      const dot = add("circle", {
        cx: x,
        cy: y,
        r: 3.5,
        fill: "currentColor",
        stroke: "var(--w-bg)",
        "stroke-width": 1.5,
      });
      fullTitle(dot, `${item.title}：${item.value} ${c.unit || ""}`);
    });
  } else {
    const n = c.items.length,
      left = 42,
      bottom = height - 30,
      plotHeight = bottom - 26,
      step = (width - left - 14) / n;
    for (let i = 0; i <= 4; i++) {
      const y = bottom - (i * plotHeight) / 4;
      add("line", {
        x1: left,
        y1: y,
        x2: width - 8,
        y2: y,
        stroke: "currentColor",
        "stroke-opacity": 0.12,
      });
      add(
        "text",
        { x: left - 8, y: y + 4, "text-anchor": "end" },
        number((c.max * i) / 4),
      );
    }
    const points: [number, number][] = [];
    c.items.forEach((item, i) => {
      const x = left + step * (i + 0.5),
        y = bottom - (value(item) / c.max) * plotHeight;
      points.push([x, y]);
      if (c.layout === "bar")
        add("rect", {
          x: x - step * 0.3,
          y,
          width: step * 0.6,
          height: bottom - y,
          rx: Math.min(6, step * 0.15),
          fill: "currentColor",
          "fill-opacity": 0.8,
        });
      else add("circle", { cx: x, cy: y, r: 4, fill: "currentColor" });
      fullTitle(add(
          "text",
          { x, y: bottom + 22, "text-anchor": "middle" },
          fitLabel(item.title, step - 8),
        ), item.title || "");
      fullTitle(add(
          "text",
          { x, y: y - 10, "text-anchor": "middle" },
          number(item.value),
        ), `${item.value} ${c.unit || ""}`);
    });
    if (c.layout === "line")
      add("polyline", {
        points: points.map((p) => p.join(",")).join(" "),
        fill: "none",
        stroke: "currentColor",
        "stroke-width": 2.5,
      });
  }
  mount.replaceChildren(svg);
}

export function initTabs(root: HTMLElement, c: ComponentConfig) {
  const holder = root.querySelector<HTMLElement>(".npw-panels");
  if (!holder) return;
  const panels = [...holder.children],
    list = node("div", "npw-tablist");
  list.setAttribute("role", "tablist");
  list.setAttribute("aria-label", c.title || "内容标签");
  const buttons: HTMLButtonElement[] = [];
  panels.forEach((details, i) => {
    const b = node("button", "", details.querySelector("summary")?.textContent);
    b.type = "button";
    b.id = `npw-tab-${c.id}-${i}`;
    b.setAttribute("role", "tab");
    const panel = node("div", "npw-tabpanel");
    panel.id = `npw-panel-${c.id}-${i}`;
    panel.setAttribute("role", "tabpanel");
    panel.setAttribute("aria-labelledby", b.id);
    panel.tabIndex = 0;
    panel.append(...(details.querySelector(".npw-panel-body")?.childNodes || []));
    b.setAttribute("aria-controls", panel.id);
    details.replaceWith(panel);
    buttons.push(b);
    list.append(b);
    b.addEventListener("click", () => activate(i));
    b.addEventListener("keydown", (e) => {
      let j = i;
      if (e.key === "ArrowRight") j = (i + 1) % panels.length;
      else if (e.key === "ArrowLeft")
        j = (i + panels.length - 1) % panels.length;
      else if (e.key === "Home") j = 0;
      else if (e.key === "End") j = panels.length - 1;
      else return;
      e.preventDefault();
      activate(j);
       buttons[j]?.focus({ preventScroll: true });
    });
  });
  function activate(index: number) {
    buttons.forEach((b, i) => {
      b.setAttribute("aria-selected", String(i === index));
      b.tabIndex = i === index ? 0 : -1;
      const panel = holder?.children[i];
      if (panel instanceof HTMLElement) panel.hidden = i !== index;
    });
    const active = buttons[index];
    if (!active) return;
    const start = active.getBoundingClientRect().left - list.getBoundingClientRect().left + list.scrollLeft,
      end = start + active.offsetWidth;
    if (start < list.scrollLeft) list.scrollLeft = start;
    else if (end > list.scrollLeft + list.clientWidth)
      list.scrollLeft = end - list.clientWidth;
  }
  holder.before(list);
  activate(0);
}
export function renderPlatform(root: HTMLElement, c: ComponentConfig) {
  const label = root.querySelector(".npw-platform");
  if (!label) return;
  const paths: Record<string, string> = {
    youtube:
      "M21.5 7.2a3 3 0 0 0-2.1-2.1C17.6 4.6 12 4.6 12 4.6s-5.6 0-7.4.5A3 3 0 0 0 2.5 7.2 31 31 0 0 0 2 12a31 31 0 0 0 .5 4.8 3 3 0 0 0 2.1 2.1c1.8.5 7.4.5 7.4.5s5.6 0 7.4-.5a3 3 0 0 0 2.1-2.1A31 31 0 0 0 22 12a31 31 0 0 0-.5-4.8ZM10 15.3V8.7l6 3.3Z",
    bilibili:
      "M7 2 10 5h4l3-3 1 1-2 2h3a3 3 0 0 1 3 3v11a3 3 0 0 1-3 3H5a3 3 0 0 1-3-3V8a3 3 0 0 1 3-3h3L6 3Zm-2 5a1 1 0 0 0-1 1v11a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1V8a1 1 0 0 0-1-1Zm2 4h2v4H7Zm8 0h2v4h-2Z",
    netease:
      "M15 3v9a4 4 0 1 1-4-4v3a1 1 0 1 0 1 1V3Zm-5 1v2a7 7 0 1 0 8 5h2A9 9 0 1 1 10 4Z",
    github:
      "M12 2a10 10 0 0 0-3.16 19.49c.5.09.68-.22.68-.48v-1.86c-2.78.6-3.37-1.18-3.37-1.18-.45-1.16-1.11-1.47-1.11-1.47-.91-.62.07-.61.07-.61 1 .07 1.53 1.03 1.53 1.03.89 1.53 2.34 1.09 2.91.83.09-.65.35-1.09.64-1.34-2.22-.25-4.55-1.11-4.55-4.94 0-1.09.39-1.98 1.03-2.68-.1-.25-.45-1.27.1-2.65 0 0 .84-.27 2.75 1.03A9.58 9.58 0 0 1 12 6.83c.85 0 1.71.11 2.51.34 1.91-1.3 2.75-1.03 2.75-1.03.55 1.38.2 2.4.1 2.65.64.7 1.03 1.59 1.03 2.68 0 3.84-2.34 4.69-4.57 4.94.36.31.68.92.68 1.85v2.75c0 .27.18.58.69.48A10 10 0 0 0 12 2Z",
    site: "m12 2 10 8h-3v11h-5v-7h-4v7H5V10H2Zm0 2.6L5.7 9.7H7V19h1v-7h8v7h1V9.7h1.3Z",
  };
  const path = paths[c.provider];
  if (!path) return;
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS(svg.namespaceURI, "path");
  p.setAttribute("d", path);
  p.setAttribute(
    "fill",
    ({
      youtube: "#e14545",
      bilibili: "#e578a0",
      netease: "#cd4247",
      github: "currentColor",
      site: "currentColor",
    } as Record<string, string>)[c.provider] || 'currentColor',
  );
  svg.append(p);
  label.prepend(svg);
}

export function renderHighlightedCode(code: HTMLElement, html: unknown, languageLabel: unknown) {
  if (!code || typeof html !== "string" || html.length >= 1e6) return;
  const template = document.createElement("template");
  template.innerHTML = html;
  const out = document.createDocumentFragment();
  let line = node("span", "npw-code-line"),
    index = 1;
  line.dataset.line = String(index);
  out.append(line);
  const walk = (n: Node, classes: string[] = []) => {
    if (n.nodeType === 3) {
      (n.textContent || '').split("\n").forEach((part, i) => {
        if (i) {
          line = node("span", "npw-code-line");
          line.dataset.line = String(++index);
          out.append(line);
        }
        if (part) line.append(node("span", classes.join(" "), part));
      });
    } else if (n instanceof Element && n.tagName === "SPAN") {
      const safe = [...n.classList].filter((x) => /^hljs-[a-z0-9_-]+$/.test(x));
      n.childNodes.forEach((child) => walk(child, [...classes, ...safe]));
    }
  };
  template.content.childNodes.forEach((n) => walk(n));
  code.replaceChildren(out);
  code.style.setProperty("--npw-line-digits", String(String(index).length));
  if (typeof languageLabel === "string" && languageLabel.length <= 100) {
    const badge = code.closest("aside")?.querySelector(".npw-code-language");
    if (badge) badge.textContent = languageLabel;
  }
}

export function initCarousel(root: HTMLElement, c: ComponentConfig) {
  const gallery = root.querySelector<HTMLElement>(".npw-gallery"),
    previous = root.querySelector<HTMLButtonElement>('[data-npw-action="previous"]'),
    next = root.querySelector<HTMLButtonElement>('[data-npw-action="next"]'),
    count = root.querySelector(".npw-carousel-count");
  if (!gallery || !previous || !next || !count) return () => {};
  const slides = [...gallery.children].filter((child): child is HTMLElement => child instanceof HTMLElement);
  let index = 0, lastWidth = 0;
  count.setAttribute("aria-live", "polite");
  count.setAttribute("aria-atomic", "true");
  const offset = (i: number) => (slides[i]?.offsetLeft || 0) - (slides[0]?.offsetLeft || 0);
  const paint = () => {
    const text = `${slides.length ? index + 1 : 0} / ${slides.length}`;
    if (count.textContent !== text) count.textContent = text;
    previous.disabled = slides.length < 2 || (!c.loop && index === 0);
    next.disabled = slides.length < 2 || (!c.loop && index === slides.length - 1);
  };
  const update = () => {
    if (!gallery.clientWidth) return;
    index = slides.reduce((best, _, i) => Math.abs(offset(i) - gallery.scrollLeft) < Math.abs(offset(best) - gallery.scrollLeft) ? i : best, 0);
    paint();
  };
  const move = (target: number, behavior: ScrollBehavior = "smooth") => {
    if (!slides.length) return;
    index = c.loop ? (target + slides.length) % slides.length : Math.max(0, Math.min(slides.length - 1, target));
    gallery.scrollTo({ left: offset(index), behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : behavior });
    paint();
  };
  const goBack = () => move(index - 1), goNext = () => move(index + 1);
  const keyboard = (e: KeyboardEvent) => {
    if (e.target !== gallery || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
    e.preventDefault();
    move(e.key === "Home" ? 0 : e.key === "End" ? slides.length - 1 : index + (e.key === "ArrowRight" ? 1 : -1));
  };
  gallery.tabIndex = 0;
  gallery.setAttribute("aria-label", "图片轮播，可左右滑动或使用方向键切换");
  gallery.addEventListener("scroll", update, { passive: true });
  gallery.addEventListener("keydown", keyboard);
  previous.addEventListener("click", goBack);
  next.addEventListener("click", goNext);
  const observer = typeof ResizeObserver === "function" ? new ResizeObserver(() => {
    if (gallery.clientWidth && gallery.clientWidth !== lastWidth) {
      lastWidth = gallery.clientWidth;
      move(index, "instant");
    }
  }) : null;
  update();
  observer?.observe(gallery);
  paint();
  return () => {
    observer?.disconnect();
    gallery.removeEventListener("scroll", update);
    gallery.removeEventListener("keydown", keyboard);
    previous.removeEventListener("click", goBack);
    next.removeEventListener("click", goNext);
  };
}

export function initPopover(root: HTMLElement, onOpen = () => {}) {
  const mention = root.querySelector<HTMLElement>(".npw-mention"),
    host = mention || root.querySelector<HTMLDetailsElement>(".npw-definition"),
    trigger = host?.querySelector(mention ? ".npw-mention-link" : "summary");
  if (!host || !trigger) return () => {};
  const events = new AbortController();
  const listen = <K extends keyof HTMLElementEventMap>(target: HTMLElement | Element | Document, name: K, fn: (event: HTMLElementEventMap[K]) => void) => target.addEventListener(name, fn as EventListener, { signal: events.signal });
  let pointer = "", wasOpen = false;
  const isOpen = () => mention ? host.dataset.npwOpen === "true" : host instanceof HTMLDetailsElement && host.open;
  const setOpen = (open: boolean) => {
    if (mention) {
      host.dataset.npwOpen = String(open);
      if (open) delete host.dataset.npwDismissed;
      else host.dataset.npwDismissed = "";
      trigger.setAttribute("aria-expanded", String(open));
    } else if (host instanceof HTMLDetailsElement) host.open = open;
    if (open) onOpen();
  };
  if (mention) trigger.setAttribute("aria-expanded", "false");
  listen(host, "pointerenter", e => { if (e.pointerType === "mouse") setOpen(true); });
  listen(host, "pointerleave", e => {
    if (e.pointerType === "mouse" && !host.contains(document.activeElement)) setOpen(false);
  });
  listen(host, "focusin", e => { if (e.target instanceof Element && e.target.matches(":focus-visible")) setOpen(true); });
  listen(host, "focusout", e => { if (!(e.relatedTarget instanceof Node) || !host.contains(e.relatedTarget)) setOpen(false); });
  listen(trigger, "pointerdown", e => { pointer = e.pointerType; wasOpen = isOpen(); });
  listen(trigger, "click", e => {
    if (mention && pointer && pointer !== "mouse" && !wasOpen) {
      e.preventDefault();
      setOpen(true);
    }
    pointer = "";
  });
  listen(host, "keydown", e => {
    if (e.key === "Escape" && isOpen()) {
      e.stopPropagation();
      setOpen(false);
    }
  });
  listen(document, "pointerdown", e => { if (isOpen() && (!(e.target instanceof Node) || !host.contains(e.target))) setOpen(false); });
  return () => events.abort();
}
