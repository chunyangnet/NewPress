import path from 'node:path';
import { mkdirSync } from 'node:fs';
import { spawn } from 'node:child_process';
const root = process.cwd();
const data = path.join(root, 'output', 'explore-menu-preview', 'data');
mkdirSync(data, { recursive: true });
const child = spawn('go', ['run', './cmd/newpress', 'serve'], {
  cwd: root, stdio: 'inherit', env: { ...process.env,
    NEWPRESS_ADDR: '127.0.0.1:19349', NEWPRESS_PUBLIC_URL: 'http://localhost:19349',
    NEWPRESS_DATA_DIR: data, NEWPRESS_DATABASE_URL: '', NEWPRESS_SEED_DEMO: 'true',
    NEWPRESS_THEMES_DIR: path.join(root, 'themes'), NEWPRESS_PLUGINS_DIR: path.join(root, 'plugins')
  }
});
child.on('exit', code => process.exit(code ?? 1));
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
