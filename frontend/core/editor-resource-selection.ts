import { Slice } from 'prosemirror-model';
import type { EditorState, Transaction } from 'prosemirror-state';
import { closeHistory } from 'prosemirror-history';
import type { createDocumentCodec } from './editor-document';

type Codec = Pick<ReturnType<typeof createDocumentCodec>, 'parse' | 'blockHTML' | 'validate'>;

// Keep the selected fragment as HTML, including partial paragraphs and marks.
// The caller publishes private content only after the document transaction succeeds.
export function resourceSelection(state: EditorState, markup: string, codec: Codec, tail = false): { html: string; transaction: Transaction } {
  const from = state.selection.from, to = tail ? state.doc.content.size : state.selection.to;
  if (to <= from) throw new Error('请先在正文中选择需要隐藏的内容。');
  const pieces: string[] = [];
  state.doc.slice(from, to, true).content.forEach(node => pieces.push(codec.blockHTML(node)));
  const transaction = closeHistory(state.tr).replaceRange(from, to, new Slice(codec.parse(markup).content, 0, 0));
  const error = codec.validate(transaction.doc);
  if (error) throw new Error(error);
  return { html: pieces.join(''), transaction };
}
