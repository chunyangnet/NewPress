#!/usr/bin/env python3
"""Reject private files, private documentation references and recognizable credentials."""
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
PRIVATE = {'docs', '.claude', '.secrets', '.playwright-cli', 'node_modules',
           'output', 'var', 'var-updates', '__pycache__'}
SECRET = re.compile(
    rb'-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----'
    rb'|\b(?:ghp_|github_pat_)[A-Za-z0-9_]{30,}\b'
    rb'|\bAKIA[0-9A-Z]{16}\b'
    rb'|\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}\b'
)
PRIVATE_DOCUMENT_REFERENCE = re.compile(
    r'''(?<![\w:/\\])(?:(?:\.\.?[/\\])+|/)?docs[/\\][^\s\x60<>()\[\]"']+'''
)


def documentation_errors(name, content):
    if Path(name).suffix.lower() != '.md':
        return []
    errors = []
    for number, line in enumerate(content.decode('utf-8').splitlines(), 1):
        for match in PRIVATE_DOCUMENT_REFERENCE.finditer(line):
            errors.append(f'{name}:{number}: reference to private documentation: {match[0]}')
    return errors


def main():
    # --include-untracked supports pre-initial-commit preparation in a fresh repo.
    args = ['git', 'ls-files', '-z', '--cached']
    if '--include-untracked' in sys.argv[1:]:
        args += ['--others', '--exclude-standard']
    result = subprocess.run(args, cwd=ROOT, capture_output=True, check=True)
    names = sorted(set(result.stdout.decode('utf-8').split('\0')) - {''})
    errors = []
    for name in names:
        path = Path(name)
        if (any(part in PRIVATE or part.startswith('.tool-install-') for part in path.parts)
                or (path.name.startswith('.env') and path.name != '.env.example')
                or path.suffix.lower() in {'.key', '.pem', '.p12', '.pfx', '.pid', '.log'}
                or name in {'newpress', 'newpress.exe', '.newpress-deploy.json',
                            '.newpress-deploy.lock', 'deploy/nginx.generated.conf'}):
            errors.append(f'{name}: private/local file')
            continue
        source = ROOT / path
        if source.is_symlink():
            errors.append(f'{name}: symlink requires publication review')
        elif source.is_file():
            content = source.read_bytes()
            if SECRET.search(content):
                errors.append(f'{name}: possible embedded credential (value withheld)')
            errors.extend(documentation_errors(name, content))
    if errors:
        print('\n'.join(errors), file=sys.stderr)
        return 1
    print(f'Public source check passed: {len(names)} files; no private paths, private documentation references or recognized credentials.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
