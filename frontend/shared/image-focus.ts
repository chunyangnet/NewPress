export function focusPercent(value: string | number): number | null {
  if (typeof value === 'string' && !value.trim()) return null;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? Math.max(0, Math.min(100, parsed)) : null;
}

// Match the server's focal crop, expressed as CSS object-position percentages.
export function cropPosition(width: number, height: number, ratio: number, x: number, y: number): [number, number] | null {
  if (![width, height, ratio, x, y].every(Number.isFinite) || width <= 0 || height <= 0 || ratio <= 0) return null;
  const cw = Math.min(width, height * ratio), ch = Math.min(height, width / ratio);
  const left = Math.max(0, Math.min(width - cw, width * (focusPercent(x) ?? 50) / 100 - cw / 2));
  const top = Math.max(0, Math.min(height - ch, height * (focusPercent(y) ?? 50) / 100 - ch / 2));
  return [width === cw ? 50 : left / (width - cw) * 100, height === ch ? 50 : top / (height - ch) * 100];
}
