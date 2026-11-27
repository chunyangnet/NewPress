import { $, $$ } from './shared.mjs';

export function initComments() {
  const form = $('[data-comment-form]'); if (!form) return;
  const parent = $('input[name="parent_id"]', form), field = $('textarea[name="content"]', form);
  const banner = $('[data-reply-context]', form), label = $('[data-reply-label]', form), cancel = $('[data-reply-cancel]', form);
  let trigger;
  const setReply = (id, name) => {
    parent.value = id; banner.hidden = id === '0'; label.textContent = name ? '回复 ' + name : '正在回复评论 #' + id;
    $$('[data-comment-reply]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.commentReply === id)));
  };
  $$('[data-comment-reply]').forEach(button => {
    button.hidden = false;
    button.addEventListener('click', () => { trigger = button; setReply(button.dataset.commentReply, button.dataset.replyAuthor); field.focus(); });
  });
  cancel.addEventListener('click', () => { setReply('0'); if (trigger?.isConnected) trigger.focus(); else field.focus(); });
  setReply(parent.value || '0', $$('[data-comment-reply]').find(button => button.dataset.commentReply === parent.value)?.dataset.replyAuthor);
  const count = $('[data-comment-count]', form);
  if (count) {
    const update = () => { count.textContent = [...field.value].length + ' / ' + field.maxLength; };
    field.addEventListener('input', update); update();
  }
}
