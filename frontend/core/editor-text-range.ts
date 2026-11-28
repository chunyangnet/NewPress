import type { EmojiRange } from '../shared/emoji-types';

// Source formatting is available before the optional emoji picker is loaded.
export function replaceEditorText(input: HTMLTextAreaElement, text: string, range: EmojiRange): string {
  if (!input.isConnected || input.disabled || input.readOnly || input.matches(':disabled')) return '当前内容不可编辑';
  const candidate = input.value.slice(0, range.start) + text + input.value.slice(range.end);
  const bytes = Number(input.dataset.emojiMaxBytes);
  const maximum = Number(input.dataset.emojiMax || input.maxLength);
  if (bytes > 0) {
    if (new TextEncoder().encode(candidate.trim()).length > bytes) return '正文内容超过长度上限，请删减后重试';
  } else if (maximum > 0 && [...candidate].length > maximum) return `最多输入 ${maximum} 个字符`;
  input.focus({ preventScroll: true });
  input.setSelectionRange(range.start, range.end);
  let inserted = false;
  try { if (document.activeElement === input) inserted = document.execCommand('insertText', false, text); } catch { /* Use the range API below. */ }
  if (!inserted) input.setRangeText(text, range.start, range.end, 'end');
  input.dispatchEvent(new Event('input', { bubbles: true }));
  return '';
}
