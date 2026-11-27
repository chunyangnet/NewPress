/* Progressive charts over the server-rendered report. No extra requests,
   tracking, synthetic history, or dependency on a charting library. */
(() => {
  'use strict';

  const number = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 });
  const compact = new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 });
  const count = (value) => Math.max(0, Number.isFinite(Number(value)) ? Number(value) : 0);
  const display = (value) => value >= 1000000 ? compact.format(value) : number.format(value);
  const percent = (value, total) => {
    if (!(total > 0)) return '—';
    const ratio = Math.min(100, count(value) / total * 100);
    return ratio > 0 && ratio < 0.1 ? '<0.1%' : number.format(ratio) + '%';
  };

  function makeSeries(rows, key, width = 900, top = 20, bottom = 180) {
    const values = rows.map((row) => count(row[key]));
    const total = values.reduce((sum, value) => sum + value, 0);
    const peak = Math.max(0, ...values);
    // Whole-number ticks, including for small counts and all-zero periods.
    const magnitude = Math.max(1, 10 ** Math.floor(Math.log10(Math.max(1, peak / 4))));
    const step = [1, 2, 3, 4, 5, 6, 8, 10].find((unit) => unit * magnitude >= peak / 4) * magnitude;
    const ceiling = Math.max(4, step * 4);
    const points = values.map((value, index) => ({
      x: values.length === 1 ? width / 2 : index * width / Math.max(1, values.length - 1),
      y: bottom - value / ceiling * (bottom - top),
      value,
    }));
    return { total, peak, average: rows.length ? total / rows.length : 0, ceiling, points,
      ticks: Array.from({ length: 5 }, (_, index) => ceiling - index * ceiling / 4) };
  }

  function sourceBreakdown(sources, total) {
    if (!(total > 0)) return [];
    let used = 0;
    const parts = sources.map((source, index) => {
      const value = Math.min(count(source.count), Math.max(0, total - used));
      const part = { label: source.label, count: value, share: value / total * 100, offset: used / total * 100, index };
      used += value;
      return part;
    });
    // The API returns only the top 10. Keep the omitted share in the ring
    // instead of inflating those 10 sources to 100% of all traffic.
    if (used < total) parts.push({ label: '其余来源', count: total - used, share: (total - used) / total * 100, offset: used / total * 100, index: -1 });
    return parts;
  }

  function nearestIndex(x, width, length) {
    if (width <= 0 || length < 2) return 0;
    return Math.max(0, Math.min(length - 1, Math.round(x / width * (length - 1))));
  }

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { makeSeries, sourceBreakdown, nearestIndex, percent };
  }
  if (typeof document === 'undefined') return;
  const charts = new Map();
  function initialize() {
  const workspace = document.querySelector('[data-analytics]');
  if (workspace?.querySelector('[data-react-ready="true"]')) return;
  if (!workspace || charts.has(workspace)) return;
  charts.set(workspace, null);

  const find = (selector) => workspace.querySelector(selector);
  const all = (selector) => [...workspace.querySelectorAll(selector)];
  const setText = (selector, value) => all(selector).forEach((node) => { node.textContent = value; });
  const svgNode = (tag, attributes = {}) => {
    const node = document.createElementNS('http://www.w3.org/2000/svg', tag);
    Object.entries(attributes).forEach(([name, value]) => node.setAttribute(name, String(value)));
    return node;
  };
  const rows = all('[data-analytics-daily] tbody tr').map((row) => ({
    date: row.dataset.date, views: count(row.dataset.views),
    searches: count(row.dataset.searches), misses: count(row.dataset.misses),
  }));
  const totals = {
    views: count(workspace.dataset.views), articles: count(workspace.dataset.articleViews),
    searches: count(workspace.dataset.searches), misses: count(workspace.dataset.misses),
  };

  all('[data-format-number]').forEach((node) => {
    const value = count(node.textContent.trim());
    node.textContent = display(value);
    node.title = number.format(value);
  });
  setText('[data-average]', number.format(totals.views / Math.max(1, rows.length)));
  setText('[data-article-share]', percent(totals.articles, totals.views));
  setText('[data-found-count]', number.format(Math.max(0, totals.searches - totals.misses)));
  setText('[data-miss-rate]', percent(totals.misses, totals.searches));
  setText('[data-hit-rate]', percent(Math.max(0, totals.searches - totals.misses), totals.searches));
  const hit = totals.searches ? Math.max(0, totals.searches - totals.misses) / totals.searches * 100 : 0;
  find('[data-hit-bar]').style.width = hit + '%';
  find('[data-miss-bar]').style.width = (totals.searches ? 100 - hit : 0) + '%';
  find('[data-search-health]').hidden = false;

  all('[data-sparkline]').forEach((svg) => {
    const series = makeSeries(rows, svg.dataset.sparkline, 100, 3, 31);
    if (!series.peak) return;
    const points = series.points.map((point) => `${point.x + 2},${point.y}`).join(' ');
    svg.append(svgNode('polygon', { points: `2,34 ${points} 102,34` }), svgNode('polyline', { points }));
    svg.removeAttribute('hidden');
  });
  const articleRing = find('[data-article-ring]');
  if (totals.views) {
    const share = Math.min(100, totals.articles / totals.views * 100);
    articleRing.querySelector('.analytics-mini-fill').setAttribute('stroke-dasharray', `${share} ${100 - share}`);
    articleRing.removeAttribute('hidden');
  }

  const sourceRows = all('[data-source-count]');
  const parts = sourceBreakdown(sourceRows.map((row) => ({ label: row.dataset.sourceLabel, count: count(row.dataset.sourceCount) })), totals.views);
  const sourceColors = [100, 56, 78, 34, 90, 45, 68, 25, 84, 39].map(weight =>
    `color-mix(in srgb, var(--admin-brand) ${weight}%, var(--white))`);
  parts.forEach((part) => {
    const color = part.index < 0 ? 'var(--admin-line)' : sourceColors[part.index % sourceColors.length];
    if (part.count) find('[data-source-segments]').append(svgNode('circle', {
      cx: 90, cy: 90, r: 72, pathLength: 100, stroke: color,
      'stroke-dasharray': `${part.share} ${100 - part.share}`, 'stroke-dashoffset': -part.offset,
    }));
    const row = sourceRows[part.index];
    if (row) {
      row.style.setProperty('--source-color', color);
      row.querySelector('[data-source-share]').textContent = percent(part.count, totals.views);
    } else if (part.index < 0) {
      const remainder = find('[data-source-remainder]');
      remainder.textContent = `其余来源 ${number.format(part.count)} 次，占 ${percent(part.count, totals.views)}（环图浅灰部分）`;
      remainder.hidden = false;
    }
  });

  const chart = find('[data-chart]');
  const chartPanel = chart.closest('.analytics-trend');
  const shell = find('[data-chart-shell]');
  const cursor = find('[data-chart-cursor]');
  const tooltip = find('[data-chart-tooltip]');
  const seriesNames = { views: '页面浏览', searches: '主动搜索', misses: '无结果搜索' };
  const emptyTitles = { views: '还没有访问记录', searches: '本期还没有搜索记录', misses: '本期没有无结果搜索' };
  let key = 'views';
  let series;
  let activeIndex = Math.max(0, rows.length - 1);

  function hidePoint() {
    cursor.setAttribute('visibility', 'hidden');
    tooltip.hidden = true;
  }

  function showPoint(index, announce = false) {
    if (!rows.length || !series.points[index]) return;
    activeIndex = index;
    const point = series.points[index];
    cursor.querySelector('line').setAttribute('x1', point.x);
    cursor.querySelector('line').setAttribute('x2', point.x);
    cursor.querySelector('circle').setAttribute('cx', point.x);
    cursor.querySelector('circle').setAttribute('cy', point.y);
    cursor.setAttribute('visibility', 'visible');
    tooltip.querySelector('time').textContent = rows[index].date + ' · ' + (workspace.dataset.timezone || window.ChunnelTime?.zone() || 'UTC');
    tooltip.querySelector('time').dateTime = rows[index].date;
    tooltip.querySelector('b').textContent = number.format(point.value);
    tooltip.hidden = false;
    const width = chart.getBoundingClientRect().width;
    const target = point.x / 900 * width;
    const preferred = target > width / 2 ? target - tooltip.offsetWidth - 14 : target + 14;
    tooltip.style.left = Math.max(0, Math.min(width - tooltip.offsetWidth, preferred)) + 'px';
    if (announce) setText('[data-chart-announcement]', `${rows[index].date}，${seriesNames[key]} ${number.format(point.value)} 次`);
  }

  function renderLabels() {
    const labels = find('[data-chart-labels]');
    const width = chart.getBoundingClientRect().width;
    const slots = Math.min(rows.length, width < 420 ? 3 : 5);
    labels.replaceChildren();
    for (let index = 0; index < slots; index++) {
      const row = rows[Math.round(index / Math.max(1, slots - 1) * (rows.length - 1))];
      const label = document.createElement('span');
      label.textContent = row.date.slice(5).replace('-', '/');
      labels.append(label);
    }
  }

  function renderSeries(nextKey) {
    if (!Object.hasOwn(seriesNames, nextKey)) return;
    key = nextKey;
    series = makeSeries(rows, key);
    chartPanel.dataset.series = key;
    shell.dataset.empty = String(!series.peak);
    setText('[data-series-label], [data-tooltip-label]', seriesNames[key]);
    setText('[data-chart-total]', display(series.total));
    find('[data-chart-total]').title = number.format(series.total);
    setText('[data-chart-average]', number.format(series.average));
    setText('[data-chart-peak]', display(series.peak));
    find('[data-chart-peak]').title = number.format(series.peak);
    setText('[data-chart-empty-title]', emptyTitles[key]);
    const emptyCopy = find('[data-chart-empty] p');
    emptyCopy.textContent = key === 'views' ? '公开页面产生访问后，趋势会在这里呈现。后台账号的访问不计入统计。' : key === 'searches' ? '读者使用发现页搜索后，这里会显示每日搜索次数。' : '有搜索却没有找到结果时，这里会记录对应次数。';
    find('[data-chart-empty]').hidden = Boolean(series.peak);
    const drawing = find('[data-chart-series]');
    drawing.replaceChildren();
    if (series.peak) {
      const points = series.points.map((point) => `${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(' ');
      drawing.append(svgNode('polygon', { class: 'analytics-chart-area', points: `0,180 ${points} 900,180` }), svgNode('polyline', { class: 'analytics-chart-line', points }));
    }
    all('[data-chart-axis] span').forEach((node, index) => { node.textContent = compact.format(series.ticks[index]); });
    all('[data-series]').filter((node) => node.tagName === 'BUTTON').forEach((button) => {
      button.setAttribute('aria-pressed', String(button.dataset.series === key));
    });
    chart.setAttribute('aria-label', `每日${seriesNames[key]}趋势，期间共 ${number.format(series.total)} 次。使用左右方向键查看每日数据。`);
    hidePoint();
    renderLabels();
  }

  chart.tabIndex = 0;
  chart.setAttribute('aria-describedby', 'analytics-chart-help');
  find('[data-chart-switch]').hidden = false;
  find('[data-chart-switch]').addEventListener('click', (event) => {
    const button = event.target.closest('button[data-series]');
    if (button) {
      renderSeries(button.dataset.series);
      setText('[data-chart-announcement]', `已切换为${seriesNames[key]}，期间 ${number.format(series.total)} 次`);
    }
  });
  chart.addEventListener('pointermove', (event) => {
    const rect = chart.getBoundingClientRect();
    showPoint(nearestIndex(event.clientX - rect.left, rect.width, rows.length));
  });
  chart.addEventListener('pointerleave', () => {
    if (document.activeElement !== chart) hidePoint();
  });
  chart.addEventListener('focus', () => showPoint(activeIndex, true));
  chart.addEventListener('blur', hidePoint);
  chart.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') { hidePoint(); return; }
    const index = { ArrowLeft: Math.max(0, activeIndex - 1), ArrowRight: Math.min(rows.length - 1, activeIndex + 1), Home: 0, End: rows.length - 1 }[event.key];
    if (index === undefined) return;
    event.preventDefault();
    showPoint(index, true);
  });
  renderSeries('views');
  if (typeof ResizeObserver !== 'undefined') {
    const observer = new ResizeObserver(() => { renderLabels(); if (!tooltip.hidden) showPoint(activeIndex); });
    observer.observe(chart);
    charts.set(workspace, observer);
  }
  }
  initialize();
  document.addEventListener('chunnel:admin-updated', initialize);
  document.addEventListener('newpress:analytics-ready', event => {
    const host = event.detail?.host;
    charts.get(host)?.disconnect();
  });
  document.addEventListener('newpress:analytics-fallback', event => {
    const host = event.detail?.host;
    const chart = host?.querySelector('[data-chart]');
    if (chart) charts.get(host)?.observe(chart);
  });
  document.addEventListener('chunnel:admin-before-replace', event => {
    for (const [workspace, observer] of charts) if (event.detail.regions.some(region => region === workspace || region.contains(workspace))) { observer?.disconnect(); charts.delete(workspace); }
  });
})();
