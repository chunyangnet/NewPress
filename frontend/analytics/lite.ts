// Progressive enhancement of the Go report; no requests or chart dependencies.
import { compact, display, isSeries, makeSeries, nearestIndex, number, percent, readCount, readNativeReport, sourceBreakdown } from './lite-data';
import type { SeriesKey } from './lite-data';

type ChartLifecycle = { pause(): void; resume(): void; dispose(): void };
const charts = new Map<HTMLElement, ChartLifecycle>();
let suspended = false;
const names = { views: '页面浏览', searches: '主动搜索', misses: '无结果搜索' };
const emptyTitles = { views: '还没有访问记录', searches: '本期还没有搜索记录', misses: '本期没有无结果搜索' };
function svgNode<K extends keyof SVGElementTagNameMap>(tag: K, attributes: Record<string, string | number>) {
  const node = document.createElementNS('http://www.w3.org/2000/svg', tag);
  Object.entries(attributes).forEach(([name, value]) => node.setAttribute(name, String(value)));
  return node;
}

function mount(workspace: HTMLElement): ChartLifecycle | null {
  const fallback = workspace.querySelector<HTMLElement>('[data-analytics-fallback]');
  if (!fallback) return null;
  const all = <T extends Element = HTMLElement>(selector: string) => [...fallback.querySelectorAll<T>(selector)];
  const find = <T extends Element = HTMLElement>(selector: string) => fallback.querySelector<T>(selector);
  const text = (selector: string, value: string) => all(selector).forEach(node => { node.textContent = value; });
  const sourceRows = all('[data-source-count]');
  const report = readNativeReport(workspace.dataset, all('[data-analytics-daily] tbody tr').map(row => row.dataset), sourceRows.map(row => row.dataset));
  if (!report) return null;
  const { rows, totals, timezone } = report;
  const chart = find<SVGSVGElement>('[data-chart]'), shell = find('[data-chart-shell]');
  const panel = chart?.closest<HTMLElement>('.analytics-trend');
  const cursor = find<SVGGElement>('[data-chart-cursor]'), tooltip = find('[data-chart-tooltip]');
  const line = cursor?.querySelector('line'), dot = cursor?.querySelector('circle');
  const time = tooltip?.querySelector('time'), value = tooltip?.querySelector('b');
  const labels = find('[data-chart-labels]'), drawing = find<SVGGElement>('[data-chart-series]');
  const controls = find('[data-chart-switch]'), empty = find('[data-chart-empty]');
  if (!chart || !shell || !panel || !cursor || !tooltip || !line || !dot || !time || !value || !labels || !drawing || !controls || !empty) return null;
  const abort = new AbortController(), options = { signal: abort.signal };
  let key: SeriesKey = 'views', series = makeSeries(rows, key), activeIndex = Math.max(0, rows.length - 1);
  let active = false, disposed = false;
  const hidePoint = () => { cursor.setAttribute('visibility', 'hidden'); tooltip.hidden = true; };
  const showPoint = (index: number, announce = false) => {
    const point = series.points[index], row = rows[index];
    if (!active || !point || !row) return;
    activeIndex = index;
    line.setAttribute('x1', String(point.x)); line.setAttribute('x2', String(point.x));
    dot.setAttribute('cx', String(point.x)); dot.setAttribute('cy', String(point.y));
    cursor.setAttribute('visibility', 'visible');
    time.textContent = `${row.date} · ${timezone}`; time.dateTime = row.date;
    value.textContent = number.format(point.value); tooltip.hidden = false;
    const width = chart.getBoundingClientRect().width, target = point.x / 900 * width;
    const preferred = target > width / 2 ? target - tooltip.offsetWidth - 14 : target + 14;
    tooltip.style.left = `${Math.max(0, Math.min(width - tooltip.offsetWidth, preferred))}px`;
    if (announce) text('[data-chart-announcement]', `${row.date}，${names[key]} ${number.format(point.value)} 次`);
  };
  const renderLabels = () => {
    const slots = Math.min(rows.length, chart.getBoundingClientRect().width < 420 ? 3 : 5);
    labels.replaceChildren();
    for (let index = 0; index < slots; index++) {
      const row = rows[Math.round(index / Math.max(1, slots - 1) * (rows.length - 1))];
      if (!row) continue;
      const label = document.createElement('span');
      label.textContent = row.date.slice(5).replace('-', '/'); labels.append(label);
    }
  };
  const renderSeries = (nextKey: SeriesKey) => {
    key = nextKey; series = makeSeries(rows, key);
    panel.dataset.series = key; shell.dataset.empty = String(!series.peak);
    text('[data-series-label], [data-tooltip-label]', names[key]);
    text('[data-chart-total]', display(series.total)); text('[data-chart-average]', number.format(series.average));
    text('[data-chart-peak]', display(series.peak)); text('[data-chart-empty-title]', emptyTitles[key]);
    for (const [selector, count] of [['[data-chart-total]', series.total], ['[data-chart-peak]', series.peak]] as const) {
      const node = find(selector); if (node) node.title = number.format(count);
    }
    text('[data-chart-empty] p', key === 'views' ? '公开页面产生访问后，趋势会在这里呈现。后台账号的访问不计入统计。' : key === 'searches' ? '读者使用发现页搜索后，这里会显示每日搜索次数。' : '有搜索却没有找到结果时，这里会记录对应次数。');
    empty.hidden = Boolean(series.peak); drawing.replaceChildren();
    if (series.peak) {
      const points = series.points.map(point => `${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(' ');
      drawing.append(svgNode('polygon', { class: 'analytics-chart-area', points: `0,180 ${points} 900,180` }), svgNode('polyline', { class: 'analytics-chart-line', points }));
    }
    all('[data-chart-axis] span').forEach((node, index) => { const tick = series.ticks[index]; if (tick !== undefined) node.textContent = compact.format(tick); });
    all<HTMLButtonElement>('button[data-series]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.series === key)));
    chart.setAttribute('aria-label', `每日${names[key]}趋势，期间共 ${number.format(series.total)} 次。使用左右方向键查看每日数据。`);
    hidePoint(); renderLabels();
  };

  all('[data-format-number]').forEach(node => {
    const count = readCount(node.textContent?.trim());
    if (count !== null) { node.textContent = display(count); node.title = number.format(count); }
  });
  text('[data-average]', number.format(totals.views / Math.max(1, rows.length)));
  text('[data-article-share]', percent(totals.articles, totals.views));
  text('[data-found-count]', number.format(totals.searches - totals.misses));
  text('[data-miss-rate]', percent(totals.misses, totals.searches));
  text('[data-hit-rate]', percent(totals.searches - totals.misses, totals.searches));
  const hit = totals.searches ? (totals.searches - totals.misses) / totals.searches * 100 : 0;
  const hitBar = find('[data-hit-bar]'), missBar = find('[data-miss-bar]'), health = find('[data-search-health]');
  if (hitBar) hitBar.style.width = `${hit}%`;
  if (missBar) missBar.style.width = `${totals.searches ? 100 - hit : 0}%`;
  if (health) health.hidden = false;
  all<SVGSVGElement>('[data-sparkline]').forEach(svg => {
    if (!isSeries(svg.dataset.sparkline)) return;
    svg.replaceChildren();
    const spark = makeSeries(rows, svg.dataset.sparkline, 100, 3, 31);
    if (!spark.peak) { svg.setAttribute('hidden', ''); return; }
    const points = spark.points.map(point => `${point.x + 2},${point.y}`).join(' ');
    svg.append(svgNode('polygon', { points: `2,34 ${points} 102,34` }), svgNode('polyline', { points }));
    svg.removeAttribute('hidden');
  });
  const articleRing = find<SVGSVGElement>('[data-article-ring]');
  if (!totals.views) articleRing?.setAttribute('hidden', '');
  if (totals.views && articleRing) {
    const share = Math.min(100, totals.articles / totals.views * 100);
    articleRing.querySelector('.analytics-mini-fill')?.setAttribute('stroke-dasharray', `${share} ${100 - share}`);
    articleRing.removeAttribute('hidden');
  }
  const segments = find('[data-source-segments]'), remainder = find('[data-source-remainder]');
  segments?.replaceChildren(); if (remainder) remainder.hidden = true;
  const colors = [100, 56, 78, 34, 90, 45, 68, 25, 84, 39].map(weight => `color-mix(in srgb, var(--admin-brand) ${weight}%, var(--white))`);
  sourceBreakdown(report.sources, totals.views).forEach(part => {
    const color = part.index < 0 ? 'var(--admin-line)' : colors[part.index % colors.length] ?? 'var(--admin-brand)';
    if (part.count) segments?.append(svgNode('circle', { cx: 90, cy: 90, r: 72, pathLength: 100, stroke: color,
      'stroke-dasharray': `${part.share} ${100 - part.share}`, 'stroke-dashoffset': -part.offset }));
    const row = sourceRows[part.index];
    if (row) {
      row.style.setProperty('--source-color', color);
      const share = row.querySelector('[data-source-share]'); if (share) share.textContent = percent(part.count, totals.views);
    } else if (part.index < 0 && remainder) {
      remainder.textContent = `其余来源 ${number.format(part.count)} 次，占 ${percent(part.count, totals.views)}（环图浅灰部分）`; remainder.hidden = false;
    }
  });
  chart.tabIndex = 0; chart.setAttribute('aria-describedby', 'analytics-chart-help'); controls.hidden = false;
  controls.addEventListener('click', event => {
    if (!active || !(event.target instanceof Element)) return;
    const button = event.target.closest<HTMLButtonElement>('button[data-series]');
    if (button && isSeries(button.dataset.series)) {
      renderSeries(button.dataset.series);
      text('[data-chart-announcement]', `已切换为${names[key]}，期间 ${number.format(series.total)} 次`);
    }
  }, options);
  chart.addEventListener('pointermove', event => {
    const rect = chart.getBoundingClientRect(); showPoint(nearestIndex(event.clientX - rect.left, rect.width, rows.length));
  }, options);
  chart.addEventListener('pointerleave', () => { if (document.activeElement !== chart) hidePoint(); }, options);
  chart.addEventListener('focus', () => showPoint(activeIndex, true), options);
  chart.addEventListener('blur', hidePoint, options);
  chart.addEventListener('keydown', event => {
    if (!active) return;
    if (event.key === 'Escape') { hidePoint(); return; }
    const indexes: Record<string, number> = { ArrowLeft: Math.max(0, activeIndex - 1), ArrowRight: Math.min(rows.length - 1, activeIndex + 1), Home: 0, End: rows.length - 1 };
    const index = indexes[event.key]; if (index === undefined) return;
    event.preventDefault(); showPoint(index, true);
  }, options);
  renderSeries('views');
  const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => {
    if (!active || !workspace.isConnected) return;
    renderLabels(); if (!tooltip.hidden) showPoint(activeIndex);
  });
  const lifecycle: ChartLifecycle = {
    pause() { active = false; observer?.disconnect(); hidePoint(); },
    resume() {
      if (disposed || suspended || !workspace.isConnected || fallback.hidden || workspace.querySelector('[data-react-ready="true"]') || !workspace.contains(chart)) return;
      active = true; observer?.observe(chart); renderLabels();
    },
    dispose() { disposed = true; lifecycle.pause(); abort.abort(); },
  };
  lifecycle.resume(); return lifecycle;
}
function initialize() {
  for (const [host, lifecycle] of charts) if (!host.isConnected) { lifecycle.dispose(); charts.delete(host); }
  document.querySelectorAll<HTMLElement>('[data-analytics]').forEach(host => {
    if (charts.has(host) || host.querySelector('[data-react-ready="true"]')) return;
    const lifecycle = mount(host); if (lifecycle) charts.set(host, lifecycle);
  });
}
function eventHost(event: Event): HTMLElement | null {
  const detail: unknown = (event as CustomEvent<unknown>).detail;
  return detail && typeof detail === 'object' && 'host' in detail && detail.host instanceof HTMLElement ? detail.host : null;
}
initialize();
document.addEventListener('chunnel:admin-updated', initialize);
document.addEventListener('newpress:analytics-ready', event => { const host = eventHost(event); if (host) charts.get(host)?.pause(); });
document.addEventListener('newpress:analytics-fallback', event => { const host = eventHost(event); initialize(); if (host) charts.get(host)?.resume(); });
document.addEventListener('chunnel:admin-before-replace', event => {
  const regions: unknown = event.detail?.regions; if (!Array.isArray(regions)) return;
  for (const [host, lifecycle] of charts) if (regions.some(region => region instanceof Element && (region === host || region.contains(host)))) {
    lifecycle.dispose(); charts.delete(host);
  }
});
window.addEventListener('pagehide', () => { suspended = true; charts.forEach(chart => chart.pause()); });
window.addEventListener('pageshow', () => { suspended = false; initialize(); charts.forEach(chart => chart.resume()); });
