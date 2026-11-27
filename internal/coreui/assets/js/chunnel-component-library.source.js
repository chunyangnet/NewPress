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
  const escape = (value) =>
    String(value ?? "").replace(
      /[&<>"']/g,
      (ch) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[ch],
    );
  const { componentIcon } = require("./component-icon.source.js");
  const definitions = [
    ["callout", "提示块", "内容排版", "info", "蓝色提示、温暖贴士、成功或警告"],
    ["quote", "引用", "内容排版", "message-square", "留白、引号与引用来源"],
    ["card", "内容卡片", "内容排版", "panels-top-left", "标题、正文、图片与跳转"],
    ["accordion", "折叠面板", "内容排版", "chevron-down", "分组收纳长内容"],
    ["tabs", "标签页", "内容排版", "panels-top-left", "多个内容面板切换"],
    ["table", "表格", "内容排版", "layout-grid", "可编辑行列，窄屏横向滚动"],
    ["badges", "徽章标签", "内容排版", "tags", "分类、链接和状态标签"],
    ["annotation", "高亮批注", "内容排版", "pen-line", "突出文字，展开补充说明"],
    ["glossary", "词汇释义", "内容排版", "circle-help", "术语与可展开的解释"],
    ["ai", "AI 声明内容块", "内容排版", "sparkles", "明确标注 AI 辅助创作"],
    ["code", "高亮代码", "代码与资源", "code-2", "本地语言库、行号与一键复制"],
    ["download", "网盘下载", "代码与资源", "file-down", "资源链接、提取码和说明"],
    ["gift", "礼物红包", "代码与资源", "shopping-bag", "公开祝福、兑换码或礼物链接"],
    [
      "redpacket",
      "积分红包",
      "代码与资源",
      "wallet",
      "真实余额转账，作者启用后领取",
    ],
    ["image-card", "图片卡片", "图片与媒体", "image", "照片、渐变蒙层与图注"],
    ["gallery", "图片网格", "图片与媒体", "layout-grid", "自定义列数、拼贴与灯箱"],
    ["carousel", "图片轮播", "图片与媒体", "panels-top-left", "横向滑动、手动切换"],
    ["compare", "图片对比", "图片与媒体", "git-compare", "拖动对照两张图片"],
    ["audio", "音频播放器", "图片与媒体", "radio-tower", "本地音频、备用格式、网易云 ID"],
    ["video", "视频播放器", "图片与媒体", "play", "本地、YouTube、哔哩哔哩"],
    ["progress", "进度条", "数据与互动", "sliders-horizontal", "显示完成比例与说明"],
    ["counter", "数字计数", "数据与互动", "hash", "数字与单位的醒目展示"],
    ["chart", "数据图表", "数据与互动", "chart-no-axes-column", "柱状、折线、雷达多边形评分"],
    ["poll", "投票", "数据与互动", "check-circle", "真实结果，选择登录或游客参与"],
    ["rating", "评分", "数据与互动", "star", "5 分或 10 分制与汇总均分"],
    ["mention", "@ 用户", "数据与互动", "at-sign", "本站、GitHub、B 站悬浮资料卡"],
  ].map(([kind, name, category, icon, description]) => ({
    kind,
    name,
    category,
    icon,
    description,
  }));
  const map = Object.fromEntries(definitions.map((d) => [d.kind, d]));
  const uniqueID = () => crypto.randomUUID().replace(/-/g, "");
  const defaults = (kind) => {
    const c = {
      v: 1,
      id: uniqueID(),
      kind,
      title: map[kind].name,
      text: "",
      tone: "theme",
      style: "soft",
      width: "full",
      density: "normal",
    };
    const items = (a, b) => [
      { title: a, text: "在这里写下内容。" },
      { title: b, text: "在这里补充更多内容。" },
    ];
    switch (kind) {
      case "callout":
        Object.assign(c, {
          title: "提示",
          text: "这是一条提示信息，帮助你更好地理解接下来的内容。",
          tone: "blue",
        });
        break;
      case "quote":
        Object.assign(c, {
          title: "",
          text: "生活不止眼前的苟且，还有诗和远方。",
          caption: "高晓松",
          tone: "amber",
        });
        break;
      case "code":
        Object.assign(c, {
          title: "",
          language: "python",
          text: 'def hello_world():\n    print("Hello, World!")\n\n    # 这是一个优雅的代码块\n    return True',
        });
        break;
      case "download":
        Object.assign(c, {
          title: "资源下载",
          text: "文件说明、大小或版本信息",
          url: "",
          caption: "",
        });
        break;
      case "gift":
        Object.assign(c, {
          title: "新年快乐，恭喜发财",
          text: "",
          sender: "",
          image: "",
          caption: "在这里填写公开祝福或兑换码",
          url: "",
        });
        break;
      case "redpacket":
        Object.assign(c, {
          title: "恭喜发财，大吉大利",
          text: "",
          amount: "1",
          count: 10,
          currency: "main",
          mode: "login",
        });
        break;
      case "image-card":
        Object.assign(c, {
          title: "远山与樱花",
          text: "一场与自然的邂逅",
          image: "",
        });
        break;
      case "gallery":
      case "carousel":
        Object.assign(c, {
          title: "光影之间",
          items: [
            { title: "第一张", image: "" },
            { title: "第二张", image: "" },
            { title: "第三张", image: "" },
          ],
          columns: 3,
          layout: "grid",
          loop: true,
        });
        break;
      case "compare":
        Object.assign(c, {
          title: "前后对比",
          items: [
            { title: "之前", image: "" },
            { title: "之后", image: "" },
          ],
        });
        break;
      case "audio":
      case "video":
        Object.assign(c, {
          provider: "local",
          url: "",
          uid: "",
          items: [],
          image: "",
          loop: false,
        });
        break;
      case "table":
        Object.assign(c, {
          title: "",
          rows: [
            ["功能", "描述", "状态"],
            ["🚀 性能优化", "提升页面加载速度", "✓ 已完成"],
            ["🎨 界面升级", "全新视觉风格", "◷ 进行中"],
            ["🛡 安全加固", "增强系统安全性", "● 待开始"],
          ],
        });
        break;
      case "accordion":
      case "tabs":
        Object.assign(c, {
          title: "",
          items: items("开始之前", "了解更多"),
          open: false,
        });
        break;
      case "card":
        Object.assign(c, {
          title: "值得记录的事",
          text: "把想法放在这里，给内容一点呼吸的空间。",
          url: "",
          image: "",
        });
        break;
      case "badges":
        Object.assign(c, {
          title: "",
          items: ["★ 推荐", "技术", "设计", "生活", "效率", "学习"].map(
            (title) => ({ title, url: "" }),
          ),
        });
        break;
      case "progress":
        Object.assign(c, {
          title: "项目进度",
          value: 75,
          max: 100,
          tone: "teal",
        });
        break;
      case "counter":
        Object.assign(c, {
          title: "每一点积累都算数",
          value: 128,
          unit: "篇记录",
        });
        break;
      case "chart":
        Object.assign(c, {
          title: "能力评分",
          layout: "radar",
          max: 100,
          unit: "分",
          items: [
            { title: "设计", value: 85 },
            { title: "性能", value: 90 },
            { title: "易用性", value: 80 },
            { title: "兼容性", value: 88 },
            { title: "扩展性", value: 92 },
          ],
        });
        break;
      case "poll":
        Object.assign(c, {
          title: "你更喜欢哪一种？",
          mode: "login",
          items: [{ title: "简洁清爽" }, { title: "丰富细腻" }],
        });
        break;
      case "rating":
        Object.assign(c, {
          title: "这篇文章对你有帮助吗？",
          mode: "login",
          max: 5,
        });
        break;
      case "annotation":
      case "glossary":
        Object.assign(c, {
          title: "值得注意的词语",
          text: "在这里补充批注或解释。",
        });
        break;
      case "ai":
        Object.assign(c, {
          title: "关于这段内容",
          text: "此部分由 AI 辅助整理，作者已审核并补充。",
        });
        break;
      case "mention":
        Object.assign(c, {
          title: "用户名",
          provider: "github",
          uid: "octocat",
          text: "作者提供的简介；悬浮时尝试读取公开资料。",
          image: "",
        });
        break;
    }
    return c;
  };
  const encode = (c) => {
    const bytes = new TextEncoder().encode(JSON.stringify(c));
    if (bytes.length > 65536) throw new Error("组件配置不能超过 64 KiB");
    let value = "";
    for (const b of bytes) value += String.fromCharCode(b);
    return btoa(value)
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
  };
  const decode = (value) => {
    if (typeof value !== "string" || value.length > 87382)
      throw new Error("组件配置过长");
    const raw = atob(value.replace(/-/g, "+").replace(/_/g, "/"));
    const c = JSON.parse(
      new TextDecoder("utf-8", { fatal: true }).decode(
        Uint8Array.from(raw, (ch) => ch.charCodeAt(0)),
      ),
    );
    if (c.v !== 1 || !map[c.kind] || !/^[\w-]{12,64}$/.test(c.id))
      throw new Error("组件格式无效");
    return c;
  };
  const markup = (c) =>
    `<aside data-np-widget="${escape(c.kind)}" data-np-config="${encode(c)}"><p>${escape(c.title || map[c.kind].name)}</p></aside>`;
  const api = { definitions, defaults, encode, decode, markup };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  if (typeof document === "undefined") return;
  window.NewPressComponentLibrary = api;
  const dialog = document.querySelector("[data-component-library]");
  if (!dialog) return;
  const scriptURL =
    document.currentScript?.src ||
    new URL("/core-assets/js/chunnel-component-library.js", location.href).href;
  const source = document.querySelector("[data-content-source]"),
    list = dialog.querySelector("[data-component-list]"),
    fields = dialog.querySelector("[data-component-fields]"),
    preview = dialog.querySelector("[data-component-preview]"),
    search = dialog.querySelector("[data-component-search]"),
    categories = dialog.querySelector("[data-component-categories]"),
    status = dialog.querySelector("[data-component-status]"),
    insert = dialog.querySelector("[data-component-insert]");
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  let current = null,
    selection = null,
    trigger = null,
    category = "全部",
    editing = false,
    validation = null;
  const validationBusy = (value) => {
    dialog.querySelector(".npl-workspace").inert = value;
    insert.disabled = value || !current;
    insert.textContent = value
      ? "正在校验…"
      : editing
        ? "保存组件设置"
        : "插入组件";
    insert.setAttribute("aria-busy", String(value));
  };
  const say = (text) => {
    status.textContent = text;
  };
  let previewWorker = null,
    previewJob = null,
    nextPreviewJob = null,
    previewDelay = 0,
    previewSequence = 0;
  const stopPreviewWorker = () => {
    clearTimeout(previewDelay);
    clearTimeout(previewJob?.timer);
    previewWorker?.terminate();
    previewWorker = previewJob = nextPreviewJob = null;
  };
  function pumpPreviewCode() {
    if (previewJob || !nextPreviewJob || !dialog.open) return;
    try {
      if (!previewWorker) {
        previewWorker = new Worker(
          new URL("newpress-highlight-worker.js", scriptURL),
        );
        previewWorker.onmessage = ({ data }) => {
          const job = previewJob;
          if (!job || job.id !== data.id) return;
          clearTimeout(job.timer);
          previewJob = null;
          if (!data.error && job.code.isConnected)
            renderHighlightedCode(job.code, data.html, data.label);
          pumpPreviewCode();
        };
        previewWorker.onerror = stopPreviewWorker;
      }
      previewJob = nextPreviewJob;
      nextPreviewJob = null;
      previewJob.timer = setTimeout(stopPreviewWorker, 2500);
      previewWorker.postMessage({
        id: previewJob.id,
        text: previewJob.text,
        language: previewJob.language,
      });
    } catch {
      stopPreviewWorker();
    }
  }
  function showList() {
    const q = search.value.trim().toLowerCase();
    list.replaceChildren();
    definitions
      .filter(
        (d) =>
          (category === "全部" || d.category === category) &&
          `${d.name} ${d.description} ${d.kind}`.toLowerCase().includes(q),
      )
      .forEach((d) => {
        const b = el("button", "npl-choice");
        b.type = "button";
        b.dataset.componentKind = d.kind;
        b.setAttribute("aria-pressed", String(current?.kind === d.kind));
        const icon = el("span", "npl-choice-icon");
        icon.append(componentIcon(d.icon));
        b.append(
          icon,
          el("strong", "", d.name),
          el("small", "", d.description),
        );
        b.addEventListener("click", () => {
          if (current?.kind === d.kind) return;
          if (editing && current?.kind !== d.kind) {
            say("编辑时保留组件类型；需要更换类型可删除后重新插入。");
            return;
          }
          current = defaults(d.kind);
          renderFields();
          showList();
        });
        list.append(b);
      });
    if (!list.children.length)
      list.append(el("p", "npl-empty", "没有匹配组件，试试其他关键词。"));
  }
  ["全部", ...new Set(definitions.map((d) => d.category))].forEach((name) => {
    const b = el("button", "", name);
    b.type = "button";
    b.setAttribute("aria-pressed", String(name === category));
    b.addEventListener("click", () => {
      category = name;
      categories
        .querySelectorAll("button")
        .forEach((x) => x.setAttribute("aria-pressed", String(x === b)));
      showList();
    });
    categories.append(b);
  });
  search.addEventListener("input", showList);
  const options = {
    tone: [
      ["theme", "跟随主题"],
      ["blue", "晴空蓝"],
      ["teal", "深湖绿"],
      ["amber", "暖杏色"],
      ["green", "叶绿"],
      ["red", "朱红"],
      ["purple", "紫藤"],
      ["gray", "中性灰"],
    ],
    style: [
      ["soft", "柔和背景"],
      ["outline", "描边"],
      ["solid", "纯色底"],
    ],
    width: [
      ["full", "正文宽度"],
      ["reading", "居中 · 680px"],
      ["compact", "居中 · 460px"],
    ],
    density: [
      ["normal", "标准"],
      ["compact", "紧凑"],
      ["airy", "宽松"],
    ],
    mode: [
      ["login", "登录参与 · 每个账号一次"],
      ["guest", "游客参与 · 同一浏览器一次"],
      ["both", "登录和游客均可参与"],
    ],
  };
  function field(
    parent,
    label,
    key,
    type = "text",
    choices = null,
    object = current,
  ) {
    const wrap = el("label", "npl-field"),
      title = el("span", "", label);
    let input;
    if (choices) {
      input = el("select");
      choices.forEach(([v, t]) => {
        const o = el("option", "", t);
        o.value = v;
        input.append(o);
      });
    } else if (type === "textarea") {
      input = el("textarea");
      input.rows = key === "text" && current.kind === "code" ? 9 : 3;
    } else {
      input = el("input");
      input.type = type;
      if (type === "number") input.step = "any";
    }
    input.dataset.componentField = key;
    input.value = object[key] ?? "";
    if (type === "checkbox") input.checked = !!object[key];
    if (type === "text")
      input.maxLength = ["url", "image"].includes(key) ? 2048 : 600;
    if (type === "textarea")
      input.maxLength = current.kind === "code" ? 48000 : 8000;
    input.addEventListener("input", () => {
      object[key] =
        type === "number"
          ? Number(input.value)
          : type === "checkbox"
            ? input.checked
            : input.value;
      previewCurrent();
    });
    if (key === "provider")
      input.addEventListener("change", () => {
        current.uid = "";
        renderFields();
      });
    wrap.append(title, input);
    if (key === "image") {
      const pick = el("button", "npl-add", "从媒体库选择");
      pick.type = "button";
      pick.addEventListener("click", () =>
        window.NewPressComponentMedia?.(pick, (url, item) => {
          object[key] = url;
          input.value = url;
          if (!object.title && item.alt_text) object.title = item.alt_text;
          previewCurrent();
        }),
      );
      wrap.append(pick);
    }
    parent.append(wrap);
    return input;
  }
  function rowsEditor() {
    const area = el("section", "npl-rows"),
      actions = el("div", "npl-row-actions");
    field(area, "表格说明（可选）", "title");
    const grid = el("div", "npl-table-editor");
    current.rows.forEach((row, i) => {
      const line = el("div", "npl-table-row");
      row.forEach((value, j) => {
        const input = el("input");
        input.value = value;
        input.maxLength = 1000;
        input.setAttribute(
          "aria-label",
          `${i === 0 ? "表头" : "第 " + i + " 行"}，第 ${j + 1} 列`,
        );
        input.addEventListener("input", () => {
          current.rows[i][j] = input.value;
          previewCurrent();
        });
        line.append(input);
      });
      if (i > 0) {
        const remove = el("button", "", "−");
        remove.type = "button";
        remove.setAttribute("aria-label", `删除第 ${i} 行`);
        remove.addEventListener("click", () => {
          current.rows.splice(i, 1);
          renderFields();
        });
        line.append(remove);
      }
      grid.append(line);
    });
    for (const [name, fn] of [
      [
        "加一行",
        () => {
          if (current.rows.length < 50)
            current.rows.push(current.rows[0].map(() => ""));
        },
      ],
      [
        "加一列",
        () => {
          if (current.rows[0].length < 10)
            current.rows.forEach((r) => r.push(""));
        },
      ],
      [
        "删末列",
        () => {
          if (current.rows[0].length > 1) current.rows.forEach((r) => r.pop());
        },
      ],
    ]) {
      const b = el("button", "", name);
      b.type = "button";
      b.addEventListener("click", () => {
        fn();
        renderFields();
      });
      actions.append(b);
    }
    area.append(
      grid,
      actions,
      el("small", "npl-hint", "第一行为表头。最多 50 行、10 列。"),
    );
    fields.append(area);
  }
  function itemsEditor(keys) {
    const group = el("section", "npl-items");
    group.append(el("h4", "", "条目设置"));
    (current.items ||= []).forEach((item, i) => {
      const box = el("fieldset", "npl-item");
      box.append(el("legend", "", String(i + 1).padStart(2, "0")));
      keys.forEach(([key, label, type]) =>
        field(box, label, key, type || "text", null, item),
      );
      const tools = el("div", "npl-item-actions");
      for (const [name, fn] of [
        [
          "上移",
          () => {
            if (i > 0)
              [current.items[i - 1], current.items[i]] = [
                current.items[i],
                current.items[i - 1],
              ];
          },
        ],
        [
          "下移",
          () => {
            if (i < current.items.length - 1)
              [current.items[i + 1], current.items[i]] = [
                current.items[i],
                current.items[i + 1],
              ];
          },
        ],
        [
          "删除",
          () => {
            if (current.kind !== "compare") current.items.splice(i, 1);
          },
        ],
      ]) {
        const b = el("button", "", name);
        b.type = "button";
        b.addEventListener("click", () => {
          fn();
          renderFields();
        });
        tools.append(b);
      }
      box.append(tools);
      group.append(box);
    });
    if (current.kind !== "compare") {
      const add = el("button", "npl-add", "＋ 添加条目");
      add.type = "button";
      add.addEventListener("click", () => {
        const limit = current.kind === "poll" ? 12 : 30;
        if (current.items.length >= limit) {
          say(`最多 ${limit} 个条目`);
          return;
        }
        current.items.push(
          Object.fromEntries(
            keys.map(([key, , type]) => [key, type === "number" ? 0 : ""]),
          ),
        );
        renderFields();
      });
      group.append(add);
    }
    fields.append(group);
  }
  function renderFields() {
    say("");
    fields.replaceChildren();
    if (!current) return;
    const d = map[current.kind],
      k = current.kind;
    fields.append(
      el("h3", "npl-settings-title", d.name),
      el("p", "npl-hint", d.description),
    );
    if (k !== "table")
      field(
        fields,
        ["gift", "redpacket"].includes(k) ? "红包祝福语" : "标题 / 名称",
        "title",
      );
    if (k === "gift") {
      field(fields, "发送者名称（留空使用文章作者）", "sender");
      field(fields, "发送者头像（留空使用文章作者）", "image");
    }
    if (
      !["table", "badges", "gallery", "carousel", "compare", "chart"].includes(
        k,
      )
    )
      field(
        fields,
        k === "code" ? "代码内容" : "正文 / 说明",
        "text",
        "textarea",
      );
    if (k === "code") {
      const names = {auto:"自动识别",plaintext:"纯文本",javascript:"JavaScript",typescript:"TypeScript",python:"Python",go:"Go",java:"Java",cpp:"C++",c:"C",csharp:"C#",rust:"Rust",php:"PHP",ruby:"Ruby",swift:"Swift",kotlin:"Kotlin",dart:"Dart",sql:"SQL",bash:"Bash / Shell",powershell:"PowerShell",json:"JSON",yaml:"YAML",xml:"XML",html:"HTML",css:"CSS",scss:"SCSS",markdown:"Markdown",dockerfile:"Dockerfile",nginx:"Nginx"};
      const choices = [...document.querySelectorAll('#npw-languages option')].map(option => [option.value, names[option.value] || option.value]);
      const selected = current.language || "auto";
      if (!choices.some(([value]) => value === selected)) choices.push([selected, selected]);
      const input = field(fields, "代码语言", "language", "text", choices);
      input.value = selected;
      input.dataset.selectUi = "searchable";
      input.setAttribute("aria-label", "选择代码语言");
      const advanced = el("details", "npl-language-custom"), summary = el("summary", "", "其他语言或别名"), custom = el("input");
      custom.type = "text"; custom.maxLength = 40; custom.placeholder = "例如 c++、shell、brainfuck";
      custom.setAttribute("aria-label", "自定义代码语言别名"); custom.autocomplete = "off"; custom.spellcheck = false;
      custom.addEventListener("change", () => {
        const value = custom.value.trim().toLowerCase();
        custom.setCustomValidity(value && !/^[a-z0-9_+.#-]{1,40}$/.test(value) ? "请填写有效的语言别名" : "");
        if (!value || !custom.reportValidity()) return;
        if (![...input.options].some(option => option.value === value)) { const option = el("option", "", value); option.value = value; input.append(option); }
        input.value = value; current.language = value; input.dispatchEvent(new Event("change", {bubbles:true})); previewCurrent();
      });
      advanced.append(summary, custom); fields.append(advanced, el("small", "npl-hint", "可搜索语言名称。自动识别适合未知语言；纯文本保留代码排版，不应用高亮。"));
    }
    if (["download", "gift", "card", "ai"].includes(k))
      field(fields, "跳转链接（HTTPS 或本站路径）", "url");
    if (["image-card", "card", "ai", "mention", "audio", "video"].includes(k))
      field(fields, "封面 / 头像（本站图片路径）", "image");
    if (
      [
        "quote",
        "download",
        "gift",
        "code",
        "table",
        "gallery",
        "carousel",
        "image-card",
        "audio",
        "video",
        "chart",
      ].includes(k)
    )
      field(
        fields,
        k === "gift"
          ? "公开礼物内容 / 兑换码"
          : k === "download"
            ? "提取码"
            : k === "quote"
              ? "引用来源"
              : "图注 / 补充说明",
        "caption",
        k === "gift" ? "textarea" : "text",
      );
    if (k === "redpacket") {
      field(fields, "每份金额（按货币显示单位）", "amount");
      field(fields, "红包份数 · 最多 10000", "count", "number");
      field(fields, "货币标识", "currency");
      fields.append(
        el(
          "p",
          "npl-note",
          "使用文章作者的名称和头像。发布后由作者在前台确认启用，读者点击“開”领取。领取时扣款，需要货币开放转账；修改金额、份数或币种后需要重新启用，调整外观不影响领取记录。",
        ),
      );
    }
    if (["gallery", "carousel"].includes(k)) {
      field(fields, "列数 · 1–6", "columns", "number");
      field(fields, "图片布局", "layout", "text", [
        ["grid", "规整网格"],
        ["mosaic", "拼贴"],
        ["list", "纵向列表"],
      ]);
      if (k === "carousel")
        field(fields, "首尾循环（不自动播放）", "loop", "checkbox");
    }
    if (["gallery", "carousel", "compare"].includes(k))
      itemsEditor([
        ["title", "图片说明"],
        ["image", "本站图片路径"],
        ["text", "补充文字"],
      ]);
    if (["accordion", "tabs"].includes(k)) {
      if (k === "accordion") field(fields, "默认展开", "open", "checkbox");
      itemsEditor([
        ["title", "面板标题"],
        ["text", "面板内容", "textarea"],
      ]);
    }
    if (k === "badges")
      itemsEditor([
        ["title", "标签文字"],
        ["url", "跳转链接（可空）"],
      ]);
    if (k === "table") rowsEditor();
    if (["poll", "rating"].includes(k)) {
      field(fields, "参与方式", "mode", "text", options.mode);
      fields.append(
        el(
          "small",
          "npl-hint",
          "游客按浏览器凭据去重。清理 Cookie 或换设备后无法识别为同一人；需要严格按账户限制请选择登录参与。修改问题、选项或参与方式会开始新一轮，调整外观不影响结果。",
        ),
      );
      if (k === "poll") itemsEditor([["title", "选项文字"]]);
      else {
        const input = field(fields, "评分上限", "max", "text", [
          ["5", "5 分制"],
          ["10", "10 分制"],
        ]);
        input.addEventListener("change", () => {
          current.max = Number(input.value);
        });
      }
    }
    if (["progress", "counter"].includes(k))
      field(fields, "当前数值", "value", "number");
    if (k === "progress" || k === "chart")
      field(fields, "最大值", "max", "number");
    if (["counter", "chart"].includes(k)) field(fields, "单位", "unit");
    if (k === "chart") {
      field(fields, "图表类型", "layout", "text", [
        ["radar", "雷达多边形"],
        ["bar", "柱状图"],
        ["line", "折线图"],
      ]);
      itemsEditor([
        ["title", "指标名称"],
        ["value", "数值", "number"],
      ]);
    }
    if (["audio", "video", "mention"].includes(k)) {
      const providers =
        k === "audio"
          ? [
              ["local", "本站音频"],
              ["netease", "网易云歌曲 ID"],
            ]
          : k === "video"
            ? [
                ["local", "本站视频"],
                ["youtube", "YouTube"],
                ["bilibili", "哔哩哔哩"],
              ]
            : [
                ["github", "GitHub"],
                ["site", "本站用户"],
                ["bilibili", "哔哩哔哩"],
              ];
      field(fields, "来源平台", "provider", "text", providers);
      if (current.provider === "local") {
        field(fields, "媒体文件路径", "url");
        field(fields, "循环播放", "loop", "checkbox");
        itemsEditor([
          ["title", "备用文件说明"],
          ["url", "备用格式文件路径"],
        ]);
        fields.append(
          el(
            "p",
            "npl-hint",
            "浏览器能解码的格式可直接播放；可同时提供 MP3、AAC、Ogg、FLAC、WAV 或 MP4、WebM 等备用文件。暂不支持的格式保留下载入口。",
          ),
        );
      } else
        field(
          fields,
          current.provider === "github"
            ? "GitHub 用户名"
            : current.provider === "youtube"
              ? "视频 ID（11 位）"
              : current.provider === "bilibili" && k === "video"
                ? "BV 号或 av 号"
                : "用户 / 歌曲 ID",
          "uid",
        );
      if (k === "mention")
        fields.append(
          el(
            "p",
            "npl-hint",
            "GitHub 公开资料和头像由本站中转缓存；外站不可达时显示这里填写的资料。哔哩哔哩用户卡使用作者提供的资料。",
          ),
        );
    }
    const style = el("details", "npl-appearance");
    style.open = true;
    style.append(el("summary", "", "外观设置"));
    for (const key of ["gift", "redpacket"].includes(k)
      ? ["width", "density"]
      : ["tone", "style", "width", "density"])
      field(
        style,
        { tone: "配色", style: "背景样式", width: "展示宽度", density: "间距" }[
          key
        ],
        key,
        "text",
        options[key],
      );
    fields.append(style);
    previewCurrent();
    insert.disabled = false;
  }
  let disposePreview = () => {};
  function previewCurrent() {
    if (!current) return;
    const c = current,
      k = c.kind;
    clearTimeout(previewDelay);
    nextPreviewJob = null;
    if (k !== "code") stopPreviewWorker();
    disposePreview();
    disposePreview = () => {};
    preview.replaceChildren();
    const card = el("aside", "npw");
    card.dataset.npWidget = k;
    card.dataset.npwSkin = "chunnel";
    card.dataset.npwTone = c.tone;
    card.dataset.npwStyle = c.style;
    card.dataset.npwDensity = c.density;
    card.dataset.npwWidth = c.width;
    const heading = () => {
      if (c.title) card.append(el("h3", "npw-title", c.title));
    };
    const text = () => {
      if (c.text) card.append(el("p", "npw-text", c.text));
    };
    const picture = (src, alt = "") => {
      if (/^\/(?!\/)/.test(src || "") && !/[\\\s<>"']/.test(src)) {
        const image = el("img", "npw-image");
        image.src = src;
        image.alt = alt;
        image.loading = "lazy";
        return image;
      }
      return el("span", "npl-image-placeholder", "▧");
    };
    const headingIn = (parent) => {
      if (c.title) parent.append(el("h3", "npw-title", c.title));
      if (c.text) parent.append(el("p", "npw-text", c.text));
    };
    if (k === "code") {
      const bar = el("div", "npw-code-bar"),
        dots = el("span", "npw-window-dots");
      dots.append(el("i"), el("i"), el("i"));
      bar.append(
        dots,
        el("span", "npw-code-language", !c.language || c.language === "auto" ? "自动识别" : ["plaintext", "text", "txt"].includes(c.language) ? "纯文本" : c.language),
        el("span", "npw-code-label", c.title),
        el("span", "npw-copy", "⧉ Copy"),
      );
      const pre = el("pre", "npw-code"),
        code = el("code");
      (c.text || "")
        .split("\n")
        .slice(0, 80)
        .forEach((line, i) => {
          const span = el("span", "npw-code-line", line || " ");
          span.dataset.line = String(i + 1);
          code.append(span);
        });
      pre.append(code);
      card.append(bar, pre);
      if ((c.text || "").length <= 48000) {
        const job = {
          id: ++previewSequence,
          code,
          text: (c.text || "").split("\n").slice(0, 80).join("\n"),
          language: c.language,
        };
        previewDelay = setTimeout(() => {
          nextPreviewJob = job;
          pumpPreviewCode();
        }, 180);
      }
    } else if (k === "callout") {
      card.append(
        el(
          "span",
          "npw-callout-icon",
          { amber: "✦", green: "✓", red: "!", purple: "✧" }[c.tone] || "i",
        ),
      );
      const body = el("div", "npw-callout-body");
      if (c.title) body.append(el("h3", "npw-title", c.title));
      body.append(el("p", "npw-text", c.text));
      card.append(body);
    } else if (k === "quote") {
      card.append(el("span", "npw-quote-mark", "“"));
      const b = el("blockquote");
      b.append(
        el("p", "npw-text", c.text),
        el("p", "npw-quote-credit", "—— " + (c.caption || "")),
      );
      card.append(b);
    } else if (k === "table") {
      heading();
      const table = el("table");
      const head = el("thead"),
        body = el("tbody");
      (c.rows || []).slice(0, 8).forEach((row, i) => {
        const tr = el("tr");
        row.forEach((v) => {
          const cell = el(i ? "td" : "th");
          const tone =
            i &&
            (/^[✓✔] /.test(v)
              ? "green"
              : /^◷ /.test(v)
                ? "amber"
                : /^● /.test(v)
                  ? "gray"
                  : "");
          if (tone) {
            const status = el("span", "npw-table-status", v);
            status.dataset.npwTone = tone;
            cell.append(status);
          } else cell.textContent = v;
          tr.append(cell);
        });
        (i ? body : head).append(tr);
      });
      table.append(head, body);
      const wrap = el("div", "npw-table-scroll");
      wrap.append(table);
      card.append(wrap);
    } else if (k === "progress") {
      const row = el("div", "npw-progress-heading");
      row.append(
        el("h3", "npw-title", c.title),
        el("span", "", `${Math.round((c.value / c.max) * 100) || 0}%`),
      );
      const p = el("progress");
      p.max = c.max || 100;
      p.value = c.value || 0;
      card.append(row, p);
      text();
    } else if (k === "badges") {
      const wrap = el("div", "npw-badges");
      c.items.forEach((i) => wrap.append(el("span", "npw-badge", i.title)));
      card.append(wrap);
    } else if (k === "counter") {
      heading();
      const p = el("p", "npw-counter");
      p.append(el("strong", "", c.value), el("span", "", c.unit));
      card.append(p);
      text();
    } else if (["gift", "redpacket"].includes(k)) {
      const invitation = el("button", "npw-packet-launch"),
        main = el("span", "npw-packet-card-main"),
        icon = el("span", "npw-packet-mini"),
        hint = el("p", "npl-packet-hint", "点击卡片预览拆红包效果"),
        stage = el("div", "npl-packet-stage"),
        envelope = el("div", "npw-packet-envelope"),
        flap = el("div", "npw-packet-flap"),
        front = el("div", "npw-packet-front"),
        sender = el("div", "npw-packet-sender");
      invitation.type = "button";
      invitation.setAttribute("aria-label", "预览红包");
      icon.setAttribute("aria-hidden", "true");
      main.append(icon, el("span", "npw-packet-card-title", c.title));
      invitation.append(
        main,
        el(
          "span",
          "npw-packet-card-label",
          k === "gift" ? "礼物红包" : "积分红包",
        ),
      );
      stage.hidden = true;
      if (
        k === "gift" &&
        /^\/(?!\/)/.test(c.image || "") &&
        !/[\\\s<>]/.test(c.image)
      ) {
        const img = el("img", "npw-packet-avatar");
        img.src = c.image;
        img.alt = "";
        sender.append(img);
      } else sender.append(el("span", "npw-packet-avatar-placeholder", ""));
      sender.append(
        el(
          "span",
          "",
          `${k === "gift" && c.sender ? c.sender : "文章作者"}发出的红包`,
        ),
      );
      front.append(sender, el("p", "npw-packet-wish", c.title));
      const coin = el("button", "npw-packet-coin", "開");
      coin.type = "button";
      coin.setAttribute("aria-label", "预览拆红包动效");
      const result = el("div", "npw-packet-result");
      result.hidden = true;
      result.append(
        el("p", "npw-packet-result-label", "效果预览"),
        el(
          "strong",
          k === "gift" ? "npw-packet-result-gift" : "npw-packet-result-amount",
          k === "gift" ? c.caption : c.amount,
        ),
      );
      const reset = el("button", "npw-packet-result-button", "再次预览");
      reset.type = "button";
      reset.addEventListener("click", () => {
        envelope.classList.remove("is-opened");
        result.hidden = true;
        coin.disabled = false;
        coin.focus();
      });
      result.append(reset);
      coin.addEventListener("click", () => {
        envelope.classList.add("is-opened");
        result.hidden = false;
        coin.disabled = true;
        reset.focus({ preventScroll: true });
      });
      const back = el("button", "npl-packet-return", "返回正文卡片");
      back.type = "button";
      invitation.addEventListener("click", () => {
        invitation.hidden = true;
        hint.hidden = true;
        stage.hidden = false;
        coin.focus({ preventScroll: true });
      });
      back.addEventListener("click", () => {
        envelope.classList.remove("is-opened");
        result.hidden = true;
        coin.disabled = false;
        stage.hidden = true;
        invitation.hidden = false;
        hint.hidden = false;
        invitation.focus({ preventScroll: true });
      });
      envelope.append(flap, front, coin, result);
      stage.append(envelope, back);
      card.append(invitation, hint, stage);
    } else if (["accordion", "tabs"].includes(k)) {
      heading();
      const holder = el("div", "npw-panels");
      (c.items || []).forEach((i) => {
        const d = el("details", "npw-panel");
        d.open = !!c.open || k === "tabs";
        const body = el("div", "npw-panel-body");
        body.append(el("p", "", i.text));
        d.append(el("summary", "", i.title), body);
        holder.append(d);
      });
      card.append(holder);
      if (k === "tabs") initTabs(card, { ...c, id: `preview-${c.id}` });
    } else if (k === "compare") {
      heading();
      const panel = el("div", "npw-compare");
      panel.classList.add("is-ready");
      c.items.forEach((i) => {
        const figure = el("figure", "npw-compare-image");
        figure.append(picture(i.image, i.title), el("figcaption", "", i.title));
        panel.append(figure);
      });
      const label = el("label", "npw-compare-control", "拖动比较"),
        range = el("input");
      range.type = "range";
      range.min = "0";
      range.max = "100";
      range.value = "50";
      range.setAttribute("aria-label", "图片对比预览");
      range.addEventListener("input", () =>
        panel.style.setProperty("--npw-compare", `${range.value}%`),
      );
      label.append(range);
      card.append(panel, label);
    } else if (["gallery", "carousel", "image-card"].includes(k)) {
      if (k !== "image-card") heading();
      const wrap = el("div", "npw-gallery");
      wrap.dataset.npwLayout = c.layout || "grid";
      wrap.dataset.npwColumns = String(k === "compare" ? 2 : c.columns || 1);
      (k === "image-card"
        ? [{ title: c.title, image: c.image, text: c.text }]
        : c.items || []
      ).forEach((item) => {
        const f = el("figure", "npw-slide"),
          media = el("span", "npw-lightbox-link");
        if (
          /^\/(?!\/)/.test(item.image || "") &&
          !/[\\\s<>]/.test(item.image)
        ) {
          const img = el("img", "npw-image");
          img.src = item.image;
          img.alt = item.title;
          img.loading = "lazy";
          media.append(img);
        } else media.append(el("div", "npl-image-placeholder", "▧"));
        f.append(media);
        const caption = el("figcaption");
        caption.append(
          el("strong", "", item.title),
          el("span", "", item.text || ""),
        );
        f.append(caption);
        wrap.append(f);
      });
      card.append(wrap);
      if (k === "carousel") {
        const nav = el("nav", "npw-carousel-nav"),
          prev = el("button", "npw-button", "← 上一张"),
          next = el("button", "npw-button", "下一张 →"),
          count = el("span", "npw-carousel-count", `1 / ${c.items.length}`);
        prev.type = next.type = "button";
        prev.dataset.npwAction = "previous";
        next.dataset.npwAction = "next";
        nav.append(prev, count, next);
        card.append(nav);
      }
    } else if (["audio", "video"].includes(k)) {
      const top = el("div", "npw-media-heading"),
        label = el(
          "span",
          "npw-platform",
          {
            local: "本地媒体",
            netease: "网易云音乐",
            youtube: "YouTube",
            bilibili: "哔哩哔哩",
          }[c.provider] || "本地媒体",
        );
      label.dataset.npwPlatform = c.provider || "local";
      top.append(label);
      if (k === "video") card.append(top);
      const stage = el("div", "npw-media-stage"),
        info = el("div", "npw-media-info");
      headingIn(info);
      if (k === "audio") {
        info.append(top);
        const cover = el("div", "npw-audio-cover");
        cover.append(
          c.image ? picture(c.image) : el("span", "npw-audio-record", "♪"),
        );
        stage.append(cover, info);
      } else if (c.image) stage.append(picture(c.image));
      const play = el("button", "npw-media-play");
      play.type = "button";
      play.setAttribute("aria-label", "播放按钮预览");
      play.append(el("span", "npw-play-icon"));
      play.addEventListener("click", () =>
        say("播放器效果请在保存后的文章中体验。"),
      );
      stage.append(play);
      card.append(stage);
      if (k === "video") card.append(info);
      const links = el("div", "npw-media-links");
      links.append(
        el(
          "span",
          "npw-link",
          c.provider === "local" ? "下载原文件" : "在原平台打开",
        ),
      );
      card.append(links);
      renderPlatform(card, c);
    } else if (k === "download") {
      const heading = el("div", "npw-download-heading");
      const symbol = el("span", "npw-symbol", "↓");
      symbol.setAttribute("aria-hidden", "true");
      const body = el("div", "npw-download-body");
      headingIn(body);
      heading.append(symbol, body);
      card.append(heading);
      if (c.caption) {
        const codes = el("div", "npw-download-code");
        const meta = el("p", "npw-meta");
        const password = el("code", "", c.caption);
        password.tabIndex = 0;
        password.title = c.caption;
        meta.append(el("span", "", "提取码"), password);
        const copy = el("span", "npw-button", "复制");
        copy.setAttribute("title", "复制提取码");
        codes.append(meta, copy);
        card.append(codes);
      }
      if (c.url) card.append(el("span", "npw-link", "前往下载"));
    } else if (["card", "ai"].includes(k)) {
      if (k === "card" && c.image) card.append(picture(c.image));
      const body = el("div", "npw-card-content");
      if (k === "ai") body.append(el("span", "npw-ai-label", "✧ AI 辅助创作"));
      headingIn(body);
      if (k === "ai" && c.image) body.append(picture(c.image));
      if (c.url) body.append(el("span", "npw-link", "了解更多"));
      card.append(body);
    } else if (["poll", "rating"].includes(k)) {
      heading();
      text();
      const options = el("div", "npw-vote-options");
      if (k === "poll")
        c.items.forEach((i) => {
          const option = el("button", "npw-vote");
          option.type = "button";
          option.append(
            el("span", "", i.title),
            el("span", "npw-vote-count", "0 · 0%"),
          );
          options.append(option);
        });
      else
        for (let i = 1; i <= Number(c.max || 5); i++) {
          const star = el("button", "npw-star");
          star.type = "button";
          star.setAttribute("aria-label", `${i} 分预览`);
          const shape = el("span", "npw-star-shape");
          shape.setAttribute("aria-hidden", "true");
          star.append(shape, el("small", "", i));
          options.append(star);
        }
      const results = el("p", "npw-results");
      if (k === "rating")
        results.append(
          el("strong", "npw-result-value", "—"),
          document.createTextNode(` / ${c.max} 分 · 0 人参与`),
        );
      else results.textContent = "0 人参与";
      card.append(options, results);
      card.append(
        el(
          "small",
          "npw-meta",
          {
            login: "登录用户每人一次",
            guest: "游客参与，同一浏览器一次",
            both: "登录或游客参与，同一身份一次",
          }[c.mode],
        ),
      );
    } else if (k === "chart") {
      const meta = el("div", "npw-chart-meta");
      meta.append(el("span", "", {radar: "雷达多边形", bar: "柱状图", line: "折线图"}[c.layout]), el("span", "", `刻度上限 ${c.max}${c.unit || ""}`));
      card.append(meta);
      heading();
      card.append(el("div", "npw-chart"));
      const data = el("details", "npw-chart-data");
      data.append(el("summary", "", "查看图表数据"));
      const table = el("table");
      c.items.forEach((i) => {
        const tr = el("tr");
        tr.append(
          el("td", "", i.title),
          el("td", "", `${i.value} ${c.unit || ""}`),
        );
        table.append(tr);
      });
      data.append(table);
      card.append(data);
    } else if (["annotation", "glossary"].includes(k)) {
      const detail = el("details", "npw-definition"),
        summary = el("summary");
      summary.append(el("mark", "", c.title), el("span", "", " ⓘ"));
      detail.append(summary, el("p", "", c.text));
      card.append(detail);
    } else if (k === "mention") {
      const wrap = el("div", "npw-mention"),
        link = el("a", "npw-mention-link", `@${c.title || c.uid} `);
      link.href = "#";
      link.addEventListener("click", (e) => e.preventDefault());
      const platform = el(
        "span",
        "npw-platform",
        { site: "本站", github: "GitHub", bilibili: "哔哩哔哩" }[c.provider],
      );
      platform.dataset.npwPlatform = c.provider;
      link.append(platform);
      const profile = el("div", "npw-user-card");
      profile.append(el("div", "npw-user-cover"));
      if (c.image) profile.append(picture(c.image));
      headingIn(profile);
      profile.append(el("span", "npw-link", "查看主页"));
      wrap.append(link, profile);
      card.append(wrap);
      renderPlatform(card, c);
    } else {
      heading();
      text();
    }
    if (c.caption && !["quote", "download", "gift", "redpacket"].includes(k))
      card.append(el("p", "npw-caption", c.caption));
    preview.append(card);
    if (k === "chart") disposePreview = drawChart(card, c);
    if (k === "carousel") disposePreview = initCarousel(card, c);
    if (["mention", "glossary"].includes(k)) disposePreview = initPopover(card);
    dialog.querySelector("[data-component-name]").textContent = map[k].name;
  }
  function open(event, captured) {
    disposePreview();
    disposePreview = () => {};
    if (source?.disabled || source?.readOnly) return;
    trigger = event?.currentTarget || document.activeElement;
    selection = captured || window.ChunnelBlockEditor.capture();
    editing = false;
    current = null;
    insert.disabled = true;
    fields.replaceChildren(
      el("p", "npl-empty", "选择一个组件，开始设置内容与外观。"),
    );
    preview.replaceChildren(el("p", "npl-empty", "预览会显示在这里"));
    category = "全部";
    search.value = "";
    categories
      .querySelectorAll("button")
      .forEach((b) =>
        b.setAttribute("aria-pressed", String(b.textContent === category)),
      );
    showList();
    say("");
    dialog.showModal();
    search.focus();
  }
  api.edit = (config, captured, button) => {
    trigger = button;
    selection = captured;
    editing = true;
    current = structuredClone(config);
    category = "全部";
    search.value = "";
    showList();
    renderFields();
    insert.textContent = "保存组件设置";
    dialog.showModal();
  };
  api.open = (button, captured) => open({currentTarget: button}, captured);
  if (dialog.dataset.componentLazy !== 'true') document
    .querySelectorAll("[data-component-library-open]")
    .forEach((b) => b.addEventListener("click", open));
  dialog
    .querySelector("[data-component-close]")
    .addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => {
    disposePreview();
    disposePreview = () => {};
    stopPreviewWorker();
    validation?.abort();
    validation = null;
    validationBusy(false);
    insert.textContent = "插入组件";
    trigger?.focus();
  });
  insert.addEventListener("click", async () => {
    if (!current || validation) return;
    const controller = new AbortController();
    let timeout;
    try {
      const snapshot = structuredClone(current),
        captured = selection,
        wasEditing = editing;
      validate(snapshot);
      const token = document.querySelector(
        '[data-content-editor] [name="csrf_token"]',
      )?.value;
      if (!token) throw new Error("编辑会话已失效，请保存草稿并刷新页面");
      validation = controller;
      validationBusy(true);
      say("正在检查组件设置…");
      timeout = setTimeout(() => controller.abort(), 14000);
      const response = await fetch(
        source?.closest("[data-content-editor]")?.dataset.componentValidateUrl || "/api/v1/admin/article-components/validate",
        {
          method: "POST",
          credentials: "same-origin",
          cache: "no-store",
          redirect: "error",
          signal: controller.signal,
          headers: {
            "Content-Type": "application/json",
            Accept: "application/json",
            "X-CSRF-Token": token,
          },
          body: JSON.stringify({ config: encode(snapshot) }),
        },
      );
      const result = await response.json();
      if (!response.ok)
        throw new Error(
          result?.message ||
            result?.error?.message ||
            "组件校验失败，请检查设置后重试",
        );
      if (
        validation !== controller ||
        !dialog.open ||
        controller.signal.aborted
      )
        return;
      window.ChunnelBlockEditor.insert(
        (wasEditing ? "" : "\n") +
          markup(decode(result.config)) +
          (wasEditing ? "" : "\n"),
        captured,
      );
      dialog.close();
    } catch (e) {
      if (dialog.open && (!validation || validation === controller))
        say(
          e.name === "AbortError"
            ? "校验超时，设置已保留，请重试。"
            : e.message,
        );
    } finally {
      clearTimeout(timeout);
      if (validation === controller) {
        validation = null;
        validationBusy(false);
      }
    }
  });
  window.addEventListener("pagehide", () => {
    disposePreview();
    disposePreview = () => {};
    validation?.abort();
    stopPreviewWorker();
  });
  window.addEventListener("pageshow", event => {
    if (event.persisted && dialog.open && current) previewCurrent();
  });
  function validate(c) {
    if (
      c.kind === "poll" &&
      (c.items.length < 2 || c.items.some((i) => !i.title.trim()))
    )
      throw new Error("投票至少需要两个非空选项");
    if (
      c.kind === "chart" &&
      (c.items.length < 2 ||
        (c.layout === "radar" && (c.items.length < 3 || c.items.length > 12)) ||
        c.max <= 0 ||
        c.items.some((i) => i.value < 0 || i.value > c.max))
    )
      throw new Error("请检查图表数值，雷达图需要 3–12 个指标");
    if (c.kind === "progress" && (c.max <= 0 || c.value < 0 || c.value > c.max))
      throw new Error("进度应在 0 到最大值之间");
    if (
      c.kind === "redpacket" &&
      (!/^[0-9]+(\.[0-9]{1,6})?$/.test(c.amount) ||
        Number(c.amount) <= 0 ||
        !Number.isInteger(c.count) ||
        c.count < 1 ||
        c.count > 10000)
    )
      throw new Error("请填写有效的红包金额和份数");
    for (const item of [c, ...(c.items || [])]) {
      if (item.url) {
        let valid = false;
        try {
          const u = new URL(item.url, location.href);
          valid =
            ((item.url.startsWith("/") &&
              !item.url.startsWith("//") &&
              u.origin === location.origin) ||
              u.protocol === "https:") &&
            !u.username &&
            !u.password &&
            !/[\\\s<>"']/.test(item.url);
        } catch {}
        if (!valid) throw new Error("链接需要 HTTPS 地址或本站路径");
      }
      if (item.image && !/^\/(?!\/)/.test(item.image))
        throw new Error("图片请使用本站媒体路径");
    }
    if (
      ["video", "audio"].includes(c.kind) &&
      c.provider === "local" &&
      [c.url, ...(c.items || []).map((i) => i.url)]
        .filter(Boolean)
        .some((u) => !/^\/(?!\/)/.test(u))
    )
      throw new Error("本地媒体请使用本站路径");
    if (c.provider === "youtube" && !/^[\w-]{11}$/.test(c.uid))
      throw new Error("YouTube 视频 ID 需要 11 位字符");
    if (
      c.provider === "bilibili" &&
      c.kind === "video" &&
      !/^(BV[a-zA-Z0-9]{10}|av[1-9][0-9]{0,18})$/.test(c.uid)
    )
      throw new Error("请填写正确的 BV 号或 av 号");
    if (
      ["netease", "site"].includes(c.provider) ||
      (c.provider === "bilibili" && c.kind === "mention")
    ) {
      if (!/^[1-9][0-9]{0,18}$/.test(c.uid))
        throw new Error("请填写正确的数字 ID");
    }
    if (
      c.provider === "github" &&
      !/^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$/.test(c.uid)
    )
      throw new Error("GitHub 用户名格式不正确");
    encode(c);
  }
})();
