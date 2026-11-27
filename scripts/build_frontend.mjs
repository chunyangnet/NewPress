// Generated assets are committed. Node and these dev dependencies are not needed to serve.
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';
import { transform } from 'esbuild';
import { minify as compressJavaScript } from 'terser';
import { PurgeCSS } from 'purgecss';
import { motionCSS } from './motion.mjs';
import { splitHomeOverlays } from './home_resources.mjs';
import { buildIconSprite } from './build_icons.mjs';
import './build_core_ui.mjs';
import { frontendEntries, compileFrontendEntry, browserTargets } from './frontend_entries.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
const theme = resolve(root, 'themes/chunnel');
const read = (path) => readFile(resolve(theme, path), 'utf8');
const check = process.argv.includes('--check');
await buildIconSprite(resolve(theme, 'templates/partials/icons.html'), check);
const banner = '/* Generated: npm run build:frontend */\n';
const products = new Map();
const minify = async (source, loader) => banner + (await transform(source, {
  loader, minify: true, target: ['chrome100', 'firefox110', 'safari16'], legalComments: 'none', charset: 'utf8',
})).code;
// Composite resources compile their shared entries from the same TS sources.
const sharedScriptSources = new Map();
products.set('assets/css/chunnel-color-mode.css', await minify(await read('assets/css/chunnel-color-mode.source.css'), 'css'));
const colorMode = (await compileFrontendEntry('color-mode', { minify: false })).code;
products.set('assets/css/chunnel-navigation-badges.css', await minify(await read('assets/css/chunnel-navigation-badges.source.css'), 'css'));
const articleBadges = await readFile(resolve(root, 'internal/coreui/assets/css/newpress-article-badges.source.css'), 'utf8');
products.set('assets/css/chunnel-article-badges.css', await minify(articleBadges, 'css'));
for (const entry of ['chunnel-billing', 'cookie-consent']) {
  products.set(`assets/css/${entry}.css`, await minify(await read(`assets/css/${entry}.source.css`), 'css'));
}
products.set('assets/css/chunnel-home-sidebar.css', await minify(await read('assets/css/chunnel-home-sidebar.source.css'), 'css'));
products.set('assets/css/chunnel-home-blocks.css', await minify(await read('assets/css/chunnel-home-blocks.source.css'), 'css'));


