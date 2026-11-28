import hljs from "highlight.js";
// All bundled grammars stay off the main thread. Unknown labels use plain text.
const common = [
  "javascript",
  "typescript",
  "python",
  "go",
  "java",
  "cpp",
  "csharp",
  "rust",
  "bash",
  "json",
  "xml",
  "css",
  "sql",
  "yaml",
  "markdown",
];
self.onmessage = (event: MessageEvent<unknown>) => {
  const data = event.data !== null && typeof event.data === 'object' && !Array.isArray(event.data) ? event.data as Record<string, unknown> : null;
  if (!data || typeof data.text !== "string" || data.text.length > 48000) {
    self.postMessage({ id: data?.id, error: true });
    return;
  }
  const lang = String(data.language || "auto").trim().toLowerCase();
  try {
    const result =
      lang === "auto"
        ? hljs.highlightAuto(data.text, common)
        : hljs.highlight(data.text, {
            language: hljs.getLanguage(lang) ? lang : "plaintext",
            ignoreIllegals: true,
          });
    const language = result.language || (lang === "auto" ? "plaintext" : lang);
    const grammar = hljs.getLanguage(language);
    const label = grammar === hljs.getLanguage("plaintext")
      ? "纯文本"
      : grammar?.name || language;
    // Unknown explicit labels stay visible; their content is safely escaped.
    self.postMessage({ id: data.id, html: result.value, language, label: !hljs.getLanguage(lang) && lang !== "auto" ? lang : label });
  } catch {
    self.postMessage({ id: data.id, error: true });
  }
};
