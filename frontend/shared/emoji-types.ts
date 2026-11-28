export interface EmojiRange { start: number; end: number; direction?: 'none' | 'forward' | 'backward' }
export interface EmojiEditor {
  editor: HTMLDivElement;
  selection(): EmojiRange;
  replace(text: string, range?: EmojiRange, kind?: string): string;
}
export type EmojiEditorFactory = (input: HTMLTextAreaElement, options: {
  fragment(text: string): DocumentFragment;
  validate(input: HTMLTextAreaElement, value: string): string;
}) => EmojiEditor;
declare global { interface Window { ChunnelEmojiEditor?: EmojiEditorFactory } }
