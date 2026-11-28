"""Load checked release binaries and Docker image archives without build tools."""
import hashlib
import json
from pathlib import Path
import re
import tempfile

import deploy_sources as sources


def load(root, arch):
    path = root / 'deployment.json'
    if not path.exists():
        return None
    if path.is_symlink() or path.stat().st_size > 65536:
        raise ValueError('部署包清单不是有效普通文件')
    data = json.loads(path.read_text(encoding='utf-8'))
    if (data.get('schema') != 1 or data.get('os') != 'linux' or data.get('arch') != arch
            or not re.fullmatch(r'[a-f0-9]{40}', data.get('commit', ''))):
        raise ValueError('部署包平台或版本信息不匹配')
    binary = root / 'bin/newpress'
    if binary.is_symlink() or not binary.is_file():
        raise ValueError('部署包缺少预编译程序，不会回退到源码构建')
    if hashlib.sha256(binary.read_bytes()).hexdigest() != data.get('binary_sha256'):
        raise ValueError('预编译程序 SHA-256 不匹配')
    return data


def install_images(root, manifest, arch, run, log):
    docker = manifest['docker']
    images = docker['images']
    if set(images) != {'app', 'postgres', 'proxy', 'redis'}:
        raise ValueError('部署包镜像清单不完整')
    for item in images.values():
        if (not re.fullmatch(r'newpress(?:-postgres|-nginx|-redis)?:[a-zA-Z0-9_.-]+', item['tag'])
                or not re.fullmatch(r'sha256:[a-f0-9]{64}', item['id'])):
            raise ValueError('部署包镜像标识无效')

    def matches(item):
        result = run(['docker', 'image', 'inspect', item['tag']], capture=True, check=False)
        if result.returncode:
            return False
        try:
            actual = json.loads(result.stdout)[0]
            return actual['Id'] == item['id'] and actual['Os'] == 'linux' and actual['Architecture'] == arch
        except (ValueError, KeyError, IndexError, TypeError):
            return False

    if not all(matches(item) for item in images.values()):
        origin = (root / 'INSTALL_SOURCE').read_text(encoding='utf-8').strip() if (root / 'INSTALL_SOURCE').exists() else 'auto'
        if origin not in ('auto', 'github'):
            raise ValueError('部署包来源设置无效')
        base = sources.https_base(docker['mirrors']['github'])
        first = docker['parts'][0]
        selector = sources.Selector('auto' if origin == 'auto' else 'official', timeout=10, log=log)
        ranked = selector.github_release(base + '/' + first['name'], first['size'])
        # Retain the measured order across all parts, promoting a working fallback.
        selected = [{'name': item['name'], 'base': item['url'].rsplit('/', 1)[0]} for item in ranked]
        with tempfile.TemporaryDirectory(prefix='newpress-images-') as temporary:
            directory = Path(temporary)
            archive = directory / 'images.tar.gz'
            digest, total = hashlib.sha256(), 0
            with archive.open('wb') as output:
                for index, part in enumerate(docker['parts']):
                    name = part['name']
                    if (not re.fullmatch(r'newpress-[a-zA-Z0-9_.-]+\.part[0-9]{3}', name)
                            or not 0 < part['size'] <= 64 * 1024 * 1024
                            or not re.fullmatch(r'[a-f0-9]{64}', part['sha256'])):
                        raise ValueError('镜像分片清单无效')
                    choices = [{'name': item['name'], 'url': item['base'] + '/' + name} for item in selected]
                    target = directory / f'part-{index}'
                    used = sources.download_verified(choices, target, part['sha256'], log, expected_size=part['size'])
                    selected.sort(key=lambda item: item['name'] != used['name'])
                    if target.stat().st_size != part['size']:
                        raise ValueError('镜像分片长度不匹配')
                    with target.open('rb') as source:
                        while block := source.read(1024 * 1024):
                            output.write(block)
                            digest.update(block)
                            total += len(block)
                    target.unlink()
            if digest.hexdigest() != docker['sha256'] or total != docker['size']:
                raise ValueError('完整镜像归档校验失败')
            run(['docker', 'load', '--input', archive])
    if not all(matches(item) for item in images.values()):
        raise ValueError('导入镜像的 ID 或平台与发布清单不符')
    return {name: item['tag'] for name, item in images.items()}
