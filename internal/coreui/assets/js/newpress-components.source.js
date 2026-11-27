(() => {
  "use strict";
  const {
    drawChart,
    initTabs,
    initCarousel,
    initPopover,
    renderPlatform,
    renderHighlightedCode,
  } = require("./newpress-component-view.source.js");
  if (window.NewPressComponents) return;
  const roots = [
    ...document.querySelectorAll("aside.npw[data-np-config]"),
  ].slice(0, 200);
  if (!roots.length) return;
  const scriptURL =
    document.currentScript?.src ||
    new URL("/core-assets/js/newpress-components.js", location.href).href;
  const node = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  const configs = new WeakMap(),
    states = new WeakMap(),
    busy = new WeakSet(),
    activated = new WeakSet();
  const announce = (root, text) => {
    let e = root.querySelector(".npw-status");
    if (!e) {
      e = node("p", "npw-status");
      e.setAttribute("role", "status");
      root.append(e);
    }
    e.textContent = text;
    if (packetSession?.root === root && packetDialog?.open) {
      packetDialog.querySelector(".npw-packet-message").textContent = text;
    }
  };
  const local = (value) => {
    try {
      const u = new URL(value, location.href);
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
  const request = async (url, options = {}) => {
    const controller = new AbortController(),
      timeout = setTimeout(() => controller.abort(), 14000);
    try {
      const r = await fetch(url, {
        ...options,
        credentials: "same-origin",
        signal: controller.signal,
        headers: { Accept: "application/json", ...options.headers },
      });
      const data = await r.json();
      if (!r.ok)
        throw new Error(
          data?.message || data?.error?.message || "暂时无法完成，请稍后重试",
        );
      return data;
    } catch (e) {
      throw new Error(
        e.name === "AbortError" ? "请求超时，可以安全重试" : e.message,
      );
    } finally {
      clearTimeout(timeout);
    }
  };
  const endpoint = (root) =>
    `/api/v1/article-components/${root.dataset.npwPost}/${configs.get(root).id}`;
  let stateQueue = Promise.resolve();
  const loadState = (root) => {
    // Serialize first reads so parallel anonymous widgets share one visitor cookie.
    const task = stateQueue
      .catch(() => {})
      .then(() => request(endpoint(root)))
      .then((data) => {
        if (data.config && data.config !== root.dataset.npConfig)
          throw new Error("组件内容已更新，请刷新页面");
        states.set(root, data);
        showState(root, data);
        return data;
      });
    stateQueue = task;
    return task;
  };
  function showState(root, data) {
    const c = configs.get(root),
      s = data.state;
    if (c.kind === "redpacket") {
      const label = root.querySelector(".npw-packet-card-label");
      label.textContent = s.participated
        ? "积分红包 · 已领取"
        : s.claimed >= c.count
          ? "积分红包 · 已领完"
          : "积分红包";
      const owner = root.querySelector(".npw-packet-owner");
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
        b.dataset.npwAction = s.active ? "pause" : "activate";
        owner.append(b);
      }
      if (packetSession?.root === root && packetDialog?.open) {
        const panel = packetDialog.querySelector(".npw-packet-envelope"),
          coin = panel.querySelector(".npw-packet-coin"),
          hint = panel.querySelector(".npw-packet-message");
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
      root.querySelectorAll("[data-npw-action=vote]").forEach((b) => {
        const index = Number(b.dataset.npwIndex);
        b.disabled = s.participated;
        b.setAttribute(
          "aria-pressed",
          String(s.participated && s.choice === index),
        );
        if (c.kind === "poll") {
          const count = s.counts[index] || 0,
            pct = s.total ? Math.round((count / s.total) * 100) : 0;
          b.querySelector(".npw-vote-count").textContent = `${count} · ${pct}%`;
          b.style.setProperty("--npw-result", `${pct}%`);
        } else {
          b.classList.toggle("is-filled", s.participated && index <= s.choice);
        }
      });
      const results = root.querySelector(".npw-results");
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
  async function interact(root, action, button) {
    if (busy.has(root)) return false;
    const claimSession = action === "claim" ? packetSession : null;
    if (claimSession) claimSession.error = false;
    busy.add(root);
    if (claimSession?.root === root)
      packetDialog
        .querySelector(".npw-packet-envelope")
        .classList.add("is-requesting");
    button.disabled = true;
    try {
      const data = await loadState(root),
        c = configs.get(root);
      if (
        claimSession &&
        (packetSession !== claimSession || !packetDialog.open)
      )
        return false;
      if (action === "claim" && data.state.participated) return true;
      if (!data.csrf_token) {
        announce(root, "请先登录本站，再返回此处参与。");
        const a = node("a", "npw-link", "前往登录");
        a.href = "/account/";
        root.querySelector(".npw-status").append(" ", a);
        if (claimSession?.root === root) {
          claimSession.error = true;
          const login = node("a", "npw-packet-login", "前往登录");
          login.href = "/account/";
          packetDialog.querySelector(".npw-packet-message").append(" ", login);
        }
        return false;
      }
      const reply = await request(endpoint(root), {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": data.csrf_token,
        },
        body: JSON.stringify({
          action,
          value: Number(button.dataset.npwIndex || 0),
          fingerprint: data.fingerprint,
        }),
      });
      const next = { ...data, state: reply.state };
      states.set(root, next);
      showState(root, next);
      announce(
        root,
        action === "vote"
          ? "感谢参与，结果已保存。"
          : action === "claim"
            ? "领取成功，积分已转入你的账户。"
            : action === "activate"
              ? "红包已启用，领取时从你的余额扣款。"
              : "红包已暂停发放。",
      );
      return true;
    } catch (e) {
      if (claimSession) claimSession.error = true;
      announce(root, e.message);
      return false;
    } finally {
      busy.delete(root);
      if (claimSession && packetSession === claimSession)
        packetDialog
          .querySelector(".npw-packet-envelope")
          .classList.remove("is-requesting");
      if (button.isConnected) button.disabled = false;
      const data = states.get(root);
      if (data) showState(root, data);
    }
  }
  let packetDialog, packetSession;
  function packetSender(root) {
    const from = node("div", "npw-packet-sender"),
      avatar = root.querySelector(".npw-packet-avatar");
    if (avatar) {
      const img = avatar.cloneNode(false);
      img.loading = "eager";
      from.append(img);
    } else from.append(node("span", "npw-packet-avatar-placeholder", ""));
    from.append(
      node(
        "span",
        "",
        `${root.querySelector(".npw-packet-sender-name")?.textContent || "文章作者"}发出的红包`,
      ),
    );
    return from;
  }
  function packetResult(root) {
    const session = packetSession;
    if (!session || session.root !== root || session.revealed) return;
    session.revealed = true;
    const c = configs.get(root),
      panel = packetDialog.querySelector(".npw-packet-envelope"),
      result = panel.querySelector(".npw-packet-result");
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
    panel.querySelector(".npw-packet-coin").disabled = true;
    panel
      .querySelector(".npw-packet-front")
      .setAttribute("aria-hidden", "true");
    result.tabIndex = -1;
    result.focus({ preventScroll: true });
  }
  function openPacket(root, trigger) {
    if (packetDialog?.open) return;
    if (!packetDialog) {
      packetDialog = node("dialog", "npw-packet-dialog");
      packetDialog.setAttribute("aria-label", "红包");
      packetDialog.addEventListener("click", (e) => {
        if (e.target === packetDialog) packetDialog.close();
      });
      packetDialog.addEventListener("close", () => {
        const prior = packetSession;
        packetSession = null;
        packetDialog.replaceChildren();
        prior?.trigger?.focus({ preventScroll: true });
      });
      document.body.append(packetDialog);
    }
    const c = configs.get(root),
      session = { root, trigger, revealed: false };
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
    close.addEventListener("click", () => packetDialog.close());
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
          message.textContent = e.message;
          coin.disabled = false;
        }
      });
    }
  }
  async function copy(root, value, button) {
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
  let lightbox;
  function openImage(root, target) {
    if (!local(target.getAttribute("href"))) return;
    if (!lightbox) {
      lightbox = node("dialog", "npw-lightbox");
      lightbox.setAttribute("aria-label", "图片灯箱");
      const close = node("button", "", "×");
      close.type = "button";
      close.setAttribute("aria-label", "关闭图片");
      close.addEventListener("click", () => lightbox.close());
      lightbox.append(close, node("img"), node("p"));
      lightbox.addEventListener("click", (e) => {
        if (e.target === lightbox) lightbox.close();
      });
      document.body.append(lightbox);
    }
    const img = lightbox.querySelector("img");
    img.src = target.getAttribute("href");
    img.alt = target.querySelector("img")?.alt || "";
    lightbox.querySelector("p").textContent = img.alt;
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
  const mime = (url) =>
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
    })[String(url).split("?")[0].split(".").pop().toLowerCase()] || "";
  function player(root, button) {
    const c = configs.get(root),
      mount = root.querySelector(".npw-player");
    if (mount.children.length) return;
    if (!c.provider || c.provider === "local") {
      const media = node(c.kind);
      media.controls = true;
      media.preload = "none";
      media.loop = !!c.loop;
      media.playsInline = true;
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
      if (c.kind === "video" && local(c.image)) media.poster = c.image;
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
      let timer;
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
  let worker,
    suspended = false,
    pending = null,
    highlightQueue = [],
    nextID = 0,
    processed = 0;
  function highlight(root) {
    const c = configs.get(root);
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
  function finish(data) {
    if (!pending) return;
    const job = pending;
    pending = null;
    clearTimeout(job.timer);
    if (
      !data.error &&
      data.id === job.id &&
      typeof data.html === "string" &&
      data.html.length < 1e6
    ) {
      renderHighlightedCode(job.root.querySelector("code"), data.html, data.label);
    }
    pump();
  }
  async function profile(root) {
    if (busy.has(root) || root.dataset.profileLoaded) return;
    busy.add(root);
    try {
      const c = configs.get(root),
        p = await request(
          `/api/v1/article-component-profile/${root.dataset.npwPost}/${c.id}`,
        ),
        card = root.querySelector(".npw-user-card");
      card.querySelector(".npw-title").textContent = p.name;
      let text = card.querySelector(".npw-text");
      if (!text) {
        text = node("p", "npw-text");
        card.querySelector(".npw-user-meta").before(text);
      }
      text.textContent = p.bio || "";
      card.querySelector(".npw-user-meta").textContent = p.meta || "";
      if (local(p.avatar)) {
        let img = card.querySelector("img");
        if (!img) {
          img = node("img", "npw-image");
          img.alt = "";
          card.querySelector(".npw-user-cover").after(img);
        }
        img.src = p.avatar;
      }
      root.dataset.profileLoaded = "1";
    } catch {
      root.querySelector(".npw-user-meta").textContent =
        "资料暂不可用，仍可前往用户主页。";
    } finally {
      busy.delete(root);
    }
  }
  const disposers = new Set();
  function initializeView(root, c) {
    if (c.kind === "carousel") disposers.add(initCarousel(root, c));
    if (["mention", "glossary"].includes(c.kind))
      disposers.add(initPopover(root, c.kind === "mention" ? () => profile(root) : undefined));
  }
  const activate = (root) => {
    if (activated.has(root)) return;
    activated.add(root);
    const c = configs.get(root);
    if (c.kind === "code") highlight(root);
    if (c.kind === "chart") disposers.add(drawChart(root, c));
    if (["poll", "rating", "redpacket"].includes(c.kind))
      loadState(root).catch((e) => announce(root, e.message));
  };
  const observer =
    "IntersectionObserver" in window
      ? new IntersectionObserver(
          (entries) =>
            entries.forEach((e) => {
              if (e.isIntersecting) {
                observer.unobserve(e.target);
                activate(e.target);
              }
            }),
          { rootMargin: "180px" },
        )
      : null;
  roots.forEach((root) => {
    try {
      const raw = atob(
        root.dataset.npConfig.replace(/-/g, "+").replace(/_/g, "/"),
      );
      const c = JSON.parse(
        new TextDecoder().decode(
          Uint8Array.from(raw, (ch) => ch.charCodeAt(0)),
        ),
      );
      if (c.v !== 1 || c.kind !== root.dataset.npWidget) return;
      configs.set(root, c);
      if (["gift", "redpacket"].includes(c.kind))
        root.dataset.npwEnhanced = "packet";
      if (c.kind === "tabs" && root.querySelector(".npw-panel"))
        initTabs(root, c);
      if (c.kind === "compare") {
        const panel = root.querySelector(".npw-compare");
        panel.classList.add("is-ready");
        const range = root.querySelector("input");
        const update = () =>
          panel.style.setProperty("--npw-compare", `${Number(range.value)}%`);
        range.addEventListener("input", update);
        let pointer = null;
        const move = (e) => {
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
        const b = e.target.closest("[data-npw-action]");
        if (!b || !root.contains(b)) return;
        const action = b.dataset.npwAction;
        if (action === "lightbox") {
          e.preventDefault();
          openImage(root, b);
        } else if (action === "copy") copy(root, c.text || "", b);
        else if (action === "password") copy(root, c.caption || "", b);
        else if (action === "play") player(root, b);
        else if (action === "packet") openPacket(root, b);
        else if (["vote", "claim", "activate", "pause"].includes(action))
          interact(root, action, b);
      });
      if (observer) observer.observe(root);
      else activate(root);
    } catch {
      /* The server's readable fallback remains available. */
    }
  });
  window.addEventListener("pagehide", () => {
    if (packetDialog?.open) packetDialog.close();
    suspended = true;
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
  window.NewPressComponents = { count: roots.length };
})();
