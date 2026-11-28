// Embedded core UI builds without reading any installed theme.
import { readFile, writeFile, readdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build, transform } from 'esbuild';
import postcss from 'postcss';
import { motionCSS } from './motion.mjs';
import { buildIconSprite } from './build_icons.mjs';
import { frontendEntries, frontendRoot, compileFrontendEntry, frontendBanner } from './frontend_entries.mjs';
import { relative } from 'node:path';
import { buildFrontendLicenses } from './frontend_licenses.mjs';

const root = fileURLToPath(new URL('../internal/coreui/assets/', import.meta.url));
const check = process.argv.includes('--check');
await buildIconSprite(resolve(root, '../templates/partials/icons.html'), check);
const read = name => readFile(resolve(root, name), 'utf8');
const products = new Map();
const bundled = new Set(['css/chunnel-admin.css', ...['shared', 'directory', 'sessions', 'workflows'].map(name => `css/chunnel-admin-${name}.css`),
  'js/chunnel-component-library.js']);
const combined = new Set(['chunnel-admin-workspace', 'chunnel-admin-components', 'chunnel-home-editor', 'chunnel-foundation', 'chunnel-shell', 'chunnel-nav', 'newpress-component-view', 'newpress-components-chunnel']);
for (const folder of ['css', 'js']) {
  for (const file of await readdir(resolve(root, folder))) {
    if (!file.endsWith(`.source.${folder}`)) continue;
    const name = file.slice(0, -`.source.${folder}`.length);
    if (!combined.has(name)) products.set(`${folder}/${name}.${folder}`, await read(`${folder}/${file}`));
  }
}
const workspaceStyles = (await Promise.all(['admin-workspace', 'admin-components'].map(name => read(`css/chunnel-${name}.source.css`)))).join('\n');
products.set('css/chunnel-account.css', await read('css/chunnel-account.source.css') + '\n' + await read('css/referrals.source.css'));
products.set('css/chunnel-admin-workspace.css', workspaceStyles);
// Homepage module controls exist only in the appearance/layout workspaces.
// Keep their original cascade in a page bundle without another HTTP request.
products.set('css/chunnel-admin-layout-workspace.css', workspaceStyles + '\n' + await read('css/chunnel-home-editor.source.css'));
products.set('css/chunnel-foundation.css', await read('css/chunnel-foundation.source.css') + '\n' + motionCSS);
// Contiguous admin dependencies share compression without moving any rule
// across a conditional stylesheet. Login omits the authenticated UI layer.
products.set('css/chunnel-admin-controls.css', await read('css/chunnel-inputs.css') + '\n' + products.get('css/chunnel-foundation.css'));
const adminSkin = (await Promise.all(['chunnel-claude', 'chunnel-claude-admin'].map(async name =>
  products.get(`css/${name}.css`) ?? await read(`css/${name}.css`)))).join('\n');
