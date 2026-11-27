import path from 'node:path';
import { mkdirSync, cpSync, existsSync } from 'node:fs';
import { spawn } from 'node:child_process';
const root = process.cwd();
const port = process.env.PORT || '19361';
const data = path.join(root, 'output', 'alignment-preview-20261121', 'data');
mkdirSync(data, { recursive: true });
const plugins = path.join(root, 'output', 'alignment-preview-20261121', 'plugins');
if (!existsSync(plugins)) cpSync(path.join(root, 'plugins'), plugins, { recursive: true, filter: source => !path.basename(source).includes('lock') });
const themes = path.join(root, 'output', 'alignment-preview-20261121', 'themes');
cpSync(path.join(root, 'themes'), themes, { recursive: true, filter: source => !path.basename(source).includes('lock') });
const child = spawn('go', ['run', './cmd/newpress', 'serve'], {
  cwd: root, stdio: 'inherit', env: { ...process.env,
    NEWPRESS_ADDR: `127.0.0.1:${port}`, NEWPRESS_PUBLIC_URL: `http://localhost:${port}`,
    NEWPRESS_DATA_DIR: data, NEWPRESS_DATABASE_URL: '', NEWPRESS_SEED_DEMO: 'true',
    NEWPRESS_THEMES_DIR: themes, NEWPRESS_PLUGINS_DIR: plugins
  }
});
child.on('exit', code => process.exit(code ?? 1));
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
