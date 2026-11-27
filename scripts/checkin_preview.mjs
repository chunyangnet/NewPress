// Isolated local preview; never opens the site's normal data directory.
import { spawn } from 'node:child_process';
const port = process.env.PORT || '19348';
const env = { ...process.env, NEWPRESS_ADDR: `127.0.0.1:${port}`, NEWPRESS_DATA_DIR: './output/checkin-preview-data', NEWPRESS_DATABASE_URL: '', NEWPRESS_SETUP_TOKEN: '', NEWPRESS_ADMIN_PASSWORD_HASH: '', NEWPRESS_HTTP_BIND: '127.0.0.1', NEWPRESS_HTTP_PORT: port, NEWPRESS_PUBLIC_URL: `http://localhost:${port}`, NEWPRESS_SEED_DEMO: 'true' };
const hash = spawn('go', ['run', './cmd/newpress', 'admin', 'hash-password'], { env, stdio: ['pipe','pipe','inherit'] });
let value = ''; hash.stdout.on('data', chunk => value += chunk); hash.stdin.end('CheckinPreview-2026!Only\n');
hash.on('exit', code => { if (code) process.exit(code); env.NEWPRESS_ADMIN_PASSWORD_HASH = value.trim(); const child = spawn('go', ['run', './cmd/newpress', 'serve'], { env, stdio: 'inherit' }); child.on('exit', code => process.exit(code || 0)); });
