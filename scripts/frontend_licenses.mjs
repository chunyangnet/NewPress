import { readFile, writeFile, readdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { frontendRoot } from './frontend_entries.mjs';

export async function buildFrontendLicenses(inputs, check) {
  const packages = new Set();
  for (const input of inputs) {
    const parts = input.replaceAll('\\', '/').split('/');
    const index = parts.lastIndexOf('node_modules');
    if (index >= 0) packages.add(parts.slice(0, index + (parts[index + 1].startsWith('@') ? 3 : 2)).join('/'));
  }
  let result = 'NewPress browser bundle third-party notices\nGenerated from the exact bundled npm packages by scripts/frontend_licenses.mjs.\n';
  for (const folder of [...packages].sort()) {
    const path = resolve(frontendRoot, folder);
    const info = JSON.parse(await readFile(resolve(path, 'package.json'), 'utf8'));
    const licenseNames = (await readdir(path)).filter(name => /^(?:licen[cs]e|notice|copying)(?:$|\.)/i.test(name));
    const extras = await readdir(resolve(path, 'licenses')).catch(() => []);
    licenseNames.push(...extras.map(name => `licenses/${name}`));
    if (!licenseNames.length) throw new Error(`No license in bundled dependency ${info.name}`);
    result += `\n${'='.repeat(72)}\nComponent: ${info.name}\nVersion: ${info.version}\nLicense: ${info.license}\n`;
    for (const name of licenseNames.sort()) {
      result += `\nSource: ${name}\n${'-'.repeat(72)}\n${(await readFile(resolve(path, name), 'utf8')).replaceAll('\r\n', '\n').trimEnd()}\n`;
    }
  }
  const output = resolve(frontendRoot, 'internal/licensing/FRONTEND_LICENSES.txt');
  if (check) {
    if ((await readFile(output, 'utf8').catch(() => '')).replaceAll('\r\n', '\n') !== result) throw new Error('Frontend license notices are stale; run npm run build:frontend');
  } else await writeFile(output, result);
}
