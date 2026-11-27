#!/usr/bin/env python3
"""Exercise a signed release through the admin HTTP API against an isolated Gitee fixture."""
import argparse
import base64
import contextlib
import datetime as dt
import hashlib
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import queue
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import zipfile

ROOT = Path(__file__).resolve().parents[1]
TARGET = '1.0.0-beta.2'
BASE = '1.0.0-beta.1'
SHA = 'a' * 40


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def iso():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def wait(fn, seconds=90):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        try:
            result = fn()
            if result:
                return result
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(.15)
    raise RuntimeError(f'timed out waiting for {fn.__name__}')


@contextlib.contextmanager
def postgres(pg_bin, temp, env):
    cluster = temp / 'postgres'
    number = port()
    password = secrets.token_hex(24)
    pwfile = temp / 'pg-password'
    pwfile.write_text(password + '\n')
    args = {'env': dict(env, PGHOST='127.0.0.1', PGPORT=str(number), PGUSER='releaseqa', PGPASSWORD=password)}
    def pg(name, *options, **kwargs):
        return run([str(pg_bin / name), *map(str, options)], **args, **kwargs)
    pg('initdb', '-D', cluster, '-U', 'releaseqa', '-A', 'scram-sha-256', '--pwfile', pwfile, '--encoding=UTF8', '--no-locale', stdout=subprocess.DEVNULL)
    pg('pg_ctl', '-D', cluster, '-l', temp / 'pg.log', '-o', f'-h 127.0.0.1 -p {number}', '-w', 'start', stdout=subprocess.DEVNULL)
    try:
        pg('createdb', 'ota'); pg('createdb', 'recovery')
        yield f'postgres://releaseqa:{password}@127.0.0.1:{number}/ota?sslmode=disable', pg
    finally:
        pg('pg_ctl', '-D', cluster, '-m', 'fast', '-w', 'stop', stdout=subprocess.DEVNULL)


