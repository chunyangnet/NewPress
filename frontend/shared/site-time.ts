// Date labels are calendar values; timestamps are instants with an explicit zone.
// Unknown input is deliberate: this boundary validates API and DOM values.
type CalendarParts = { year: string; month: string; day: string };
type DateParts = Partial<Record<Intl.DateTimeFormatPartTypes, string>>;
const zeroTime = Date.parse('0001-01-01T00:00:00Z');

function calendarParts(year: string, month: string, day: string): CalendarParts | null {
  const y = Number(year), m = Number(month), d = Number(day);
  const leap = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return m >= 1 && m <= 12 && d >= 1 && d <= (days[m - 1] ?? 0) ? { year, month, day } : null;
}

export function calendar(value: unknown): CalendarParts | null {
  if (typeof value !== 'string') return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match || match[0] !== value || value === '0001-01-01') return null;
  const [, year = '', month = '', day = ''] = match;
  return calendarParts(year, month, day);
}

export function instant(value: unknown): Date | null {
  let milliseconds: number;
  if (typeof value === 'number') milliseconds = value;
  else if (value && typeof value === 'object') {
    // The Date brand check also accepts Dates from another window.
    try { milliseconds = Date.prototype.getTime.call(value); } catch { return null; }
  } else if (typeof value === 'string') {
    const match = /^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:[Zz]|[+-](\d{2}):(\d{2}))$/.exec(value);
    if (!match || match[0] !== value) return null;
    const [, y = '', m = '', d = '', h = '', min = '', sec = '', oh = '0', om = '0'] = match;
    if (!calendarParts(y, m, d) || Number(h) > 23 || Number(min) > 59 || Number(sec) > 59 || Number(oh) > 23 || Number(om) > 59) return null;
    milliseconds = Date.parse(value);
  } else return null;
  if (!Number.isFinite(milliseconds)) return null;
  const parsed = new Date(milliseconds), clipped = parsed.getTime();
  return Number.isNaN(clipped) || clipped === zeroTime ? null : parsed;
}

export function createSiteTime(readZone: () => string | null | undefined) {
  let cached = '';
  function configured(): string {
    if (cached) return cached;
    let value = '';
    try { value = (readZone() || '').trim(); } catch { value = ''; }
    if (value && value !== 'Local') {
      try { new Intl.DateTimeFormat('en-US', { timeZone: value }); cached = value; return cached; } catch { /* Fall back to UTC. */ }
    }
    cached = 'UTC';
    return cached;
  }
  function parts(value: unknown, options: Intl.DateTimeFormatOptions): DateParts | null {
    const label = calendar(value);
    if (label) return options.hour || options.minute || options.second ? null : label;
    const parsed = instant(value);
    if (!parsed) return null;
    let format: Intl.DateTimeFormat;
    try { format = new Intl.DateTimeFormat('en-CA', { timeZone: configured(), hourCycle: 'h23', ...options }); } catch { return null; }
    const out: DateParts = {};
    for (const item of format.formatToParts(parsed)) if (item.type !== 'literal') out[item.type] = item.value;
    if (out.year) out.year = out.year.padStart(4, '0');
    return out;
  }
  const pad = (value: string | undefined, size = 2) => String(value || '').padStart(size, '0');
  const year = (p: DateParts) => p.year || '';
  function formatDate(value: unknown) { const p = parts(value, { year: 'numeric', month: '2-digit', day: '2-digit' }); return p ? `${year(p)}-${pad(p.month)}-${pad(p.day)}` : ''; }
  function formatMonth(value: unknown) { const p = parts(value, { year: 'numeric', month: '2-digit' }); return p ? `${year(p)}-${pad(p.month)}` : ''; }
  function formatClock(value: unknown) { const p = parts(value, { hour: '2-digit', minute: '2-digit', second: '2-digit' }); return p ? `${pad(p.hour)}:${pad(p.minute)}:${pad(p.second)}` : ''; }
  function formatTime(value: unknown) { const p = parts(value, { hour: '2-digit', minute: '2-digit' }); return p ? `${pad(p.hour)}:${pad(p.minute)}` : ''; }
  function formatDateTime(value: unknown) { const p = parts(value, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }); return p ? `${year(p)}-${pad(p.month)}-${pad(p.day)} ${pad(p.hour)}:${pad(p.minute)}` : ''; }
  function formatDateTimeSeconds(value: unknown) { const p = parts(value, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }); return p ? `${year(p)}-${pad(p.month)}-${pad(p.day)} ${pad(p.hour)}:${pad(p.minute)}:${pad(p.second)}` : ''; }
  function formatDateDot(value: unknown) { const p = parts(value, { year: 'numeric', month: '2-digit', day: '2-digit' }); return p ? `${year(p)}.${pad(p.month)}.${pad(p.day)}` : ''; }
  function sameDay(left: unknown, right: unknown) {
    const a = parts(left, { year: 'numeric', month: '2-digit', day: '2-digit' }), b = parts(right, { year: 'numeric', month: '2-digit', day: '2-digit' });
    return !!(a && b && a.year === b.year && a.month === b.month && a.day === b.day);
  }
  function isToday(value: unknown) { return sameDay(value, new Date()); }
  function relative(value: unknown) {
    const p: DateParts | null = calendar(value) || parts(value, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
    if (!p) return '';
    const now = parts(new Date(), { year: 'numeric', month: '2-digit', day: '2-digit' });
    if (now && p.year === now.year && p.month === now.month && p.day === now.day && p.hour !== undefined) return `${pad(p.hour)}:${pad(p.minute)}`;
    const month = Number(p.month), day = Number(p.day);
    return now && p.year !== now.year ? `${year(p)}/${month}/${day}` : `${month}/${day}`;
  }
  return Object.freeze({ zone: configured, valid: (value: unknown) => !!(calendar(value) || instant(value)), formatDate, formatMonth, formatDateDot, formatTime, formatClock, formatDateTime, formatDateTimeSeconds, sameDay, isToday, relative });
}

export type SiteTime = ReturnType<typeof createSiteTime>;
