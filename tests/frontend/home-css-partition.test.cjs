const test = require('node:test');
const assert = require('node:assert/strict');

test('home CSS only defers selectors that require an overlay component', async () => {
  const { splitHomeOverlays } = await import('../../scripts/home_resources.mjs');
  const { initial, overlays } = splitHomeOverlays(`
    [hidden] { display: none }
    .site-header :is(.message-unread-count, .account-menu) { color: blue }
    body:not(.interaction-login) .header-search { color: red }
    :is(.interaction-login, .header-search) input { border: 1px solid }
    @media (max-width: 700px) { .interaction-login input { width: 100% } .home-feed { gap: 12px } }
    @supports (display: grid) { :is(.message-thread, .conversation-list) { display: grid } }
    .saved-list, .saved-note { padding: 12px }
    .emoji-editor, .emoji-source { white-space: pre-wrap }
    .emoji-trigger:hover, .emoji-search input, .emoji-ai-label { color: blue }
    .newpress-emoji { display: inline-block }
    :is(input, .emoji-editor):focus-within { outline: 1px solid }
    dialog.ui-modal, dialog.ui-drawer { padding: 0 }
    .modal-backdrop, .drawer-backdrop { position: fixed }
    dialog .modal-close, dialog.ui-drawer header button { border-radius: 8px }
    @media (max-width: 450px) { .ui-drawer > section { width: 100% } }
    body:not(.ui-modal) .site-header { margin: 0 }
    :is(.ui-modal, .cookie-dialog) > section { color: blue }
    .modal-open, .ui-modal-launcher, .drawer-backdrop-preview { color: red }
    .brand-type b, .ui-modal h2 { font: inherit }
    @keyframes toast-in { from { opacity: 0 } to { opacity: 1 } }
  `);
  for (const selector of ['[hidden]', '.message-unread-count', ':not(.interaction-login)', ':is(.interaction-login, .header-search)', '.home-feed', '@keyframes toast-in']) assert.ok(initial.includes(selector), selector);
  assert.ok(overlays.includes('@media (max-width: 700px)'));
  assert.ok(overlays.includes('.interaction-login input'));
  assert.ok(overlays.includes('@supports (display: grid)'));
  assert.ok(overlays.includes('.message-thread'));
  assert.ok(overlays.includes('.saved-list, .saved-note'));
  assert.ok(overlays.includes('.emoji-editor, .emoji-source'));
  assert.ok(overlays.includes('.emoji-trigger:hover, .emoji-search input, .emoji-ai-label'));
  assert.ok(initial.includes('.newpress-emoji'));
  assert.ok(initial.includes(':is(input, .emoji-editor):focus-within'));
  assert.ok(!overlays.includes('.header-search'));
  assert.ok(!overlays.includes('@keyframes'));
  for (const selector of ['dialog.ui-modal, dialog.ui-drawer', '.modal-backdrop, .drawer-backdrop', 'dialog .modal-close', '.ui-drawer > section']) assert.ok(overlays.includes(selector), selector);
  for (const selector of [':not(.ui-modal)', ':is(.ui-modal, .cookie-dialog)', '.modal-open', '.ui-modal-launcher', '.drawer-backdrop-preview', '.brand-type b, .ui-modal h2']) assert.ok(initial.includes(selector), selector);
});
