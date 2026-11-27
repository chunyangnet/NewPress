import { initializeCommentEditor } from './admin-comment-controls';
const editor = document.querySelector<HTMLElement>('[data-comment-editor]');
if (editor && window.ChunnelAdmin) initializeCommentEditor(editor, window.ChunnelAdmin.icon);
