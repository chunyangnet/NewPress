// Only this manifest owns migrated browser entries. Builds never modify it.
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { build } from 'esbuild';

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
  const result = await build({
    absWorkingDir: frontendRoot,
    entryPoints: [resolve(frontendRoot, entry.source)],
    bundle: true, write: false, format: 'iife', platform: 'browser',
    target: browserTargets, minify, legalComments: 'none', charset: 'utf8',
    tsconfig: resolve(frontendRoot, 'tsconfig.frontend.json'),
    define: { 'process.env.NODE_ENV': '"production"' },
    metafile: true,
  });
  if (result.outputFiles.length !== 1) throw new Error(`${name}: register CSS or additional outputs explicitly`);
  return { code: result.outputFiles[0].text, inputs: Object.keys(result.metafile.inputs) };
}
