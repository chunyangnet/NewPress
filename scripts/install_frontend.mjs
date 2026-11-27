// Explicit, process-local registry selection. Never rewrites lockfiles or npmrc.
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('../', import.meta.url));
const mode = process.argv[2];
const registries = { china: 'https://registry.npmmirror.com', official: 'https://registry.npmjs.org' };
if (!Object.hasOwn(registries, mode) || process.argv.length !== 3) {
  console.error('Usage: node scripts/install_frontend.mjs china|official');
  process.exit(2);
}
const lock = JSON.parse(readFileSync(new URL('../package-lock.json', import.meta.url), 'utf8'));
for (const [name, pkg] of Object.entries(lock.packages || {})) {
  if (!name) continue;
  if (!pkg.resolved || !pkg.integrity || !/^sha(256|384|512)-/.test(pkg.integrity)) throw new Error(`Missing integrity: ${name}`);
  const url = new URL(pkg.resolved);
  if (url.protocol !== 'https:' || !['registry.npmjs.org', 'registry.npmmirror.com'].includes(url.hostname) || url.username || url.password || url.port) {
    throw new Error(`Refusing to remap non-registry dependency: ${name}`);
  }
}
// npm's registry replacement applies to locked tarball URLs too; integrity remains
// enforced by npm ci. No audit/fund requests or dependency lifecycle scripts.
const args = ['ci', '--ignore-scripts', '--no-audit', '--no-fund', '--strict-ssl=true',
  '--replace-registry-host=always', `--registry=${registries[mode]}`,
  '--fetch-retries=1', '--fetch-timeout=30000'];
const result = spawnSync(process.platform === 'win32' ? 'npm.cmd' : 'npm', args, {
  cwd: root, stdio: 'inherit', shell: process.platform === 'win32',
});
if (result.error) console.error(result.error.message);
if (result.status !== 0) console.error('安装未完成；可显式选择另一个源重试。锁文件与全局配置未修改。');
process.exit(result.status ?? 1);