products.set('assets/css/chunnel-dialogs.css', await minify(await read('assets/css/chunnel-dialogs.source.css'), 'css'));
products.set('assets/css/chunnel-footer.css', await minify(await read('assets/css/chunnel-footer.source.css'), 'css'));
products.set('assets/css/chunnel-account.css', await minify(await read('assets/css/chunnel-account.source.css') + '\n' + await readFile(resolve(root, 'internal/coreui/assets/css/referrals.source.css'), 'utf8'), 'css'));
products.set('assets/css/chunnel-portal.css', await minify(await read('assets/css/chunnel-portal.source.css'), 'css'));
products.set('assets/css/chunnel-circles.css', await minify(await read('assets/css/chunnel-circles.source.css'), 'css'));
for (const name of ['chunnel-channels', 'chunnel-announcements', 'chunnel-status']) {
  products.set(`assets/css/${name}.css`, await minify(await read(`assets/css/${name}.source.css`), 'css'));
}
products.set('assets/css/chunnel-explore.css', await minify(await read('assets/css/chunnel-explore.source.css'), 'css'));
for (const name of ['chunnel-verification']) {
  products.set(`assets/css/${name}.css`, await minify(await read(`assets/css/${name}.source.css`), 'css'));
}
// Shared TS entries keep the core and theme URLs on one source of truth.
for (const entry of frontendEntries) {
  for (const output of entry.outputs.filter(item => item.scope === 'chunnel')) {
    if (!output.path.startsWith('themes/chunnel/')) throw new Error(`Invalid Chunnel output: ${output.path}`);
    products.set(output.path.slice('themes/chunnel/'.length), banner + (await compileFrontendEntry(entry.name)).code);
  }
}
// Compile against the complete route contract, never a sampled live DOM or coverage run.
// All conditionally rendered home sections, navigation variants and hidden dialogs are inputs.
// Dynamic families (settings, states, icons, saved items and toasts) are explicitly retained.
const contentFiles = [
  'templates/base.html', 'templates/home.html',
  ...['header', 'footer', 'navigation', 'interaction-hub', 'oauth-logos', 'icons', 'color-mode', 'home-modules', 'home-creative', 'content-pagination'].map((name) => `templates/partials/${name}.html`),
  ...['chunnel-emoji', 'chunnel-emoji-editor'].map((name) => `assets/js/${name}.js`),
];
const content = await Promise.all(contentFiles.map(async (path) => ({ raw: await read(path), extension: path.split('.').at(-1) })));
for (const entry of ['shell', 'mobile-nav', 'search', 'interactions', 'interaction-notices']) {
  content.push({ raw: await readFile(resolve(root, `frontend/shared/${entry}.ts`), 'utf8'), extension: 'ts' });
}
for (const name of ['home', 'home-features-loader', 'home-search-loader', 'home-session', 'home-feed-loader']) {
  content.push({ raw: await readFile(resolve(root, `frontend/themes/chunnel/${name}.ts`), 'utf8'), extension: 'ts' });
}
// With JavaScript disabled, controls created only by scripts never exist.
const [nativeHome] = await new PurgeCSS().purge({ content: content.filter(item => item.extension === 'html'), css: [{ raw: await read('assets/css/chunnel-nojs.css') }], blocklist: [/^admin-/] });
products.set('assets/css/chunnel-home-nojs.css', await minify(nativeHome.css, 'css'));
// Home owns its cards and pagination. The other route stylesheet would also
// retain discussion styles mentioned by shared emoji code but never rendered here.
const cssFiles = ['chunnel', 'chunnel-ui', 'chunnel-settings', 'chunnel-discovery', 'chunnel-interactions', 'chunnel-home-status'];
const common = (await Promise.all(cssFiles.map((name) => read(`assets/css/${name}.css`)))).join('\n');
// Home renders no selects. Its ignored input rules only need the native input
// exclusions; the choice marker is retained by the complete route stylesheet.
const homeInputs = (await read('assets/css/chunnel-inputs.css')).replaceAll(', [data-choice]', '');
const foundation = await read('assets/css/chunnel-foundation.source.css') + '\n' + motionCSS;
const homePortal = await read('assets/css/chunnel-home-portal.source.css');
const homePurge = {
  content,
  // Provider IDs are supplied by backend settings, so their color attributes
  // never appear as literal values in the Go template.
  dynamicAttributes: ['data-provider'],
  safelist: { standard: ['modal-open', /^home-/, /^theme-/, /^nav-/, /^is-(?!admin(?:-|$))/, /^has-/, /^icon-/, /^toast(?:$|-(?!demo))/, /^avatar-/, /^emoji-/, /^message-/, /^notice-/, /^notification-/, /^interaction-/, /^saved-/] },
  // Shared scripts mention these other routes, but home never renders their components.
  // Home has no native selects or draft-preview bar. The generic <article>
  // element is still required by feed cards and conversations.
  blocklist: [/^(article-|prose(?:-|$)|component-|admin-|is-admin(?:-|$)|toast-demo-|comments?(?:-|$)|content-preview-)/, /^select$/,
    // The modular home replaces these legacy portal components. Other routes
    // still receive their original styles from the shared route stylesheets.
    /^(portal-|feature-|collection-|quick-categor|quick-icon|quick-blue|quick-violet|quick-cyan|quick-orange)/,
    /^feed-(row|thumb|copy|list|tags|footer|panel)$/, /^(welcome|ranking|notice|creator)-widget$/,
    /^circle-(grid|card|banner|body)$/, /^(pagination|discussion-card|is-taxonomy)$/],
  // Animation names are static CSS; appearance settings change duration, not names.
  // Keep only referenced keyframes, while preserving selectable fonts and custom properties.
  fontFace: false, keyframes: true, variables: false,
};
// The Claude skin is appended last on every route. Purging it for home keeps
// only the selectors the homepage actually renders while preserving cascade order.
const community = await read('assets/css/chunnel-community.source.css');
const [homeSkin] = await new PurgeCSS().purge({ ...homePurge, css: [{ raw: community }] });
products.set('assets/css/chunnel-community.css', await minify(community, 'css'));
products.set('assets/css/chunnel-foundation.css', await minify(foundation, 'css'));
for (const glass of [false, true]) {
  const source = common + (glass ? '\n' + await read('assets/css/chunnel-glass.css') : '') + '\n' + homeInputs + '\n' + foundation;
  const [result] = await new PurgeCSS().purge({ ...homePurge, css: [{ raw: source }] });
  // Input state rules carry ignore annotations: nested :not() exclusions and
  // browser validation cannot be inferred from template text.
  // All portal rules belong to this route, including configurable/device states.
  // Append after pruning legacy pagination so shared drawers keep their styling.
  const { initial, overlays } = splitHomeOverlays(result.css + '\n' + homePortal + '\n' + homeSkin.css);
  products.set(`assets/css/chunnel-home${glass ? '-glass' : ''}.css`, await minify(initial, 'css'));
  products.set(`assets/css/chunnel-home-overlays${glass ? '-glass' : ''}.css`, await minify(overlays, 'css'));
}
const shell = (await compileFrontendEntry('shell', { minify: false })).code;
const scroll = (await compileFrontendEntry('scroll', { minify: false })).code;
const navigation = (await compileFrontendEntry('mobile-nav', { minify: false })).code;
const home = (await compileFrontendEntry('home-controller', { minify: false })).code;
const time = (await compileFrontendEntry('site-time', { minify: false })).code;


