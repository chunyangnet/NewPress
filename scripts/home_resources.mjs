import postcss from 'postcss';
import selectorParser from 'postcss-selector-parser';

// Only move rules that require an internal overlay component. Shells, buttons,
// generic inputs, navigation, states and shared :is() branches stay in the first
// sheet, so a closed dialog never changes the visible page when it is loaded.
// On home, emoji editors and their controls are created by the deferred feature
// script inside conversations; the feed renders content with .newpress-emoji.
const internal = /^(?:oauth-|message-(?!unread)|conversation-|contact-|saved-|notification-settings-|interaction-(?:login|register|info|drawer|tabs|notices|connection|error|load-more|empty)|quick-actions|toast(?:$|-)|emoji-(?:popover|grid|category|tab|item|empty|editor|source|trigger|search|ai-label|info))/;
function requiresOverlay(selector) {
  return selector.nodes.some(node =>
    node.type === 'class' && internal.test(node.value) ||
    node.type === 'pseudo' && [':is', ':where'].includes(node.value) && node.nodes.length && node.nodes.every(requiresOverlay));
}

export function splitHomeOverlays(css) {
  const initial = postcss.parse(css), overlays = postcss.root();
  const parents = new Map([[initial, overlays]]);
  const destination = parent => {
    if (!parents.has(parent)) {
      const clone = parent.clone({ nodes: [] });
      destination(parent.parent).append(clone);
      parents.set(parent, clone);
    }
    return parents.get(parent);
  };
  initial.walkRules(rule => {
    if (rule.parent.type === 'atrule' && rule.parent.name.endsWith('keyframes')) return;
    const selectors = selectorParser().astSync(rule.selector);
    if (selectors.nodes.length && selectors.nodes.every(requiresOverlay)) {
      destination(rule.parent).append(rule.clone());
      rule.remove();
    }
  });
  initial.walkAtRules(rule => { if (rule.nodes && !rule.nodes.length) rule.remove(); });
  return { initial: initial.toString(), overlays: overlays.toString() };
}
