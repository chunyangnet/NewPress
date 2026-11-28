import { readFile, writeFile, access } from 'node:fs/promises';
import { resolve } from 'node:path';
import { frontendEntries, compileFrontendEntry, frontendBanner, frontendRoot } from './frontend_entries.mjs';

export async function buildIndependentEntries({ check = false, scope } = {}) {
  for (const entry of frontendEntries) {
    for (const output of entry.outputs.filter(output => ['docshub', 'nexora'].includes(output.scope) && (!scope || scope === output.scope))) {
      const base = `themes/${output.scope}/`;
      if (!output.path.startsWith(base)) throw new Error(`Invalid theme output: ${output.path}`);
      if (!await access(resolve(frontendRoot, base)).then(() => true, () => false)) continue;
      const code = frontendBanner + (await compileFrontendEntry(entry.name)).code;
      const file = resolve(frontendRoot, output.path);
      if (check) {
        if (await readFile(file, 'utf8').catch(() => '') !== code) throw new Error(`${output.path} is stale`);
      } else await writeFile(file, code);
    }
  }
}
