import { useMemo } from 'react';
import { Chart } from '../charts/Chart';
import type { Palette } from '../charts/Chart';
import type { AnalyticsReport } from '../types';
import { createSiteTime } from '../../shared/site-time';

const numbers = new Intl.NumberFormat('zh-CN');
export const formatCount = (value: number) => numbers.format(value);
const axis = (palette: Palette) => ({ axisLine: { show: false }, axisTick: { show: false }, axisLabel: { color: palette.muted, fontSize: 11 } });

export function Metrics({ report }: { report: AnalyticsReport }) {
  const items = [
    { title: '页面浏览请求', value: report.views, note: `日均 ${formatCount(Math.round(report.views / report.days))} 次`, symbol: '01' },
    { title: '文章浏览请求', value: report.article_views, note: '文章正文页的浏览请求', symbol: '02' },
    { title: '主动搜索', value: report.searches, note: '发现页首屏搜索', symbol: '03' },
    { title: '无结果搜索', value: report.misses, note: report.searches ? `占主动搜索 ${(report.misses / report.searches * 100).toFixed(1)}%` : '本期尚无搜索请求', symbol: '04' },
  ];
  return <section className="analytics-metrics" aria-label="期间概览">
    {items.map(item => <article className="analytics-metric" key={item.title}>
      <header><h2>{item.title}</h2><span className="analytics-react-index" aria-hidden="true">{item.symbol}</span></header>
      <div className="analytics-metric-value"><strong data-metric={item.symbol}>{formatCount(item.value)}</strong><span>次</span></div>
      <footer>{item.note}</footer>
    </article>)}
  </section>;
}

export function Trend({ report }: { report: AnalyticsReport }) {
  const option = useMemo(() => (palette: Palette) => ({
    color: [palette.brand, palette.accent, palette.muted],
    grid: { left: 52, right: 22, top: 48, bottom: 35 },
    legend: { top: 4, left: 12, textStyle: { color: palette.text }, icon: 'roundRect' },
    tooltip: { trigger: 'axis', renderMode: 'richText', backgroundColor: palette.surface, textStyle: { color: palette.text }, borderColor: palette.line },
    xAxis: { type: 'category', data: report.trend.map(row => row.date), boundaryGap: false, ...axis(palette),
      axisLabel: { color: palette.muted, hideOverlap: true, formatter: (value: string) => value.slice(5).replace('-', '/') } },
    yAxis: { type: 'value', min: 0, minInterval: 1, ...axis(palette), splitLine: { lineStyle: { color: palette.line, type: 'dashed' } } },
    series: [
      { id: 'views', name: '页面浏览', key: 'views' as const, style: 'solid', symbol: 'circle' },
      { id: 'searches', name: '主动搜索', key: 'searches' as const, style: 'dashed', symbol: 'rect' },
      { id: 'misses', name: '无结果搜索', key: 'misses' as const, style: 'dotted', symbol: 'triangle' },
    ].map(item => ({ id: item.id, name: item.name, type: 'line', data: report.trend.map(row => row[item.key]),
      smooth: false, showSymbol: report.days <= 7, symbol: item.symbol, symbolSize: 6,
      lineStyle: { width: item.key === 'views' ? 2.5 : 2, type: item.style }, emphasis: { focus: 'series' } })),
  }), [report]);
  return <section className="analytics-panel analytics-trend" aria-labelledby="report-trend-title">
    <header className="analytics-panel-heading"><div><h2 id="report-trend-title">访问趋势</h2><p>{report.start} — {report.end} · {report.timezone}</p></div><span className="analytics-react-tag">{report.days} 天</span></header>
    <Chart option={option} label={`近 ${report.days} 天页面浏览、搜索和无结果搜索趋势；数值见下方每日明细。`} />
    {report.views === 0 && <p className="analytics-react-empty">本期还没有请求记录。采集后的真实计数会显示在这里。</p>}
    <details className="analytics-react-details">
      <summary>查看每日明细 · {report.days} 天</summary>
      <div className="analytics-react-table" role="region" aria-label="每日明细" tabIndex={0}>
        <table data-report-daily><caption className="sr-only">按 {report.timezone} 报告日历展示的原始日期标签</caption>
          <thead><tr><th scope="col">日期</th><th scope="col">页面浏览</th><th scope="col">主动搜索</th><th scope="col">无结果搜索</th></tr></thead>
          <tbody>{report.trend.map(row => <tr key={row.date} data-date={row.date}><th scope="row">{row.date}</th><td>{formatCount(row.views)}</td><td>{formatCount(row.searches)}</td><td>{formatCount(row.misses)}</td></tr>)}</tbody>
        </table>
      </div>
    </details>
  </section>;
}

