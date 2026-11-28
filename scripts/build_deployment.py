#!/usr/bin/env python3
"""Package a built executable and prebuilt Docker images for end-user deployment."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]


def strip_build(text):
    # Keep the Compose anchors and runtime configuration byte-for-byte.
    result, skipping = [], False
    for line in text.splitlines(keepends=True):
        if re.fullmatch(r'    build:\s*', line):
            skipping = True
            continue
        if skipping and (not line.strip() or len(line) - len(line.lstrip()) > 4):
            continue
        skipping = False
        result.append(line)
    return ''.join(result)


def docker_metadata(args):
    images = {}
    for name, prefix in [('app', 'newpress'), ('postgres', 'newpress-postgres'), ('proxy', 'newpress-nginx'), ('redis', 'newpress-redis')]:
        tag = f'{prefix}:{args.version}-{args.arch}'
        info = json.loads(subprocess.check_output(['docker', 'image', 'inspect', tag]))[0]
        if info['Os'] != 'linux' or info['Architecture'] != args.arch:
            raise ValueError('Docker image platform mismatch')
        images[name] = {'tag': tag, 'id': info['Id']}
    args.output.mkdir(parents=True, exist_ok=True)
    digest, total, parts = hashlib.sha256(), 0, []
    with args.archive.open('rb') as source:
        while data := source.read(48 * 1024 * 1024):
            name = f'newpress-{args.version}-linux-{args.arch}-docker.tar.gz.part{len(parts):03d}'
            destination = args.output / name
            with destination.open('xb') as stream:
                stream.write(data)
            parts.append({'name': name, 'size': len(data), 'sha256': hashlib.sha256(data).hexdigest()})
            digest.update(data)
            total += len(data)
    result = {'images': images, 'sha256': digest.hexdigest(), 'size': total, 'parts': parts,
              'mirrors': {'github': f'https://github.com/chunyangnet/NewPress/releases/download/v{args.version}',
                          'gitee': f'https://gitee.com/chunyangnet_0/newpress/raw/releases/distributions/{args.version}'}}
    (args.output / f'docker-linux-{args.arch}.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')


def bundle(args):
    args.output.mkdir(parents=True, exist_ok=True)
    binary = args.binary.read_bytes()
    files = {'bin/newpress.exe' if args.os == 'windows' else 'bin/newpress': binary}
    for name in ('LICENSE', 'THIRD_PARTY_NOTICES.md', 'BINARY_RELEASES.md', 'README.md', 'SESSION_PERSISTENCE.md', '.env.example', 'compose.yaml'):
        files[name] = (ROOT / name).read_bytes()
    files['THIRD_PARTY_LICENSES.txt'] = (ROOT / 'internal/licensing/THIRD_PARTY_LICENSES.txt').read_bytes()
    for directory in ('themes/chunnel', 'plugins', 'deploy'):
        for path in sorted((ROOT / directory).rglob('*')):
            if path.is_symlink():
                raise ValueError('release inputs must not contain links')
            if path.is_file():
                name = path.relative_to(ROOT).as_posix()
                if path.name.startswith('Dockerfile') or path.name in ('go-toolchain.json', 'nginx.generated.conf'):
                    continue
                files[name] = path.read_bytes()
    for path in sorted((ROOT / 'scripts').glob('deploy*')):
        if path.suffix in ('.py', '.sh'):
            files[path.relative_to(ROOT).as_posix()] = path.read_bytes()
    files['scripts/bootstrap-python.sh'] = (ROOT / 'scripts/bootstrap-python.sh').read_bytes()
    files['RELEASE_COMMIT'] = (args.commit + '\n').encode()
    for name in list(files):
        if name.endswith('.yaml'):
            files[name] = strip_build(files[name].decode('utf-8')).encode()
    metadata = {'schema': 1, 'version': args.version, 'commit': args.commit, 'os': args.os, 'arch': args.arch,
                'binary_sha256': hashlib.sha256(binary).hexdigest()}
    if args.os == 'linux':
        if args.docker is None:
            raise ValueError('Linux deployment bundles require prebuilt Docker image metadata')
        metadata['docker'] = json.loads(args.docker.read_text(encoding='utf-8'))
    files['deployment.json'] = (json.dumps(metadata, indent=2) + '\n').encode()
    prefix = f'newpress-{args.version}-{args.os}-{args.arch}'
    runtime = args.output / (prefix + '-runtime.zip')
    with zipfile.ZipFile(runtime, 'x', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
        for name, data in sorted(files.items()):
            entry = zipfile.ZipInfo(name)
            entry.create_system = 3
            entry.external_attr = ((0o100755 if name.startswith('bin/') or name.endswith('.sh') else 0o100644) << 16)
            archive.writestr(entry, data, compress_type=zipfile.ZIP_DEFLATED)
    if args.os == 'linux':
        deployment = args.output / (prefix + '-deploy.tar.gz')
        with deployment.open('xb') as raw, gzip.GzipFile(filename='', mode='wb', fileobj=raw, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode='w') as archive:
                for name, data in sorted(files.items()):
                    entry = tarfile.TarInfo(name)
                    entry.size = len(data)
                    entry.mode = 0o755 if name.startswith('bin/') or name.endswith('.sh') else 0o644
                    archive.addfile(entry, io.BytesIO(data))
        checksum = hashlib.sha256(deployment.read_bytes()).hexdigest()
        Path(str(deployment) + '.sha256').write_text(checksum + '  ' + deployment.name + '\n', encoding='ascii')
    print(f'Created {prefix} prebuilt deployment artifacts', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_subparsers(dest='mode', required=True)
    docker = modes.add_parser('docker')
    docker.add_argument('--archive', type=Path, required=True)
    package = modes.add_parser('bundle')
    package.add_argument('--binary', type=Path, required=True)
    package.add_argument('--os', choices=('linux', 'windows'), required=True)
    package.add_argument('--commit', required=True)
    package.add_argument('--docker', type=Path)
    for command in (docker, package):
        command.add_argument('--version', required=True)
        command.add_argument('--arch', choices=('amd64', 'arm64'), required=True)
        command.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', args.version):
        parser.error('invalid version')
    if args.mode == 'bundle' and not re.fullmatch(r'[a-f0-9]{40}', args.commit):
        parser.error('invalid commit')
    (docker_metadata if args.mode == 'docker' else bundle)(args)


if __name__ == '__main__':
    main()
