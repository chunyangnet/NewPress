import { drawChart, initTabs, initCarousel, initPopover, renderPlatform, renderHighlightedCode } from './component-view';
import { decodePublicComponent, parseComponentSnapshot, parseComponentReceipt, parseComponentProfile, componentActionConfirmed, componentError, validComponentTarget } from './components-data';
import type { ComponentConfig } from './component-config';
import type { ComponentSnapshot, ComponentAction, ComponentAttempt } from './components-data';
interface PacketSession { root: HTMLElement; trigger: HTMLElement; revealed: boolean; error: boolean; }
interface HighlightJob { root: HTMLElement; c: ComponentConfig; }
declare global { interface Window { NewPressComponents?: { count: number }; } }
(() => {
  "use strict";
  if (window.NewPressComponents) return;
  const roots = [
    ...document.querySelectorAll<HTMLElement>("aside.npw[data-np-config]"),
  ].slice(0, 200);
  if (!roots.length) return;
  const scriptURL =
    (document.currentScript as HTMLScriptElement | null)?.src ||
    new URL("/core-assets/js/newpress-components.js", location.href).href;
  const node = <K extends keyof HTMLElementTagNameMap>(tag: K, cls = '', text?: string | null) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  const configs = new WeakMap<HTMLElement, ComponentConfig>(),
    states = new WeakMap<HTMLElement, ComponentSnapshot>(),
    busy = new WeakSet<HTMLElement>(),
    activated = new WeakSet<HTMLElement>();
  const attempts = new WeakMap<HTMLElement, ComponentAttempt>();
  function pendingAction(root: HTMLElement, attempt?: ComponentAttempt) {
    if (attempt) attempts.set(root, attempt);
    else attempts.delete(root);
    let retry = root.querySelector<HTMLButtonElement>('[data-npw-retry]');
    if (!retry && attempt) {
      retry = node('button', 'npw-button', '核对上次操作');
      retry.type = 'button'; retry.dataset.npwRetry = ''; retry.dataset.npwAction = 'retry';
      root.append(retry);
    }
    if (retry) retry.hidden = !attempt;
  }
  const controllers = new Set<AbortController>();
  let epoch = 0;
  let identity: string | undefined;
  const connected = (root: HTMLElement, ticket: number) => !suspended && ticket === epoch && root.isConnected;
  const required = <T extends Element = HTMLElement>(root: ParentNode, selector: string): T => {
    const element = root.querySelector<T>(selector);
    if (!element) throw new Error('组件控件不可用，请刷新页面。');
    return element;
  };
  const configFor = (root: HTMLElement): ComponentConfig => {
    const config = configs.get(root);
    if (!config) throw new Error('组件配置不可用，请刷新页面。');
    return config;
  };
  const announce = (root: HTMLElement, text: string) => {
    let e = root.querySelector<HTMLElement>(".npw-status");
    if (!e) {
      e = node("p", "npw-status");
      e.setAttribute("role", "status");
      root.append(e);
    }
    e.textContent = text;
    if (packetSession?.root === root && packetDialog?.open) {
      required<HTMLElement>(packetDialog!, ".npw-packet-message").textContent = text;
    }
  };
  const local = (value: unknown): value is string => {
    try {
      const u = new URL(typeof value === 'string' ? value : '', location.href);
      return (
        typeof value === "string" &&
        value.startsWith("/") &&
        !value.startsWith("//") &&
        !/[\\\s<>"']/.test(value) &&
        u.origin === location.origin
      );
    } catch {
      return false;
    }
  };
  const request = async (url: string, options: RequestInit = {}): Promise<unknown> => {
    const controller = new AbortController(),
      timeout = setTimeout(() => controller.abort(), 14000);
    const ticket = epoch;
    controllers.add(controller);
    try {
      const r = await fetch(url, {
        ...options,
        credentials: "same-origin",
        signal: controller.signal,
        headers: { Accept: "application/json", ...options.headers },
      });
      const data: unknown = await r.json();
      if (ticket !== epoch || suspended) throw new Error('页面或登录状态已变化，请重新核对。');
      if (!r.ok)
        throw new Error(
          componentError(data),
        );
      return data;
    } catch (e) {
      throw new Error(
        e instanceof Error && e.name === "AbortError" ? "请求未完成，请再次核对原操作结果" : componentError(e),
      );
    } finally {
      clearTimeout(timeout);
      controllers.delete(controller);
    }
  };
  const endpoint = (root: HTMLElement) =>
    `/api/v1/article-components/${root.dataset.npwPost}/${configFor(root).id}`;
  let stateQueue: Promise<unknown> = Promise.resolve();
  const loadState = (root: HTMLElement) => {
    const ticket = epoch;
    // Serialize first reads so parallel anonymous widgets share one visitor cookie.
    const task = stateQueue
      .catch(() => {})
      .then(() => { if (!connected(root, ticket)) throw new Error('页面或登录状态已变化，请重新核对。'); return request(endpoint(root)); })
      .then((raw) => {
        if (!connected(root, ticket)) throw new Error('页面或登录状态已变化，请重新核对。');
        const data = parseComponentSnapshot(raw, configFor(root), root.dataset.npConfig || '');
        states.set(root, data);
        showState(root, data);
        return data;
      });
    stateQueue = task;
    return task;
  };
  function showState(root: HTMLElement, data: ComponentSnapshot) {
    const c = configFor(root),
      s = data.state;
    if (c.kind === "redpacket") {
      const label = required<HTMLElement>(root, ".npw-packet-card-label");
      label.textContent = s.participated
        ? "积分红包 · 已领取"
        : s.claimed >= c.count
          ? "积分红包 · 已领完"
          : "积分红包";
      const owner = required<HTMLElement>(root, ".npw-packet-owner");
      owner.replaceChildren();
      if (data.owner) {
        owner.append(
          node(
            "p",
            "npw-packet-terms",
            `每份 ${c.amount} ${data.currency_name || "积分"} · 共 ${c.count} 份，领取时从你的余额发放。`,
          ),
          node(
            "p",
            "",
            `已领取 ${s.claimed} / ${c.count} 份${s.pending ? ` · ${s.pending} 份正在处理` : ""}`,
          ),
        );
        const b = node(
          "button",
          "npw-button",
          s.active ? "暂停发放" : "确认从我的余额发放",
        );
        b.type = "button";
        b.disabled = busy.has(root);
        b.dataset.npwAction = s.active ? "pause" : "activate";
        owner.append(b);
      }
      if (packetSession?.root === root && packetDialog?.open) {
        const panel = required<HTMLElement>(packetDialog!, ".npw-packet-envelope"),
          coin = required<HTMLButtonElement>(panel, ".npw-packet-coin"),
          hint = required<HTMLElement>(panel, ".npw-packet-message");
        coin.disabled =
          busy.has(root) ||
          data.owner ||
          !s.active ||
          (!s.pending_self && s.claimed + s.pending >= c.count);
        coin.setAttribute(
          "aria-label",
          data.authenticated ? "打开红包并领取" : "登录后领取红包",
        );
        if (s.participated) packetResult(root);
        else if (!busy.has(root) && !packetSession.error)
          hint.textContent = data.owner
            ? "这是你发出的红包"
            : !s.active
              ? "红包尚未启用或已暂停"
              : s.pending_self
                ? "上次领取正在核实，可再次点击“開”查看结果"
                : s.claimed + s.pending >= c.count
                  ? "红包已领完"
                  : "";
      }
    } else {
      root.querySelectorAll<HTMLButtonElement>("[data-npw-action=vote]").forEach((b) => {
        const index = Number(b.dataset.npwIndex);
        b.disabled = s.participated || busy.has(root);
        b.setAttribute(
          "aria-pressed",
          String(s.participated && s.choice === index),
        );
        if (c.kind === "poll") {
          const count = s.counts[index] || 0,
            pct = s.total ? Math.round((count / s.total) * 100) : 0;
          required<HTMLElement>(b, ".npw-vote-count").textContent = `${count} · ${pct}%`;
          b.style.setProperty("--npw-result", `${pct}%`);
        } else {
          b.classList.toggle("is-filled", s.participated && index <= s.choice);
        }
      });
      const results = required<HTMLElement>(root, ".npw-results");
      if (c.kind === "rating")
        results.replaceChildren(
          node(
            "strong",
            "npw-result-value",
            s.total ? s.average.toFixed(1) : "—",
          ),
          document.createTextNode(` / ${c.max} 分 · ${s.total} 人参与`),
        );
      else
        results.textContent = `${s.total} 人参与${s.participated ? " · 你已投票" : ""}`;
    }
  }
  async function interact(root: HTMLElement, action: ComponentAction, button: HTMLButtonElement) {
    if (busy.has(root)) return false;
    const ticket = epoch;
    const claimSession = action === "claim" ? packetSession : null;
    if (claimSession) claimSession.error = false;
    busy.add(root);
    if (claimSession?.root === root)
      required<HTMLElement>(packetDialog!, ".npw-packet-envelope")
        .classList.add("is-requesting");
    button.disabled = true;
    try {
      const data = await loadState(root),
        c = configFor(root);
      if (!connected(root, ticket)) return false;
      const priorAttempt = attempts.get(root);
      if (priorAttempt && (priorAttempt.token !== data.csrf_token || priorAttempt.fingerprint !== data.fingerprint)) throw new Error("上次操作属于其他登录状态或组件版本，请刷新页面后核对。");
      const attempt: ComponentAttempt = priorAttempt || { action, value: Number(button.dataset.npwIndex || 0), fingerprint: data.fingerprint, token: data.csrf_token };
      if (priorAttempt && componentActionConfirmed(data.state, attempt)) { pendingAction(root); announce(root, "上次操作已确认，请核对当前结果。"); return true; }
      if (priorAttempt && (action !== priorAttempt.action || Number(button.dataset.npwIndex || 0) !== priorAttempt.value)) throw new Error('上次操作尚未确认，请先选择“核对上次操作”。');
      if (
        claimSession &&
        (packetSession !== claimSession || !packetDialog?.open)
      )
        return false;
      if (action === "claim" && data.state.participated) return true;
      if (action === 'vote' && data.state.participated) {
        announce(root, '你已参与，请核对当前结果。');
        return false;
      }
      if (!data.csrf_token) {
        announce(root, "请先登录本站，再返回此处参与。");
        const a = node("a", "npw-link", "前往登录");
        a.href = "/account/";
        root.querySelector<HTMLElement>(".npw-status")!.append(" ", a);
        if (claimSession?.root === root) {
          claimSession.error = true;
          const login = node("a", "npw-packet-login", "前往登录");
          login.href = "/account/";
          required<HTMLElement>(packetDialog!, ".npw-packet-message").append(" ", login);
        }
        return false;
      }
      pendingAction(root, attempt);
      const rawReply = await request(endpoint(root), {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": data.csrf_token,
        },
        body: JSON.stringify({
          action: attempt.action,
          value: attempt.value,
          fingerprint: attempt.fingerprint,
        }),
      });
      if (!connected(root, ticket)) return false;
      const next = { ...data, state: parseComponentReceipt(rawReply, c, attempt) };
      pendingAction(root);
      states.set(root, next);
      showState(root, next);
      announce(
        root,
        attempt.action === "vote"
          ? "感谢参与，结果已保存。"
          : attempt.action === "claim"
            ? "领取成功，积分已转入你的账户。"
            : attempt.action === "activate"
              ? "红包已启用，领取时从你的余额扣款。"
              : "红包已暂停发放。",
      );
      return true;
    } catch (e) {
      if (claimSession) claimSession.error = true;
      if (connected(root, ticket)) announce(root, componentError(e));
      return false;
    } finally {
      busy.delete(root);
      if (claimSession && packetSession === claimSession)
        required<HTMLElement>(packetDialog!, ".npw-packet-envelope")
          .classList.remove("is-requesting");
      if (button.isConnected) button.disabled = false;
      const data = states.get(root);
      if (connected(root, ticket) && data) showState(root, data);
    }
  }
  let packetDialog: HTMLDialogElement | undefined, packetSession: PacketSession | null = null;
  function packetSender(root: HTMLElement) {
    const from = node("div", "npw-packet-sender"),
      avatar = root.querySelector<HTMLImageElement>(".npw-packet-avatar");
    if (avatar) {
      const img = avatar.cloneNode(false) as HTMLImageElement;
      img.loading = "eager";
      from.append(img);
    } else from.append(node("span", "npw-packet-avatar-placeholder", ""));
    from.append(
      node(
        "span",
        "",
        `${root.querySelector<HTMLElement>(".npw-packet-sender-name")?.textContent || "文章作者"}发出的红包`,
      ),
    );
    return from;
  }
  function packetResult(root: HTMLElement) {
    const session = packetSession;
    if (!session || session.root !== root || session.revealed) return;
    session.revealed = true;
    const c = configFor(root),
      panel = required<HTMLElement>(packetDialog!, ".npw-packet-envelope"),
      result = required<HTMLElement>(panel, ".npw-packet-result");
    panel.classList.remove("is-requesting");
    panel.classList.add("is-opened");
    result.append(
      packetSender(root),
      node("p", "npw-packet-result-wish", c.title),
    );
    if (c.kind === "redpacket") {
      result.append(
        node("p", "npw-packet-result-label", "已领取"),
        node("strong", "npw-packet-result-amount", c.amount),
        node(
          "p",
          "npw-packet-result-unit",
          states.get(root)?.currency_name ||
            (c.currency === "main" ? "积分" : c.currency),
        ),
        node("p", "npw-packet-result-note", "已存入你的账户余额"),
      );
    } else {
      result.append(
        node("p", "npw-packet-result-label", "心意已送达"),
        node(
          "p",
          "npw-packet-result-gift",
          c.caption || c.text || "祝你万事如意",
        ),
      );
      if (c.caption) {
        const copyButton = node(
          "button",
          "npw-packet-result-button",
          "复制礼物内容",
        );
        copyButton.type = "button";
        copyButton.addEventListener("click", () =>
          copy(root, c.caption, copyButton),
        );
        result.append(copyButton);
      }
      if (c.url) {
        const a = node("a", "npw-packet-result-button", "使用礼物");
        a.href = c.url;
        a.rel = "noopener noreferrer";
        result.append(a);
      }
    }
    if (c.text && (c.kind === "redpacket" || c.caption))
      result.append(node("p", "npw-packet-result-note", c.text));
    result.hidden = false;
    required<HTMLButtonElement>(panel, ".npw-packet-coin").disabled = true;
    required<HTMLElement>(panel, ".npw-packet-front")
      .setAttribute("aria-hidden", "true");
    result.tabIndex = -1;
    result.focus({ preventScroll: true });
  }
  function openPacket(root: HTMLElement, trigger: HTMLElement) {
    if (packetDialog?.open) return;
    if (!packetDialog) {
      packetDialog = node("dialog", "npw-packet-dialog");
      packetDialog.setAttribute("aria-label", "红包");
      packetDialog.addEventListener("click", (e) => {
        if (e.target === packetDialog) packetDialog?.close();
      });
      packetDialog.addEventListener("close", () => {
        const prior = packetSession;
        packetSession = null;
        packetDialog?.replaceChildren();
        prior?.trigger?.focus({ preventScroll: true });
      });
      document.body.append(packetDialog);
    }
    const c = configFor(root),
      session: PacketSession = { root, trigger, revealed: false, error: false };
    packetSession = session;
    const panel = node("div", "npw-packet-envelope"),
      flap = node("div", "npw-packet-flap"),
      front = node("div", "npw-packet-front"),
      coin = node("button", "npw-packet-coin", "開"),
      message = node("p", "npw-packet-message"),
      result = node("div", "npw-packet-result"),
      close = node("button", "npw-packet-close", "×");
    flap.setAttribute("aria-hidden", "true");
    front.append(
      packetSender(root),
      node("p", "npw-packet-wish", c.title || "恭喜发财，大吉大利"),
    );
    coin.type = "button";
    coin.setAttribute(
      "aria-label",
      c.kind === "gift" ? "拆开礼物" : "打开红包并领取",
    );
    message.setAttribute("role", "status");
    message.setAttribute("aria-live", "polite");
    result.hidden = true;
    close.type = "button";
    close.autofocus = true;
    close.setAttribute("aria-label", "关闭红包");
    close.addEventListener("click", () => packetDialog?.close());
    coin.addEventListener("click", () => {
      if (c.kind === "gift") packetResult(root);
      else void interact(root, "claim", coin);
    });
    panel.append(flap, front, coin, message, result);
    packetDialog.append(panel, close);
    packetDialog.showModal();
    if (c.kind === "redpacket") {
      coin.disabled = true;
      message.textContent = "正在查看红包…";
      loadState(root).catch((e) => {
        if (packetSession === session) {
          message.textContent = componentError(e);
          coin.disabled = false;
        }
      });
    }
  }
  async function copy(root: HTMLElement, value: string, button: HTMLElement) {
    try {
      await navigator.clipboard.writeText(value);
      const prior = button.textContent;
      button.textContent = "已复制";
      setTimeout(() => {
        if (button.isConnected) button.textContent = prior;
      }, 1800);
    } catch {
      announce(root, "浏览器未允许复制，请选中文字手动复制。");
    }
  }
  let lightbox: HTMLDialogElement | undefined;
  function openImage(root: HTMLElement, target: HTMLElement) {
    if (!local(target.getAttribute("href"))) return;
    if (!lightbox) {
      lightbox = node("dialog", "npw-lightbox");
      lightbox.setAttribute("aria-label", "图片灯箱");
      const close = node("button", "", "×");
      close.type = "button";
      close.setAttribute("aria-label", "关闭图片");
      close.addEventListener("click", () => lightbox?.close());
      lightbox.append(close, node("img"), node("p"));
      lightbox.addEventListener("click", (e) => {
        if (e.target === lightbox) lightbox?.close();
      });
      document.body.append(lightbox);
    }
    const img = required<HTMLImageElement>(lightbox!, "img");
    img.src = target.getAttribute("href")!;
    img.alt = required<HTMLImageElement>(target, "img")?.alt || "";
    required<HTMLElement>(lightbox!, "p").textContent = img.alt;
    lightbox.showModal();
    lightbox.addEventListener(
      "close",
      () => {
        img.removeAttribute("src");
        target.focus();
      },
      { once: true },
    );
  }
  const mime = (url: string) =>
    ({
      mp3: "audio/mpeg",
      m4a: "audio/mp4",
      aac: "audio/aac",
      ogg: "audio/ogg",
      oga: "audio/ogg",
      opus: "audio/ogg; codecs=opus",
      wav: "audio/wav",
      flac: "audio/flac",
      mp4: "video/mp4",
      m4v: "video/mp4",
      webm: "video/webm",
      ogv: "video/ogg",
      mov: "video/quicktime",
    } as Record<string, string>)[String(url).split("?")[0]!.split(".").pop()!.toLowerCase()] || "";
  function player(root: HTMLElement, button: HTMLElement) {
    const c = configFor(root),
      mount = required<HTMLElement>(root, ".npw-player");
    if (mount.children.length) return;
    if (!c.provider || c.provider === "local") {
      const media = node(c.kind === "audio" ? "audio" : "video");
      media.controls = true;
      media.preload = "none";
      media.loop = !!c.loop;
      if (media instanceof HTMLVideoElement) media.playsInline = true;
      const sources = [c.url, ...(c.items || []).map((x) => x.url)].filter(
        local,
      );
      if (!sources.length) {
        announce(root, "请设置本站媒体地址。");
        return;
      }
      for (const url of sources) {
        const s = node("source");
        s.src = url;
        if (mime(url)) s.type = mime(url);
        media.append(s);
      }
      if (media instanceof HTMLVideoElement && local(c.image)) media.poster = c.image;
      media.addEventListener("error", () =>
        announce(
          root,
          "浏览器无法解码此文件，可以下载原文件，或由作者添加 MP3 / MP4 / WebM 等备用格式。",
        ),
      );
      mount.append(media);
      media.play().catch(() => {});
    } else {
      let src;
      if (c.provider === "youtube" && /^[\w-]{11}$/.test(c.uid))
        src = `https://www.youtube-nocookie.com/embed/${c.uid}?autoplay=0`;
      if (
        c.provider === "bilibili" &&
        /^(BV[a-zA-Z0-9]{10}|av[1-9][0-9]{0,18})$/.test(c.uid)
      )
        src = `https://player.bilibili.com/player.html?${c.uid.startsWith("BV") ? "bvid=" + c.uid : "aid=" + c.uid.slice(2)}&autoplay=0`;
      if (c.provider === "netease" && /^[1-9][0-9]{0,18}$/.test(c.uid))
        src = `https://music.163.com/outchain/player?type=2&id=${c.uid}&auto=0&height=66`;
      if (!src) {
        announce(root, "媒体平台或内容编号无效。");
        return;
      }
      const frame = node("iframe");
      frame.src = src;
      frame.title = c.title || "外部媒体播放器";
      frame.loading = "lazy";
      frame.allow = "fullscreen; encrypted-media; picture-in-picture";
      frame.referrerPolicy = "strict-origin-when-cross-origin";
      frame.setAttribute(
        "sandbox",
        "allow-scripts allow-same-origin allow-presentation allow-popups",
      );
      const controls = node("div", "npw-player-controls"),
        retry = node("button"), cancel = node("button");
      retry.type = cancel.type = "button";
      retry.textContent = "重新加载";
      cancel.textContent = "取消加载";
      let timer: ReturnType<typeof setTimeout>;
      const cleanup = () => {
        clearTimeout(timer);
        frame.remove();
        controls.remove();
        button.hidden = false;
        delete root.dataset.npwPlaying;
        disposers.delete(cleanup);
      };
      retry.addEventListener("click", () => { cleanup(); player(root, button); });
      cancel.addEventListener("click", () => { cleanup(); announce(root, "已取消加载，可重新播放或打开原平台链接。"); });
      controls.append(retry, cancel);
      // A cross-origin load event cannot prove the media is actually playable.
      frame.addEventListener("error", () => announce(root, "外部播放器加载失败，可重试或打开原平台链接。"));
      timer = setTimeout(() => announce(root, "若播放器仍未显示或无法播放，可重新加载，或打开原平台链接。"), 15000);
      disposers.add(cleanup);
      mount.append(frame, controls);
    }
    root.dataset.npwPlaying = "true";
    button.hidden = true;
  }
  let worker: Worker | null = null,
    suspended = false,
    pending: (HighlightJob & { id: number; timer: ReturnType<typeof setTimeout> }) | null = null,
    highlightQueue: HighlightJob[] = [],
    nextID = 0,
    processed = 0;
  function highlight(root: HTMLElement) {
    const c = configFor(root);
    if (
      processed++ >= 50 ||
      c.text.length > 48000 ||
      c.text.split("\n").length > 2000
    )
      return;
    highlightQueue.push({ root, c });
    pump();
  }
  function pump() {
    if (suspended || pending || !highlightQueue.length) return;
    const job = highlightQueue.shift();
    if (!job) return;
    try {
      if (!worker) {
        const workerURL = new URL("newpress-highlight-worker.js", scriptURL);
        // Classic workers must be same-origin; preserve the embedded asset revision.
        worker = new Worker(new URL(workerURL.pathname, window.location.origin));
        worker.onmessage = (e) => finish(e.data);
        worker.onerror = () => finish({ error: true });
      }
      pending = {
        ...job,
        id: ++nextID,
        timer: setTimeout(() => {
          worker?.terminate();
          worker = null;
          finish({ error: true });
        }, 2500),
      };
      worker.postMessage({
        id: pending.id,
        text: job.c.text,
        language: job.c.language,
      });
    } catch {
      finish({ error: true });
    }
  }
  function finish(input: unknown) {
    if (!pending || !input || typeof input !== 'object' || Array.isArray(input)) return;
    const data = input as Record<string, unknown>;
    if (!data.error && data.id !== pending.id) return;
    const job = pending;
    pending = null;
    clearTimeout(job.timer);
    if (
      !data.error &&
      data.id === job.id &&
      typeof data.html === "string" &&
      data.html.length < 1e6
    ) {
      const code = job.root.querySelector<HTMLElement>('code');
      if (code && job.root.isConnected) renderHighlightedCode(code, data.html, data.label);
    }
    pump();
  }
  async function profile(root: HTMLElement) {
    if (busy.has(root) || root.dataset.profileLoaded) return;
    const ticket = epoch;
    busy.add(root);
    try {
      const c = configFor(root),
        p = parseComponentProfile(await request(
          `/api/v1/article-component-profile/${root.dataset.npwPost}/${c.id}`,
        )),
        card = required<HTMLElement>(root, ".npw-user-card");
      if (!connected(root, ticket)) return;
      required<HTMLElement>(card, ".npw-title").textContent = p.name;
      let text = card.querySelector<HTMLElement>(".npw-text");
      if (!text) {
        text = node("p", "npw-text");
        required<HTMLElement>(card, ".npw-user-meta").before(text);
      }
      text.textContent = p.bio || "";
      required<HTMLElement>(card, ".npw-user-meta").textContent = p.meta || "";
      if (local(p.avatar)) {
        let img = card.querySelector("img");
        if (!img) {
          img = node("img", "npw-image");
          img.alt = "";
          required<HTMLElement>(card, ".npw-user-cover").after(img);
        }
        img.src = p.avatar;
      }
      root.dataset.profileLoaded = "1";
    } catch {
      if (!connected(root, ticket)) return;
      required<HTMLElement>(root, ".npw-user-meta").textContent =
        "资料暂不可用，仍可前往用户主页。";
    } finally {
      busy.delete(root);
    }
  }
  const disposers = new Set<() => void>();
  function initializeView(root: HTMLElement, c: ComponentConfig) {
    if (c.kind === "carousel") disposers.add(initCarousel(root, c));
    if (["mention", "glossary"].includes(c.kind))
      disposers.add(initPopover(root, c.kind === "mention" ? () => profile(root) : undefined));
  }
  const activate = (root: HTMLElement) => {
    if (suspended || !configs.has(root) || activated.has(root)) return;
    const ticket = epoch;
    activated.add(root);
    const c = configFor(root);
    if (c.kind === "code") highlight(root);
    if (c.kind === "chart") disposers.add(drawChart(root, c));
    if (["poll", "rating", "redpacket"].includes(c.kind))
      loadState(root).catch((e) => { if (connected(root, ticket)) announce(root, componentError(e)); });
  };
  const observer =
    "IntersectionObserver" in window
      ? new IntersectionObserver(
          (entries) =>
            entries.forEach((e) => {
              if (e.isIntersecting) {
                observer!.unobserve(e.target);
                if (e.target instanceof HTMLElement) activate(e.target);
              }
            }),
          { rootMargin: "180px" },
        )
      : null;
  roots.forEach((root) => {
    try {
      const c = decodePublicComponent(root.dataset.npConfig || '', root.dataset.npWidget || '');
      if (['poll', 'rating', 'redpacket', 'mention'].includes(c.kind) && !validComponentTarget(root.dataset.npwPost || '')) return;
      configs.set(root, c);
      if (["gift", "redpacket"].includes(c.kind))
        root.dataset.npwEnhanced = "packet";
      if (c.kind === "tabs" && root.querySelector(".npw-panel"))
        initTabs(root, c);
      if (c.kind === "compare") {
        const panel = required<HTMLElement>(root, ".npw-compare");
        panel.classList.add("is-ready");
        const range = required<HTMLInputElement>(root, "input");
        const update = () =>
          panel.style.setProperty("--npw-compare", `${Number(range.value)}%`);
        range.addEventListener("input", update);
        let pointer: number | null = null;
        const move = (e: PointerEvent) => {
          const r = panel.getBoundingClientRect();
          range.value = String(
            Math.round(
              Math.max(
                0,
                Math.min(100, ((e.clientX - r.left) / r.width) * 100),
              ),
            ),
          );
          update();
        };
        panel.addEventListener("pointerdown", (e) => {
          if (!e.isPrimary || e.button !== 0) return;
          pointer = e.pointerId;
          panel.setPointerCapture(pointer);
          move(e);
        });
        panel.addEventListener("pointermove", (e) => {
          if (e.pointerId === pointer) move(e);
        });
        panel.addEventListener("lostpointercapture", () => {
          pointer = null;
        });
        panel.addEventListener("dragstart", (e) => e.preventDefault());
      }
      initializeView(root, c);
      renderPlatform(root, c);
      root.addEventListener("click", (e) => {
        const b = e.target instanceof Element ? e.target.closest<HTMLElement>("[data-npw-action]") : null;
        if (!b || !root.contains(b)) return;
        const action = b.dataset.npwAction;
        if (action === 'retry' && b instanceof HTMLButtonElement) {
          const attempt = attempts.get(root);
          if (attempt) { b.dataset.npwIndex = String(attempt.value); void interact(root, attempt.action, b); }
        } else if (action === "lightbox") {
          e.preventDefault();
          openImage(root, b);
        } else if (action === "copy") copy(root, c.text || "", b);
        else if (action === "password") copy(root, c.caption || "", b);
        else if (action === "play") player(root, b);
        else if (action === "packet") openPacket(root, b);
        else if (["vote", "claim", "activate", "pause"].includes(action || "") && b instanceof HTMLButtonElement)
          void interact(root, action as ComponentAction, b);
      });
      if (observer) observer.observe(root);
      else activate(root);
    } catch {
      /* The server's readable fallback remains available. */
    }
  });
  window.addEventListener("pagehide", () => {
    if (packetDialog?.open) packetDialog.close();
    lightbox?.close();
    suspended = true; epoch++;
    controllers.forEach(controller => controller.abort());
    controllers.clear();
    worker?.terminate();
    worker = null;
    if (pending) {
      clearTimeout(pending.timer);
      highlightQueue.unshift({ root: pending.root, c: pending.c });
      pending = null;
    }
    observer?.disconnect();
    disposers.forEach(dispose => dispose());
    disposers.clear();
    roots.forEach(root => {
      if (configs.get(root)?.kind === "chart") activated.delete(root);
    });
  });
  window.addEventListener("pageshow", (event) => {
    if (!event.persisted) return;
    suspended = false;
    roots.forEach((root) => {
      const c = configs.get(root);
      if (!c) return;
      initializeView(root, c);
      if (["poll", "rating", "redpacket"].includes(c.kind))
        activated.delete(root);
      if (!activated.has(root)) {
        if (observer) observer.observe(root);
        else activate(root);
      }
    });
    pump();
  });
  document.addEventListener('chunnel:session', event => {
    const session = event.detail;
    if (!session || typeof session.authenticated !== 'boolean') return;
    const nextIdentity = session.authenticated ? String(session.id) + ':' + String(session.csrf_token || '') : 'guest';
    if (nextIdentity === identity) return;
    if (identity === undefined) {
      const known = roots.map(root => ({ root, state: states.get(root) })).filter(item => item.state && configFor(item.root).mode !== 'guest');
      if (known.length && known.every(({ state }) => state!.authenticated === session.authenticated && (!session.authenticated || state!.csrf_token === session.csrf_token))) { identity = nextIdentity; return; }
    }
    identity = nextIdentity; epoch++;
    controllers.forEach(controller => controller.abort()); controllers.clear();
    packetDialog?.close();
    for (const root of roots) {
      const config = configs.get(root);
      if (!config || !['poll', 'rating', 'redpacket'].includes(config.kind)) continue;
      states.delete(root);
      activated.delete(root);
      if (!suspended) { if (observer) observer.observe(root); else activate(root); }
    }
  });
  window.NewPressComponents = { count: roots.length };
})();
