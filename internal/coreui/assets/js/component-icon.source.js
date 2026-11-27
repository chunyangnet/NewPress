// Local sprite generated from the pinned Lucide subset; no external requests.
function componentIcon(name) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  for (const [key, value] of Object.entries({viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.75', 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true', focusable: 'false'})) svg.setAttribute(key, value);
  const use = document.createElementNS(ns, 'use');
  use.setAttribute('href', `#icon-${name}`);
  svg.append(use);
  return svg;
}
module.exports = { componentIcon };