for (const glass of [false, true]) {
  const shared = shell + (glass ? '\n' + navigation : '');
  const suffix = glass ? '-glass' : '';
  // Resolve appearance before first paint; bind homepage controls once the DOM exists.
  // Sharing one compressed entry keeps glass + appearance within the existing budget.
  const compiled = (await transform(colorMode + '\ndocument.addEventListener("DOMContentLoaded", () => {\n' + scroll + '\n' + shared + '\n' + home + '\n}, { once: true });', {
    loader: 'js', target: browserTargets, legalComments: 'none', charset: 'utf8',
  })).code;
  // The homepage has a strict 7 KiB gzip budget. Compress the combined program
  // once after esbuild's browser-target lowering; property names and unsafe transforms
  // stay disabled so DOM and public API contracts retain their original meaning.
  const compressed = await compressJavaScript(compiled, {
    ecma: 2022, compress: { passes: 1 }, mangle: true,
    format: { comments: false, ascii_only: false },
  });
  if (!compressed.code) throw new Error(`Homepage compression produced no code: ${suffix || 'classic'}`);
  products.set(`assets/js/chunnel-home${suffix}.js`, banner + compressed.code + '\n');
}
// Shared picker scripts come from the TS manifest; the two CSS copies still
// share the existing source-equality check until their separate consolidation.
for (const [asset, extension] of [['chunnel-selects.css', 'css'], ['chunnel-choice-menu.css', 'css']]) {
  const sharedSource = await readFile(resolve(root, `internal/coreui/assets/${extension}/${asset.replace(`.${extension}`, `.source.${extension}`)}`), 'utf8');
  if (await read(`assets/${extension}/${asset.replace(`.${extension}`, `.source.${extension}`)}`) !== sharedSource) {
    throw new Error(`${asset} theme and embedded sources differ`);
  }
  products.set(`assets/${extension}/${asset}`, await minify(sharedSource, extension));
}
const client = (await compileFrontendEntry('newpress-client', { minify: false })).code;
sharedScriptSources.set('chunnel-motion', (await compileFrontendEntry('motion', { minify: false })).code);
sharedScriptSources.set('chunnel-engagement', (await compileFrontendEntry('engagement', { minify: false })).code);
sharedScriptSources.set('chunnel-interactions', (await compileFrontendEntry('interactions', { minify: false })).code);
sharedScriptSources.set('chunnel-experience', (await compileFrontendEntry('experience', { minify: false })).code);
sharedScriptSources.set('chunnel-currency-shared', (await compileFrontendEntry('currency-shared', { minify: false })).code);
sharedScriptSources.set('chunnel-home-widgets', (await compileFrontendEntry('home-widgets', { minify: false })).code);
const features = (await Promise.all(['chunnel-motion', 'chunnel', 'chunnel-emoji-editor', 'chunnel-emoji', 'chunnel-interactions', 'chunnel-engagement', 'chunnel-experience', 'chunnel-currency-shared', 'chunnel-home-widgets'].map((name) => sharedScriptSources.get(name) ?? read(`assets/js/${name}.js`)))).join('\n');
products.set('assets/js/chunnel-home-features.js', await minify(`if (!window.ChunnelHomeFeaturesReady) { window.ChunnelHome?.stop();\n${client}\n${time}\n${features}\nwindow.ChunnelHomeFeaturesReady = true; }`, 'js'));

for (const [path, value] of products) {
  const gzip = gzipSync(value, { level: 5 }).length;
  console.log(`${path}: ${Buffer.byteLength(value)} B / gzip ${gzip} B`);
  if (check) {
    if (await read(path).catch(() => '') !== value) throw new Error(`${path} is stale; run npm run build:frontend`);
  } else await writeFile(resolve(theme, path), value);
}
