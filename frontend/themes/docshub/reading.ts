import { $, $$, announce, copyText } from './shared';
import { activeHeadingIndex, headingIDs, tocHeadingSelector } from './state';

export function initReading() {
  const article = $('[data-article-body]');
  if (!article) return;
  $('[data-copy-link]')?.addEventListener('click', async () => {
    try { await copyText(location.href); announce('链接已复制'); } catch { announce('复制失败，请从地址栏复制链接'); }
  });
  const reading = $('[data-reading-time]');
  if (reading) {
    const text = article.textContent || '', cjk = (text.match(/[\u3400-\u9fff]/g) || []).length;
    const words = text.replace(/[\u3400-\u9fff]/g, '').trim().split(/\s+/).filter(Boolean).length;
    reading.textContent = '约 ' + Math.max(1, Math.ceil(cjk / 350 + words / 220)) + ' 分钟阅读';
  }
  $$('pre', article).forEach(pre => {
    pre.tabIndex = 0; pre.setAttribute('aria-label', '代码示例，可横向滚动');
    if (article.dataset.codeCopy === 'false') return;
    const text = ($('code', pre) || pre).textContent, button = document.createElement('button');
    button.className = 'copy-code'; button.type = 'button'; button.textContent = '复制代码';
    let timer: ReturnType<typeof setTimeout> | undefined;
    button.addEventListener('click', async () => {
      try {
        await copyText(text); button.textContent = '已复制'; announce('代码已复制');
        clearTimeout(timer); timer = setTimeout(() => { button.textContent = '复制代码'; }, 2200);
      } catch { announce('复制失败，请选中代码手动复制'); }
    });
    pre.append(button);
  });
  $$('table', article).forEach(table => {
    const wrapper = document.createElement('div'); wrapper.className = 'table-scroll'; wrapper.tabIndex = 0;
    wrapper.setAttribute('role', 'region'); wrapper.setAttribute('aria-label', '表格，可横向滚动');
    table.before(wrapper); wrapper.append(table);
  });
  // Keep existing h2/h3 bookmarks stable when h4 entries are enabled later.
  const primaryHeadings = $$('h2,h3', article), reserved = $$('[id]').filter(node => !primaryHeadings.includes(node)).map(node => node.id);
  const ids = headingIDs(primaryHeadings.map(heading => heading.id), reserved);
  primaryHeadings.forEach((heading, index) => { heading.id = ids[index] || heading.id; heading.tabIndex = -1; });
  const details = $$('h4', article), detailReserved = $$('[id]').filter(node => !details.includes(node)).map(node => node.id);
  const detailIDs = headingIDs(details.map(heading => heading.id), detailReserved);
  details.forEach((heading, index) => { heading.id = detailIDs[index] || heading.id; heading.tabIndex = -1; });
  const allHeadings = $$('h2,h3,h4', article);
  const headings = $$(tocHeadingSelector(article.dataset.tocDepth), article), lists = $$('[data-toc-list],[data-mobile-toc-list]'), links: HTMLAnchorElement[][] = [];
  headings.forEach((heading, index) => {
    const title = heading.textContent.trim() || '章节 ' + (index + 1), group: HTMLAnchorElement[] = [];
    lists.forEach(list => {
      const li = document.createElement('li'), link = document.createElement('a');
      if (heading.tagName === 'H3') li.className = 'toc-sub';
      if (heading.tagName === 'H4') li.className = 'toc-detail';
      link.href = '#' + encodeURIComponent(heading.id); link.textContent = title;
      link.addEventListener('click', () => {
        const details = $<HTMLDetailsElement>('[data-mobile-toc]'); if (details) details.open = false;
        activate(index); heading.focus({ preventScroll: true });
      });
      li.append(link); list.append(li); group.push(link);
    });
    links.push(group);
  });
  if (headings.length) $$('[data-toc],[data-mobile-toc]').forEach(node => { node.hidden = false; });
  const aside = $('.article-aside');
  if (aside && ![...aside.children].some(node => node instanceof HTMLElement && !node.hidden)) {
    aside.hidden = true;
    $('.reading-layout')?.classList.add('reading-layout-no-aside');
  }
  function activate(index: number) {
    links.forEach((group, i) => group.forEach(link => {
      if (i === index) link.setAttribute('aria-current', 'location'); else link.removeAttribute('aria-current');
    }));
    const current = $('[data-current-section]'); if (current) current.textContent = headings[index]?.textContent || '';
  }
  const progress = $<HTMLProgressElement>('[data-docs-reading-progress]'); if (progress) progress.hidden = false;
  let frame: number | undefined;
  const track = () => {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      const rect = article.getBoundingClientRect();
      const padding = parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop);
      const offset = (Number.isFinite(padding) ? padding : 100)
        + (parseFloat(getComputedStyle(headings[0] || article).scrollMarginTop) || 0) + 4;
      const distance = Math.max(1, rect.height - window.innerHeight + offset), ratio = Math.min(1, Math.max(0, (offset - rect.top) / distance));
      if (progress) { progress.value = Math.round(ratio * 100); progress.setAttribute('aria-valuetext', '已阅读 ' + progress.value + '%'); }
      const atEnd = window.scrollY + window.innerHeight >= document.documentElement.scrollHeight - 2 && rect.top < offset;
      activate(activeHeadingIndex(headings.map(heading => heading.getBoundingClientRect().top), offset, atEnd));
    });
  };
  window.addEventListener('scroll', track, { passive: true }); window.addEventListener('resize', track);
  window.addEventListener('hashchange', track); window.addEventListener('pageshow', track);
  if (typeof ResizeObserver !== 'undefined') new ResizeObserver(track).observe(article);
  if (location.hash) {
    let id: string | undefined; try { id = decodeURIComponent(location.hash.slice(1)); } catch {}
    const target = allHeadings.find(heading => heading.id === id); if (target) requestAnimationFrame(() => target.scrollIntoView());
  }
  track();
}
