import path from 'node:path';
import { mkdirSync, writeFileSync, cpSync } from 'node:fs';
import { spawn, spawnSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
const root = process.cwd();
const data = path.join(root, 'output', 'explore-editor-preview', `data-${Date.now()}`);
mkdirSync(data, { recursive: true });
// Snapshot theme assets so concurrent builds cannot invalidate this preview.
const themes = path.join(data, 'preview-themes');
cpSync(path.join(root, 'themes', 'chunnel'), path.join(themes, 'chunnel'), {recursive:true});
const password = randomBytes(24).toString('base64url');
const hash = spawnSync('go', ['run', './cmd/newpress', 'admin', 'hash-password'], {cwd: root, input: password + '\n', encoding: 'utf8'});
if (hash.status !== 0) throw new Error('Preview password hashing failed');
writeFileSync(path.join(data, '../credentials.json'), JSON.stringify({username:'preview-editor', password}), {mode:0o600});
const child = spawn('go', ['run', './cmd/newpress', 'serve'], {
  cwd: root, stdio: 'inherit', env: { ...process.env,
    NEWPRESS_ADDR: `127.0.0.1:${process.env.PORT || 19364}`, NEWPRESS_PUBLIC_URL: `http://localhost:${process.env.PORT || 19364}`,
    NEWPRESS_DATA_DIR: data, NEWPRESS_DATABASE_URL: '', NEWPRESS_SEED_DEMO: 'true',
    NEWPRESS_ADMIN_USERNAME: 'preview-editor', NEWPRESS_ADMIN_PASSWORD_HASH: hash.stdout.trim(), NEWPRESS_ADMIN_PASSWORD_HASH_FILE: '', NEWPRESS_SETUP_TOKEN: '', NEWPRESS_SETUP_TOKEN_FILE: '',
    NEWPRESS_THEMES_DIR: themes, NEWPRESS_PLUGINS_DIR: path.join(root, 'plugins')
  }
});
child.on('exit', code => process.exit(code ?? 1));
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
