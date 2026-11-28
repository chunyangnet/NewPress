import path from 'node:path';
import { cpSync, mkdirSync } from 'node:fs';
import { spawn } from 'node:child_process';
const root = process.cwd();
const workspace = path.join(root, 'output', 'narrow-header-preview');
mkdirSync(workspace, { recursive: true });
cpSync(path.join(root, 'themes', 'chunnel'), path.join(workspace, 'themes', 'chunnel'), { recursive: true });
mkdirSync(path.join(workspace, 'plugins'), { recursive: true });
const child = spawn('go', ['run', './cmd/newpress', 'serve'], {
  cwd: root, stdio: 'inherit', env: { ...process.env,
    NEWPRESS_ADDR: '127.0.0.1:19430', NEWPRESS_PUBLIC_URL: 'http://localhost:19430',
    NEWPRESS_DATA_DIR: path.join(workspace, 'data'), NEWPRESS_DATABASE_URL: '', NEWPRESS_SEED_DEMO: 'true',
    NEWPRESS_THEMES_DIR: path.join(workspace, 'themes'), NEWPRESS_PLUGINS_DIR: path.join(workspace, 'plugins')
  }
});
child.on('exit', code => process.exit(code ?? 1));
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
