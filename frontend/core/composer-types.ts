import type { EditorSourceSelection } from './media-types';

export type BlockKind = 'paragraph' | 'h2' | 'h3' | 'quote' | 'code' | 'callout' | 'list' | 'table' | 'image' | 'post' | 'component' | 'html';
export interface ComponentConfig {
  id: string;
  kind: string;
  title?: string;
  [key: string]: unknown;
}
// Structured fields are validated again by renderBlock before producing HTML.
// Untouched blocks retain their exact raw representation, including whitespace.
export interface ComposerBlock {
  id: string;
  kind: BlockKind;
  prefix?: string;
  raw?: string;
  dirty?: boolean;
  text?: string;
  language?: string;
  tone?: string;
  ordered?: boolean;
  rows?: string[][];
  url?: string;
  alt?: string;
  caption?: string;
  width?: number;
  height?: number;
  postID?: number;
  layout?: string;
  title?: string;
  config?: ComponentConfig;
}
export interface ComposerState { blocks: ComposerBlock[]; tail: string }
export interface EditorTextSelection { from: number; to: number; text: string; body: string }
export interface BlockEditorAPI {
  reset(value?: string): void;
  capture(): EditorSourceSelection;
  insert(markup: string, selection: EditorSourceSelection): void;
  parse(value: string): ComposerState;
  serialize(state: unknown): string;
  renderBlock(block: unknown): string;
  localImageURL(value: unknown): boolean;
  localPostURL(value: unknown): boolean;
  replaceSelectedHTML?(markup: string, tail?: boolean): string | null;
  captureText?(): EditorTextSelection | null;
  replaceText?(value: string, selection: EditorTextSelection): boolean;
  focus?(): void;
  setMode?(mode: string): void;
}
declare global {
  interface Window { ChunnelBlockEditor?: BlockEditorAPI }
}