export function Sources({ report }: { report: AnalyticsReport }) {
  const option = useMemo(() => (palette: Palette) => ({
    grid: { left: 6, right: 48, top: 10, bottom: 8, containLabel: true },
    tooltip: { trigger: 'axis', renderMode: 'richText', backgroundColor: palette.surface, textStyle: { color: palette.text }, borderColor: palette.line },
    xAxis: { type: 'value', show: false, min: 0, minInterval: 1 },
    yAxis: { type: 'category', inverse: true, data: report.sources.map(row => row.label), ...axis(palette), axisLabel: { color: palette.text, width: 120, overflow: 'truncate' } },
    series: [{ type: 'bar', data: report.sources.map(row => row.count), barMaxWidth: 14,
      itemStyle: { color: palette.brand, borderRadius: [0, 3, 3, 0] }, label: { show: true, position: 'right', color: palette.text } }],
  }), [report]);
  return <section className="analytics-panel" aria-labelledby="report-sources-title">
    <header className="analytics-panel-heading"><div><h2 id="report-sources-title">访问来源</h2><p>请求来源 · 前 10 项</p></div></header>
    {report.sources.length > 0 ? <>
      <Chart option={option} height={Math.max(150, report.sources.length * 31 + 20)} label="来源请求数；精确数值见来源列表。" />
      <details className="analytics-react-details"><summary>查看来源数值</summary>
        <ul className="analytics-react-ranks">{report.sources.map(row => <li key={row.label}><span>{row.label}</span><strong>{formatCount(row.count)} <small>次</small></strong></li>)}</ul>
      </details>
    </> : <p className="analytics-react-empty">本期暂无来源记录。</p>}
    <p className="analytics-caption">来源按请求统计，并非会话首次来源。有限榜单之和可能小于总浏览量。</p>
  </section>;
}

export function Articles({ report }: { report: AnalyticsReport }) {
  return <section className="analytics-panel" aria-labelledby="report-articles-title">
    <header className="analytics-panel-heading"><div><h2 id="report-articles-title">热门内容</h2><p>期间浏览排行 · 互动为当前总数</p></div><span className="analytics-react-tag">前 20 篇</span></header>
    {report.articles.length ? <div className="analytics-react-table" role="region" aria-label="热门内容" tabIndex={0}>
      <table><thead><tr><th scope="col">文章</th><th scope="col">期间浏览</th><th scope="col">点赞总数</th><th scope="col">收藏总数</th><th scope="col">评论总数</th></tr></thead>
        <tbody>{report.articles.map(row => <tr key={row.id}><th scope="row"><a href={row.url}>{row.title}</a></th><td>{formatCount(row.views)}</td><td>{formatCount(row.likes)}</td><td>{formatCount(row.bookmarks)}</td><td>{formatCount(row.comments)}</td></tr>)}</tbody>
      </table>
    </div> : <p className="analytics-react-empty">本期暂无当前公开文章的浏览记录。</p>}
  </section>;
}

export function SearchTerms({ report }: { report: AnalyticsReport }) {
  return <section className="analytics-panel" aria-labelledby="report-search-title">
    <header className="analytics-panel-heading"><div><h2 id="report-search-title">搜索洞察</h2><p>读者还没有找到的内容</p></div><span className="analytics-react-tag">前 20 项</span></header>
    {report.terms.length ? <ol className="analytics-react-ranks">{report.terms.map(row => <li key={row.label}><span>{row.label}</span><strong>{formatCount(row.count)} <small>次</small></strong></li>)}</ol>
      : <p className="analytics-react-empty">本期没有可展示的无结果搜索词。</p>}
    <p className="analytics-caption">疑似邮箱、链接和长数字会隐藏词语，次数仍保留。</p>
  </section>;
}

export function ReportNotes({ report }: { report: AnalyticsReport }) {
  const time = useMemo(() => createSiteTime(() => report.timezone), [report.timezone]);
  return <>
    <p className="analytics-react-updated">最近保存：{time.formatDateTimeSeconds(report.updated_at) || '尚未保存数据'} · {report.timezone}</p>
    <details className="analytics-method"><summary>统计口径与数据状态</summary>
      <div><p>浏览指标是请求计数，刷新可能再次计数，不代表独立访客。来源按请求的 Referer 归类；文章互动是当前总数。</p>
        <p>报告沿用服务端日期标签。站点时区变更不会重算历史日桶。本进程启动后已舍弃 {formatCount(report.dropped_since_start)} 条请求，该数值不是跨实例的全站损失量。</p>
        <p>不保存访客标识、IP、浏览器信息或完整来源地址。排除后台账号、常见机器人、预取及 DNT / GPC 请求；过滤不能证明请求来自真人。</p></div>
    </details>
  </>;
}
