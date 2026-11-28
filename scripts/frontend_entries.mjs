// Only this manifest owns migrated browser entries. Builds never modify it.
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { build } from 'esbuild';
import { minify as compressJavaScript } from 'terser';

export const frontendRoot = fileURLToPath(new URL('../', import.meta.url));
export const frontendEntries = JSON.parse(await readFile(new URL('../frontend/entries.json', import.meta.url), 'utf8'));
export const frontendBanner = '/* Generated: npm run build:frontend */\n';
export const browserTargets = ['chrome100', 'firefox110', 'safari16'];
const names = new Set(), outputs = new Set();
for (const entry of frontendEntries) {
  if (names.has(entry.name)) throw new Error(`Duplicate frontend entry: ${entry.name}`);
  names.add(entry.name);
  for (const output of entry.outputs) {
    if (outputs.has(output.path)) throw new Error(`Duplicate frontend output: ${output.path}`);
    outputs.add(output.path);
  }
}

export async function compileFrontendEntry(name, { minify = true } = {}) {
  const entry = frontendEntries.find(item => item.name === name);
  if (!entry) throw new Error(`Unknown frontend entry: ${name}`);
  // The two immediate editor bundles benefit from Terser's whole-program
  // compression. Keep public properties and unsafe transforms untouched.
  const compressEditor = minify && (name === 'document-editor' || name === 'editor-page');
  const result = await build({
    absWorkingDir: frontendRoot,
    entryPoints: [resolve(frontendRoot, entry.source)],
    bundle: true, write: false, format: 'iife', platform: 'browser',
    target: browserTargets, minify: minify && !compressEditor, legalComments: 'none', charset: 'utf8',
    tsconfig: resolve(frontendRoot, 'tsconfig.frontend.json'),
    define: { 'process.env.NODE_ENV': '"production"' },
    // A timed-out optional download can still arrive after a retry. Guard this
    // entire entry so only one picker mounts; ordinary entries keep their startup.
    banner: name === 'editor-emoji-picker' ? { js: 'if (!window.ChunnelEmoji) {' } : undefined,
    footer: name === 'editor-emoji-picker' ? { js: '}' } : undefined,
    metafile: true,
  });
  if (result.outputFiles.length !== 1) throw new Error(`${name}: register CSS or additional outputs explicitly`);
  let code = result.outputFiles[0].text;
  if (compressEditor) {
    const compressed = await compressJavaScript(code, {
      ecma: 2022, compress: { passes: 3 }, mangle: true,
      format: { comments: false, ascii_only: false },
    });
    if (!compressed.code) throw new Error(`${name}: editor compression produced no code`);
    code = compressed.code + '\n';
  }
  return { code, inputs: Object.keys(result.metafile.inputs) };
}