def exercise(root, seed, public, host, platform, env, on_download, db=None, pg=None, image=None, withdrawn=None):
    root.mkdir()
    site = root / 'site'; site.mkdir()
    proof = root / 'proof'; proof.mkdir()
    for name in ('themes', 'data', 'plugins', 'updates'):
        (site / name).mkdir()
    shutil.copytree(ROOT / 'themes/starter', site / 'themes/starter')
    theme = site / 'themes/starter/theme.json'
    manifest = json.loads(theme.read_bytes()); manifest['version'] = '2.1.0'
    theme.write_text(json.dumps(manifest), encoding='utf-8')
    (theme.parent / 'site-only.txt').write_bytes(b'local edit')
    number = port(); base = f'http://127.0.0.1:{number}'
    name = 'newpress-ota-' + uuid.uuid4().hex
    password = secrets.token_hex(22)
    hashed = run([str(seed), 'admin', 'hash-password'], env=env, input=password+'\n', capture_output=True, text=True).stdout.strip()
    app_root = Path('/qa/site') if image else site
    receipt_name = (Path('/qa/proof') if image else proof) / 'backup.json'
    values = dict(NEWPRESS_ADDR=f'127.0.0.1:{number}', NEWPRESS_PUBLIC_URL=base,
                  NEWPRESS_UPDATE_DIR=str(app_root/'updates'), NEWPRESS_DATA_DIR=str(app_root/'data'),
                  NEWPRESS_THEMES_DIR=str(app_root/'themes'), NEWPRESS_PLUGINS_DIR=str(app_root/'plugins'),
                  NEWPRESS_ACTIVE_THEME='starter', NEWPRESS_UPDATE_PUBLIC_KEY=public,
                  NEWPRESS_ADMIN_PASSWORD_HASH=hashed, NEWPRESS_UPDATE_OBSERVE_FOR='3m',
                  NEWPRESS_UPDATE_BACKUP_RECEIPT_FILE=str(receipt_name), NEWPRESS_UPDATE_CHECK_INTERVAL='0',
                  NEWPRESS_TEST_CORE_RELEASE_FIXTURE=host)
    if db:
        values['NEWPRESS_DATABASE_URL'] = db
    command = [str(seed), 'run']
    if image:
        if not sys.platform.startswith('linux') or os.getuid() == 0:
            raise RuntimeError('Docker smoke uses Linux host networking and a non-root test UID')
        command = ['docker', 'run', '--rm', '--name', name, '--network', 'host', '--read-only', '--user', f'{os.getuid()}:{os.getgid()}', '--cap-drop=ALL', '--security-opt=no-new-privileges', '--tmpfs', '/tmp:rw,mode=1777', '-v', f'{site}:/qa/site:rw', '-v', f'{proof}:/qa/proof:ro', '-v', f'{seed}:/qa/seed:ro']
        for k in values:
            command.extend(['--env', k])
    jar = http.cookiejar.CookieJar(); client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
    def request(path, body=None, method='GET', headers=None):
        req = urllib.request.Request(base+path, data=body, method=method, headers=headers or {})
        with client.open(req, timeout=4) as res:
            return res.read()
    def login():
        request('/admin/login/', urllib.parse.urlencode({'username':'admin', 'password':password, 'next':'/admin/updates/'}).encode(), 'POST', {'Origin':base, 'Content-Type':'application/x-www-form-urlencoded'})
        return json.loads(request('/api/v1/admin/session'))['csrf_token']
    def write(path, obj=None, method='POST'):
        data = b'' if obj is None else json.dumps(obj).encode()
        return json.loads(request('/api/v1/admin/updates/'+path, data, method, {'Origin':base, 'Content-Type':'application/json', 'X-CSRF-Token':csrf}))
    def status():
        return json.loads(request('/api/v1/admin/updates'))
    def finish_check():
        snap = status(); phase = snap['state']['phase']
        if phase == 'failed':
            raise RuntimeError(snap['state']['logs'][-1]['message'])
        if phase == 'available':
            return snap
    def wait_failed(ticket):
        def denied():
            state = json.loads(request('/api/v1/admin/updates/progress', headers={'X-Update-Ticket': ticket}))
            if state['phase'] in ('completed', 'rolled_back', 'cancelled'):
                raise AssertionError(f"expected a refused cutover, got {state['phase']}")
            return state if state['phase'] == 'failed' else None
        return wait(denied, 60)
    def instance():
        with client.open(base + '/health/ready', timeout=4) as res:
            return res.headers['X-NewPress-Instance']
    def start(*, committed=False):
        args = command
        if image:
            # Once installed, boot through the production image's own ENTRYPOINT and CMD.
            args = command + ([image] if committed else ['--entrypoint', '/qa/seed', image, 'run'])
        return subprocess.Popen(args, cwd=root, env=dict(env, **values), stdout=log, stderr=log)
    def stop(p):
        if image:
            subprocess.run(['docker','stop','--time','25',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        elif p.poll() is None:
            p.terminate()
        p.wait(timeout=40)
        # On Windows the parent's pipe closing also drains the serving child.
        def closed():
            with socket.socket() as sock:
                return sock.connect_ex(('127.0.0.1',number)) != 0
        wait(closed,20)
    with (root / 'app.log').open('wb') as log:
        proc = start()
        try:
            wait(lambda: request('/health/ready')); csrf = login()
            initial = status()
            assert initial['current'] == BASE
            defaults = initial['policy']
            assert not defaults['customized'] and not defaults['auto_check'] and defaults['launcher_applies_window']
            window = 2 if pg else 15
            saved = write('policy', dict(revision=defaults['revision'], auto_check=withdrawn is not None,
                                         check_interval_minutes=1, observation_seconds=window), 'PUT')
            assert saved['customized'] and saved['revision'] != defaults['revision']
            write('source', {'owner':'qa','repo':'release','ref':'main','manifest':'updates/stable.json','token':'fixture-read'}, 'PUT')
            if withdrawn is not None:
                def discovered():
                    current = status()
                    return current if current.get('available') else None
                snap = wait(discovered, 100)
                assert snap['state']['phase'] == 'idle' and snap['discovery']['interval'] == '1m0s'
                assert not (site/'updates/request.json').exists()
                print(f'PASS {platform} scheduled signed discovery before administrator approval', flush=True)
            else:
                write('check')
                snap = wait(finish_check)
            assert snap['trusted_publisher'] and snap['available']['version'] == TARGET
            attempt = {'revision':snap['revision'], 'version':TARGET, 'backup_confirmed':True}
            if pg:
                try:
                    write('start', attempt)
                except urllib.error.HTTPError as e:
                    assert e.code == 422
                else:
                    raise RuntimeError('PostgreSQL update accepted without a recovery record')
                # Stop writes during the backup. Verify real restores into isolated destinations.
                stop(proc); captured = iso(); dump = root/'database.dump'
                pg('pg_dump', '--format=custom', '-f', dump, 'ota')
                pg('pg_restore', '--exit-on-error', '-d', 'recovery', dump)
                count = pg('psql', '-d','recovery','-Atc','select count(*) from np_users', capture_output=True, text=True).stdout.strip()
                assert int(count) > 0
                filezip = root/'files.zip'
                with zipfile.ZipFile(filezip,'x',zipfile.ZIP_DEFLATED) as z:
                    for tree in ('data','plugins','themes'):
                        for f in (site/tree).rglob('*'):
                            if f.is_file() and f.name != '.newpress.lock':
                                z.write(f, f.relative_to(site))
                restored = root/'restore-files'
                with zipfile.ZipFile(filezip) as z:
                    assert z.testzip() is None; z.extractall(restored)
                assert (restored/'themes/starter/site-only.txt').read_bytes() == b'local edit'
                (proof/'backup.json').write_text(json.dumps(dict(schema=1,site=base,**{'from':BASE},to=TARGET,captured_at=captured,restore_verified_at=iso(),database_snapshot=hashlib.sha256(dump.read_bytes()).hexdigest(),site_files_snapshot=hashlib.sha256(filezip.read_bytes()).hexdigest())),encoding='utf-8')
                proc = start(); wait(lambda: request('/health/ready')); csrf = login()
                # The API accepts this receipt, but the supervisor must see it again before stopping the old process.
                receipt = proof / 'backup.json'
                verified = receipt.read_bytes()
                serving = instance()
                assert serving
                on_download.put(receipt.unlink)
                failure = wait_failed(write('start', attempt)['ticket'])
                assert '备份回执' in failure['logs'][-1]['message'] and instance() == serving
                assert not (site/'updates/transaction.json').exists()
                print(f'PASS {platform} PostgreSQL receipt revoked after admission, original process kept serving', flush=True)
                receipt.write_bytes(verified)
                write('check')
                attempt['revision'] = wait(finish_check)['revision']
            ticket = write('start', attempt)['ticket']
            def completed():
                state=json.loads(request('/api/v1/admin/updates/progress',headers={'X-Update-Ticket':ticket}))
                if state['phase'] in ('failed','rolled_back','cancelled'):
                    raise RuntimeError(state['logs'][-1]['message'])
                return state if state['phase']=='completed' else None
            result = wait(completed,120)
            guard_duration = dt.datetime.fromisoformat(result['protected_until']) - dt.datetime.fromisoformat(result['updated_at'])
            assert guard_duration == dt.timedelta(seconds=window), guard_duration
            assert any(entry['message'].find('观察') >= 0 for entry in result['logs'])
            csrf = login(); assert status()['current'] == TARGET
            active=json.loads((site/'updates/active.json').read_bytes()); assert active['version']==TARGET
            selected=site/'updates/releases'/active['id']/'themes/starter'
            assert (selected/'site-only.txt').read_bytes()==b'local edit' and json.loads((selected/'theme.json').read_bytes())['version']=='2.1.0'
            assert json.loads((site/'updates/previous.json').read_bytes())['version']==BASE
            if not pg:
                backup=next((site/'updates').glob('backup-*.zip'))
                report=json.loads(run([str(seed),'backup','inspect',str(backup)],env=env,cwd=root,capture_output=True,text=True,encoding='utf-8').stdout)
                assert report['verified']
            stop(proc)
            proc = start(committed=True)
            wait(lambda: request('/health/ready'))
            csrf = login()
            restarted = status()
            assert restarted['current'] == TARGET and restarted['policy'] == saved
            print(f'PASS {platform} saved update policy applied to cutover and persisted across restart', flush=True)
            if not pg:
                # Simulate a failed boot of this isolated staged executable after the supervisor exits.
                stop(proc)
                binary = 'newpress.exe' if os.name == 'nt' else 'newpress'
                with (selected.parent.parent / 'bin' / binary).open('ab') as f:
                    f.write(b'fixture disk fault')
                proc = start(committed=True)
                def restored_version():
                    state = json.loads(request('/api/v1/admin/updates/progress', headers={'X-Update-Ticket': ticket}))
                    return state['phase'] == 'rolled_back' and state['previous'] == BASE
                wait(restored_version, 60)
                csrf = login()
                assert status()['current'] == BASE and not (site/'updates/guard.json').exists()
                print(f'PASS {platform} persisted post-commit rollback after supervisor restart', flush=True)
            if withdrawn is not None:
                write('check')
                checked = wait(finish_check)
                on_download.put(withdrawn.set)
                approval = {'version': TARGET, 'revision': checked['revision'], 'backup_confirmed': True}
                receipt = write('start', approval)['ticket']
                failure = wait_failed(receipt)
                assert withdrawn.is_set() and '撤回' in failure['logs'][-1]['message']
                assert failure['downloaded'] == failure['total'] > 0
                assert not (site/'updates/request.json').exists() and not (site/'updates/releases'/failure['id']).exists()
                withdrawn.clear()  # Replay the previously signed commit against the same site.
                write('check')
                def replay_denied():
                    snap = status()
                    return snap if snap['state']['phase'] == 'failed' else None
                final = wait(replay_denied)
                assert final['current'] == BASE and 'available' not in final and '序号' in final['discovery']['message']
                print(f'PASS {platform} signed withdrawal during download, no switch, older commit rejected', flush=True)
            print(f'PASS {platform} {"Docker" if image else "native"} {"PostgreSQL restore" if pg else "file recovery archive"} signed HTTP cutover, site theme, restart',flush=True)
        finally:
            stop(proc)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pg-bin',type=Path,help='also test an isolated cluster using PostgreSQL server and client binaries')
    parser.add_argument('--docker-image',help='also exercise a read-only non-root Linux container using host networking')
    args=parser.parse_args()
    env={k:v for k,v in os.environ.items() if not k.upper().startswith('NEWPRESS_')}
    platform='/'.join(run(['go','env','GOOS','GOARCH'],cwd=ROOT,capture_output=True,text=True).stdout.split())
    with tempfile.TemporaryDirectory(prefix='newpress-ota-smoke-') as tmp:
        root=Path(tmp); key=root/'ephemeral-key'; package=root/'release'; seed=root/('seed.exe' if os.name=='nt' else 'seed')
        run(['go','run','./cmd/pluginmarket','-keygen','-key',str(key)],cwd=ROOT,env=env,stdout=subprocess.DEVNULL)
        expires=(dt.datetime.now(dt.timezone.utc)+dt.timedelta(hours=1)).isoformat(timespec='seconds')
        run([sys.executable,'scripts/build_update.py','--version',TARGET,'--platform',platform,'--output',str(package),'--data-compatible','--signing-key',str(key),'--sequence','1','--expires',expires],cwd=ROOT,env=env)
        withdrawal_dir = root/'withdrawal'
        run([sys.executable, 'scripts/build_update.py', '--version', TARGET, '--withdraw', '--output', str(withdrawal_dir),
             '--signing-key', str(key), '--sequence', '2', '--expires', expires], cwd=ROOT, env=env)
        revoked = (withdrawal_dir/'updates/stable.json').read_bytes()
        assert not (withdrawal_dir/'packages').exists()
        on_download, withdrawn = queue.Queue(), threading.Event()
        run(['go','test','-c','-trimpath','-o',str(seed),'./cmd/newpress'],cwd=ROOT,env=dict(env,CGO_ENABLED='0'))
        signed=(package/'updates/stable.json').read_bytes(); m=json.loads(base64.b64decode(json.loads(signed)['payload'])); artifact=m['artifacts'][0]; payload=package/artifact['path']
        assert payload.stat().st_size==artifact['size'] and hashlib.sha256(payload.read_bytes()).hexdigest()==artifact['sha256']
        class Provider(http.server.BaseHTTPRequestHandler):
            def log_message(self,*args): pass
            def do_GET(self):
                url=urllib.parse.urlsplit(self.path); q=urllib.parse.parse_qs(url.query); prefix='/api/v5/repos/qa/release/'
                if q.get('access_token')!=['fixture-read']: self.send_error(403);return
                old_ref, new_ref = [SHA], ['b' * 40]
                ref = q.get('ref')
                artifact_path = prefix + 'raw/' + artifact['path']
                if url.path == prefix + 'commits/main':
                    data = json.dumps({'sha': new_ref[0] if withdrawn.is_set() else SHA}).encode()
                elif url.path == prefix + 'raw/updates/stable.json' and ref in (old_ref, new_ref):
                    data = signed if ref == old_ref else revoked
                elif url.path == artifact_path and ref == old_ref:
                    data = payload.read_bytes()
                else:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                if url.path == artifact_path:
                    try:
                        on_half = on_download.get_nowait()
                    except queue.Empty:
                        on_half = None
                    if on_half:
                        mid = len(data) // 2
                        self.wfile.write(data[:mid])
                        self.wfile.flush()
                        on_half()
                        data = data[mid:]
                self.wfile.write(data)
        with http.server.ThreadingHTTPServer(('127.0.0.1',0),Provider) as server:
            host=f'http://127.0.0.1:{server.server_port}'; thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            env['NEWPRESS_TEST_CORE_RELEASE_FIXTURE']=host
            common=(seed,Path(str(key)+'.pub').read_text().strip(),host,platform,env,on_download)
            try:
                exercise(root/'file', *common, withdrawn=withdrawn)
                if args.pg_bin:
                    with postgres(args.pg_bin.resolve(),root,env) as (db,pg):
                        exercise(root/'pg',*common,db=db,pg=pg,image=args.docker_image)
                elif args.docker_image:
                    exercise(root/'container',*common,image=args.docker_image)
            finally:
                server.shutdown(); thread.join()

if __name__=='__main__':
    main()
