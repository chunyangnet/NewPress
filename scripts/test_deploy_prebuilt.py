import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import build_deployment
import deploy
import deploy_prebuilt


class PrebuiltDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        (self.root / 'bin').mkdir()
        (self.root / 'bin/newpress').write_bytes(b'checked executable')
        self.manifest = dict(schema=1, os='linux', arch='amd64', commit='a' * 40,
                             binary_sha256=hashlib.sha256(b'checked executable').hexdigest())
        (self.root / 'deployment.json').write_text(json.dumps(self.manifest))

    def test_native_preparation_never_probes_go_or_source_dependencies(self):
        args = deploy.parser().parse_args(['--mode', 'native', '--yes'])
        with patch.object(deploy, 'ROOT', self.root), patch.object(deploy.sources, 'Selector') as selector:
            plan = deploy.prepare_sources(args, {}, 'native', False, 'amd64')
        self.assertEqual(plan, {'prebuilt': self.manifest})
        self.assertEqual(selector.return_value.mock_calls, [])

    def test_wrong_platform_and_modified_binary_stop_before_any_build(self):
        with self.assertRaises(ValueError):
            deploy_prebuilt.load(self.root, 'arm64')
        (self.root / 'bin/newpress').write_bytes(b'corrupt')
        with self.assertRaises(ValueError):
            deploy_prebuilt.load(self.root, 'amd64')

    def test_prebuilt_docker_needs_compose_but_never_buildx(self):
        with patch.object(deploy, 'ROOT', self.root), patch.object(deploy, 'run', return_value=subprocess.CompletedProcess([], 0, '2.40.0')) as run:
            deploy.check_docker_plugins()
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0], ['docker', 'compose', 'version', '--short'])

    def images(self, *, corrupt=False):
        data = b'prebuilt image archive'
        images = {key: {'tag': f'{name}:1.0.0-beta.2-amd64', 'id': 'sha256:' + str(index) * 64}
                  for index, (key, name) in enumerate([('app', 'newpress'), ('postgres', 'newpress-postgres'), ('proxy', 'newpress-nginx'), ('redis', 'newpress-redis')], 1)}
        checksum = hashlib.sha256(data).hexdigest()
        self.manifest['docker'] = dict(images=images, parts=[dict(name='newpress-test.part000', size=len(data), sha256=checksum)],
                                       size=len(data), sha256='0' * 64 if corrupt else checksum,
                                       mirrors={'github': 'https://github.com/example', 'gitee': 'https://gitee.com/example'})
        (self.root / 'INSTALL_SOURCE').write_text('gitee')
        calls, loaded = [], False

        def run(command, **kwargs):
            nonlocal loaded
            calls.append(command)
            if command[:2] == ['docker', 'load']:
                loaded = True
                return subprocess.CompletedProcess(command, 0)
            self.assertEqual(command[:3], ['docker', 'image', 'inspect'])
            item = next(value for value in images.values() if value['tag'] == command[3])
            return subprocess.CompletedProcess(command, 0 if loaded else 1,
                json.dumps([dict(Id=item['id'], Os='linux', Architecture='amd64')]))

        def download(choices, target, digest, log):
            self.assertEqual([choice['name'] for choice in choices], ['gitee'])
            self.assertEqual(digest, checksum)
            target.write_bytes(data)

        with patch.object(deploy_prebuilt.sources, 'download_verified', side_effect=download):
            if corrupt:
                with self.assertRaises(ValueError):
                    deploy_prebuilt.install_images(self.root, self.manifest, 'amd64', run, lambda _: None)
                self.assertFalse(loaded)
            else:
                result = deploy_prebuilt.install_images(self.root, self.manifest, 'amd64', run, lambda _: None)
                self.assertEqual(result['app'], images['app']['tag'])
                self.assertTrue(loaded)
        self.assertFalse(any('build' in command for command in calls))

    def test_docker_imports_verified_images_without_building(self):
        self.images()

    def test_corrupt_docker_archive_is_not_loaded(self):
        self.images(corrupt=True)

    def test_runtime_bundle_has_no_compose_build_or_source_tree(self):
        args = argparse.Namespace(output=self.root / 'out', binary=self.root / 'bin/newpress', os='windows', arch='amd64', commit='a' * 40, version='1.0.0-beta.2', docker=None)
        build_deployment.bundle(args)
        with zipfile.ZipFile(next(args.output.glob('*.zip'))) as archive:
            names = set(archive.namelist())
            self.assertIn('bin/newpress.exe', names)
            self.assertIn('scripts/deploy_prebuilt.py', names)
            self.assertFalse(any(name.startswith(('docs/', 'internal/', 'node_modules/')) or name == 'go.mod' for name in names))
            for name in names:
                if name.endswith('.yaml'):
                    self.assertNotIn('    build:', archive.read(name).decode())


if __name__ == '__main__':
    unittest.main()
