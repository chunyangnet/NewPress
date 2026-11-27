// Conventional, optional source build for independent themes. No Chunnel DOM
// or resource list is required. Runtime packages do not need Node.
import { readdir, readFile, writeFile, realpath } from 'node:fs/promises';
import { resolve, relative, sep } from 'node:path';
import { build, transform } from 'esbuild';

const args = process.argv.slice(2);
const index = args.indexOf('--source');
if (index < 0 || !args[index + 1]) throw new Error('Usage: npm run build:theme -- --source themes/starter [--check]');
const root = await realpath(resolve(args[index + 1]));
const check = args.includes('--check');
const manifest = JSON.parse(await readFile(resolve(root, 'theme.json'), 'utf8'));
let count = 0;
async function walk(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.isSymbolicLink()) throw new Error('Theme sources cannot contain symlinks');
    const file = resolve(directory, entry.name);
    if (entry.isDirectory()) { await walk(file); continue; }
    if (!/\.source\.(css|js)$/.test(file)) continue;
    const output = file.replace(/\.source\.(css|js)$/, '.$1');
    if (relative(root, output).split(sep).includes('..')) throw new Error('Output escapes theme root');
    const loader = file.endsWith('.css') ? 'css' : 'js';
    const source = await readFile(file, 'utf8');
    const options = { minify: true, target: ['chrome100', 'firefox110', 'safari16'], legalComments: 'none', charset: 'utf8' };
    const modular = /^\s*(?:import\s|@import\b)/m.test(source);
    const code = modular
      ? (await build({ ...options, entryPoints: [file], bundle: true, write: false, ...(loader === 'js' ? { format: 'iife' } : { external: ['*.woff2', '*.woff', '*.webp', '*.png', '*.svg'] }) })).outputFiles[0].text
      : (await transform(source, { ...options, loader })).code;
    const compiled = '/* Generated: npm run build:theme */\n' + code;
    if (check) {
      if (await readFile(output, 'utf8').catch(() => '') !== compiled) throw new Error(`${output} is stale`);
    } else await writeFile(output, compiled);
    count++;
  }
}
await walk(resolve(root, 'assets'));
console.log(`${manifest.slug}: ${count} generated assets ${check ? 'verified' : 'built'}`);