products.set('css/chunnel-admin-login-skin.css', adminSkin);
products.set('css/chunnel-admin-skin.css', adminSkin + '\n' + products.get('css/chunnel-admin-ui.css'));
// The user directory needs invitation controls, without registration forms and
// public access-page layout. Keep the source cascade and responsive containers.
const invitations = postcss.parse(await read('css/chunnel-access.css'));
invitations.walkRules(rule => {
  const selectors = rule.selectors.filter(selector => /^(?:\.admin-invitations|\.invitation-row)(?![\w-])/.test(selector));
  if (selectors.length) rule.selectors = selectors;
  else rule.remove();
});
invitations.walkAtRules(rule => { if (rule.nodes && !rule.nodes.some(node => node.type !== 'comment')) rule.remove(); });
products.set('css/chunnel-admin-invitations.css', invitations.toString());
// These styles load together in the editor. Preserve their original cascade
// order and share one compression dictionary instead of five requests. Other
// screens keep the individual stylesheets.
products.set('css/chunnel-editor-content.css', (await Promise.all([
  'chunnel-editor-media.css', 'chunnel-composer.css',
  ...['newpress-article-widgets', 'chunnel-editor-widgets', 'newpress-article-badges', 'chunnel-article-badge-editor'].map(name => `${name}.source.css`),
].map(name => read(`css/${name}`)))).join('\n'));
products.set('css/chunnel-editor-components.css', (await Promise.all(['newpress-components', 'newpress-components-chunnel', 'chunnel-component-library'].map(name => read(`css/${name}.source.css`)))).join('\n'));
// Ship the scoped Chunnel skin with the portable components: public pages and
// editor previews share one stylesheet, without another request or runtime work.
products.set('css/newpress-components.css', (await Promise.all(['newpress-components', 'newpress-components-chunnel'].map(name => read(`css/${name}.source.css`)))).join('\n'));
// The picker and field enhancements are contiguous dependencies on the same
// pages. Keep their order while sharing compression; compile TS from its source.
products.set('js/chunnel-admin-media.js', (await Promise.all(['media-picker', 'media-fields']
  .map(async name => (await compileFrontendEntry(name, { minify: false })).code))).join('\n'));
for (const [file, source] of products) {
  const loader = file.endsWith('.css') ? 'css' : 'js';
  const options = { minify: true, target: ['chrome100', 'firefox110', 'safari16'], legalComments: 'none', charset: 'utf8' };
  const code = bundled.has(file)
    ? (await build({ entryPoints: [resolve(root, file.replace(/\.(css|js)$/, '.source.$1'))], bundle: true, write: false,
      external: ['*.woff', '*.woff2', '*.png', '*.svg', '*.webp'], ...options })).outputFiles[0].text
    : (await transform(source, { loader, ...options })).code;
  const value = '/* Generated: npm run build:frontend */\n' + code;
  if (check) {
    if (await read(file).catch(() => '') !== value) throw new Error(`core UI ${file} is stale; run npm run build:frontend`);
  } else await writeFile(resolve(root, file), value);
}
console.log(`Core UI: ${products.size} generated assets ${check ? 'verified' : 'built'}`);

const bundledInputs = new Set();
for (const entry of frontendEntries) {
  const outputs = entry.outputs.filter(output => output.scope === 'core');
  if (!outputs.length) continue;
  const compiled = await compileFrontendEntry(entry.name);
  compiled.inputs.forEach(input => bundledInputs.add(input));
  const value = frontendBanner + compiled.code;
  for (const output of outputs) {
    const file = relative(root, resolve(frontendRoot, output.path)).replaceAll('\\', '/');
    if (products.has(file)) throw new Error(`TS output still has a legacy source: ${file}`);
    if (check) {
      if (await read(file).catch(() => '') !== value) throw new Error(`core UI ${file} is stale; run npm run build:frontend`);
    } else await writeFile(resolve(root, file), value);
  }
}
await buildFrontendLicenses(bundledInputs, check);

// Compile only browser assets into Go. Editable sources and CSS fragments stay
// in the repository, without inflating or being exposed by the server binary.
const embedPaths = ['templates', 'assets/images', 'assets/fonts', 'defaults.json', 'pages.json'];
for (const folder of ['css', 'js']) {
  const entries = await readdir(resolve(root, folder));
  for (const name of entries.sort()) {
    if (name.endsWith(`.${folder}`) && !name.includes('.source.')) embedPaths.push(`assets/${folder}/${name}`);
  }
}
const embedSource = '// Code generated by scripts/build_core_ui.mjs; DO NOT EDIT.\npackage coreui\n\nimport "embed"\n\n'
  + embedPaths.map(name => `//go:embed ${name}`).join('\n') + '\nvar files embed.FS\n';
const embedFile = resolve(root, '../assets_embed.go');
if (check) {
  if (await readFile(embedFile, 'utf8').catch(() => '') !== embedSource) throw new Error('core UI embed manifest is stale; run npm run build:frontend');
} else await writeFile(embedFile, embedSource);
