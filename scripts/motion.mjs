// Shared spring presets. These are NewPress values, not private iOS constants.
// Sample the unit step response of x'' + 2*zeta*omega*x' + omega^2*x = omega^2.
export function springCurve(damping = 1, samples = 24) {
  return Array.from({ length: samples + 1 }, (_, index) => {
    if (!index) return '0';
    if (index === samples) return '1';
    const t = 9 * index / samples;
    const s = Math.sqrt(1 - damping * damping);
    const value = damping === 1 ? 1 - (1 + t) * Math.exp(-t)
      : 1 - Math.exp(-damping * t) * (Math.cos(s * t) + damping / s * Math.sin(s * t));
    return String(Math.round(value * 10000) / 10000);
  }).join(',');
}

export const motionCSS = `@supports (transition-timing-function: linear(0, 1)) {
  :root { --motion-ease: linear(${springCurve()}); --motion-spring: linear(${springCurve(.82)}); }
}`;
