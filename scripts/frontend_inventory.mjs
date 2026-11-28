// Inventory source/outputs without reading installed dependencies or local backups.
import { readdir, readFile, writeFile, mkdir } from 'node:fs/promises';
import { resolve, relative, basename } from 'node:path';
import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { frontendEntries, frontendRoot as root } from './frontend_entries.mjs';

async function files(folder) {
  const result = [];
  for (const entry of await readdir(resolve(root, folder), { withFileTypes: true }).catch(() => [])) {
    if (entry.isSymbolicLink() || ['node_modules', 'sources', '.git', 'data'].includes(entry.name)) continue;
    const name = `${folder}/${entry.name}`;
    if (entry.isDirectory()) result.push(...await files(name));
    else result.push(name);
  }
  return result.sort();
}
const browserFiles = [...await files('internal/coreui'), ...await files('themes'), ...await files('examples'), ...await files('frontend')];
const tests = await files('tests/frontend');
const buildFiles = (await files('scripts')).filter(name => /build.*\.mjs$/.test(name));
const references = await Promise.all([...browserFiles.filter(name => name.endsWith('.html')), ...tests, ...buildFiles]
  .map(async path => ({ path, text: await readFile(resolve(root, path), 'utf8') })));
const migrated = new Map(frontendEntries.flatMap(entry => entry.outputs.map(output => [output.path, entry])));
const sourceSet = new Set(browserFiles);
const assets = [];
for (const path of browserFiles.filter(name => /\.(?:mjs|js|ts|tsx)$/.test(name))) {
  const data = await readFile(resolve(root, path));
  const text = data.toString('utf8');
  const entry = migrated.get(path);
  const generated = !!entry || /Generated(?: by|:)/.test(text.slice(0, 250));
  const thirdParty = /(?:^|\/)(?:vendor|third_party)\//.test(path);
  const source = entry?.source ?? (generated && sourceSet.has(path.replace(/\.js$/, '.source.js')) ? path.replace(/\.js$/, '.source.js') : null);
  const name = basename(path);
  assets.push({ path, kind: thirdParty ? 'third-party' : generated ? 'generated' : path.endsWith('.d.ts') ? 'declarations' : 'source',
    source, bytes: data.length, gzip5: gzipSync(data, { level: 5 }).length,
    sha256: createHash('sha256').update(data).digest('hex'),
    globals: [...new Set([...text.matchAll(/\bwindow\.([A-Z][\w]*)/g)].map(match => match[1]))].sort(),
    references: references.filter(item => item.path !== path && item.text.includes(name)).map(item => item.path),
    stage: generated || thirdParty ? 'do not hand-edit' : /\.tsx?$/.test(path) ? 'strict TS' : /time|client/.test(name) ? 'shared foundations' : /editor|emoji|navigation|nav\./.test(name) ? 'high-coupling, migrate after foundations' : 'incremental business migration' });
}
const inline = references.filter(item => item.path.endsWith('.html')).flatMap(({ path, text }) =>
  [...text.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/g)].filter(match => !/\bsrc=|application\/(?:ld\+)?json/.test(match[1]) && match[2].trim())
    .map(match => ({ path, line: text.slice(0, match.index).split('\n').length, kind: 'inline browser source' })));
const report = { schema: 1, roots: ['internal/coreui', 'themes (excluding release sources)', 'examples', 'frontend'],
  note: 'References and window globals are lexical candidates for manual review; generated bundles are not counted as handwritten source. Node tools and installed/released plugin packages are outside browser migration scope.',
  entries: frontendEntries, assets, inline };
await mkdir(resolve(root, 'docs'), { recursive: true });
await writeFile(resolve(root, 'docs/FRONTEND_SOURCE_INVENTORY.json'), JSON.stringify(report, null, 2) + '\n');
const sources = assets.filter(asset => asset.kind === 'source');
await writeFile(resolve(root, 'docs/FRONTEND_SOURCE_INVENTORY.md'), `# 浏览器源码迁移清单\n\n由 \`node scripts/frontend_inventory.mjs\` 生成。源码 ${sources.length} 项，生成资源 ${assets.filter(a => a.kind === 'generated').length} 项；内联脚本 ${inline.length} 处。\n\n入口输出映射以 \`frontend/entries.json\` 为准。完整路径、引用、全局对象候选与摘要见同目录 JSON；词法引用需要结合页面回归确认。Node 构建工具、第三方代码及已发布插件包不做机械改写。\n\n| 可编辑源码 | 阶段 | 字节 | 引用候选数 |\n|---|---|---:|---:|\n` + sources.map(asset => `| \`${asset.path}\` | ${asset.stage} | ${asset.bytes} | ${asset.references.length} |`).join('\n') + '\n');
console.log(`Frontend inventory: ${sources.length} sources, ${assets.length - sources.length} outputs/declarations, ${inline.length} inline scripts`);
