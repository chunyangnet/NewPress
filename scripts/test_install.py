"""Execute the prebuilt installer with fake HTTPS downloads and real archives."""
import os
import hashlib
import io
import tarfile
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

BASH = shutil.which("bash") if os.name != "nt" else next((str(p) for p in (
    Path("D:/Program Files/Git/bin/bash.exe"), Path("C:/Program Files/Git/bin/bash.exe")) if p.exists()), None)
ROOT = Path(__file__).resolve().parents[1]


@unittest.skipUnless(BASH, "Bash unavailable")
class PrebuiltInstallerTests(unittest.TestCase):
    def run_installer(self, source="auto", fail="", existing=False, expected="", mismatch=False, corrupt=""):
        with tempfile.TemporaryDirectory(prefix="newpress-install-test-") as temporary:
            root = Path(temporary)
            binary = root / "bin"
            binary.mkdir()
            for provider in ('github',):
                bundle = root / (provider + '.tar.gz')
                with tarfile.open(bundle, 'w:gz') as archive:
                    files = {'bin/newpress': os.urandom(300000), 'deployment.json': b'{}', 'compose.yaml': b'services: {}',
                             'RELEASE_COMMIT': ('0' * 39 + ('2' if mismatch and provider == 'github' else '1')).encode(),
                             'scripts/deploy.sh': b'#!/usr/bin/env bash\nprintf \'%s\\n\' "$@" > "$NP_TEST_DIR/deployed"\n'}
                    for name, data in files.items():
                        entry = tarfile.TarInfo(name); entry.size = len(data); archive.addfile(entry, io.BytesIO(data))
                (root / (provider + '.sha256')).write_text(hashlib.sha256(bundle.read_bytes()).hexdigest() + '\n')
            scripts = {
                "uname": "#!/usr/bin/env bash\nif [[ \"$1\" == -m ]]; then echo x86_64; else echo Linux; fi\n",
                "git": "#!/usr/bin/env bash\nexit 99\n",
                "go": "#!/usr/bin/env bash\nexit 99\n",
                "curl": r'''#!/usr/bin/env bash
set -eu
np_url="${!#}" np_output='' np_probe=no
while (( $# )); do
  case "$1" in --output) np_output="$2"; shift ;; --range) np_probe=yes; shift ;; esac
  shift
done
printf '%s\n' "$np_url" >> "$NP_TEST_DIR/requests"
if [[ "$np_url" == *.sha256 ]]; then
  [[ "$NP_FAIL" != metadata ]] || exit 7
  cp "$NP_TEST_DIR/github.sha256" "$np_output"
  exit 0
fi
case "$np_url" in
  https://ghfast.top/*) provider=ghfast; speed=400000 ;;
  https://gh-proxy.com/*) provider=gh-proxy; speed=200000 ;;
  https://ghproxy.net/*) provider=ghproxy; speed=100000 ;;
  https://github.com/*) provider=github; speed=50000 ;;
  *) exit 9 ;;
esac
[[ "$NP_FAIL" != all && "$NP_FAIL" != "$provider" ]] || exit 7
if [[ "$np_probe" == yes ]]; then
  head -c 262144 "$NP_TEST_DIR/github.tar.gz" > "$np_output"
  printf '206 %s 262144' "$speed"
elif [[ "$NP_CORRUPT" == "$provider" ]]; then
  printf 'tampered content' > "$np_output"
else
  cp "$NP_TEST_DIR/github.tar.gz" "$np_output"
fi
''',
            }
            for name, content in scripts.items():
                p = binary / name
                p.write_text(content, encoding="utf-8", newline="\n")
                p.chmod(0o755)
            destination = root / "site"
            if existing:
                destination.mkdir()
                (destination / "keep.txt").write_text("original", encoding="utf-8")
            command = r'''
set -euo pipefail
NP_TEST_DIR="$1"
if command -v cygpath >/dev/null; then NP_TEST_DIR="$(cygpath -u "$NP_TEST_DIR")"; fi
export NP_TEST_DIR
export PATH="$NP_TEST_DIR/bin:$PATH"
shift
bash "$@"
'''
            arguments = [BASH, "-c", command, "installer-fixture", root.as_posix(), (ROOT / "install.sh").as_posix(),
                         "--source", source, "--dir", destination.as_posix()]
            if expected:
                arguments += ["--commit", expected]
            arguments += ["--", "--mode", "native", "--url", "https://example.test"]
            environment = dict(os.environ, NP_FAIL=fail, NP_CORRUPT=corrupt)
            result = subprocess.run(arguments, env=environment, capture_output=True, text=True,
                                    encoding="utf-8", timeout=30)
            requests = (root / "requests").read_text().splitlines() if (root / "requests").exists() else []
            deployed = (root / "deployed").read_text().splitlines() if (root / "deployed").exists() else []
            preserved = (destination / "keep.txt").read_text() if (destination / "keep.txt").exists() else None
            return result, requests, deployed, destination.exists(), preserved, list(root.glob(".newpress-install.*"))

    def test_auto_measures_all_sources_and_downloads_fastest(self):
        result, requests, deployed, exists, _, temporary = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(requests), 6)
        self.assertTrue(requests[0].startswith('https://gitee.com/'))
        self.assertTrue(requests[-1].startswith('https://ghfast.top/'))
        self.assertTrue(all(not url.endswith('.sha256') for url in requests[1:]))
        self.assertEqual(deployed, ["--mode", "native", "--url", "https://example.test"])
        self.assertTrue(exists)
        self.assertEqual(temporary, [])

    def test_unreachable_fastest_mirror_selects_next_available(self):
        result, requests, deployed, _, _, _ = self.run_installer(fail="ghfast")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(requests[-1].startswith('https://gh-proxy.com/'))
        self.assertTrue(deployed)

    def test_corrupt_fastest_mirror_is_rejected_and_next_mirror_used(self):
        result, requests, deployed, _, _, _ = self.run_installer(corrupt='ghfast')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(requests[-2].startswith('https://ghfast.top/'))
        self.assertTrue(requests[-1].startswith('https://gh-proxy.com/'))
        self.assertTrue(deployed)

    def test_official_checksum_failure_does_not_trust_proxy_checksum(self):
        result, requests, deployed, exists, _, _ = self.run_installer(fail='metadata')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(requests), 2)
        self.assertTrue(all(url.endswith('.sha256') for url in requests))
        self.assertEqual((deployed, exists), ([], False))

    def test_existing_directory_is_preserved_without_network_requests(self):
        result, requests, deployed, _, preserved, temporary = self.run_installer(existing=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((requests, deployed, preserved, temporary), ([], [], "original", []))

    def test_all_sources_missing_do_not_create_a_partial_installation(self):
        result, requests, deployed, exists, _, temporary = self.run_installer(fail="all")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(requests), 9)
        self.assertEqual((deployed, exists, temporary), ([], False, []))

    def test_commit_mismatch_is_never_executed(self):
        result, requests, deployed, _, _, _ = self.run_installer(expected="0" * 39 + "1", mismatch=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(deployed)

    def test_explicit_github_does_not_use_proxies(self):
        result, requests, deployed, exists, _, _ = self.run_installer(source="github", fail="github")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(requests), 3)
        self.assertTrue(all(url.startswith('https://github.com/') for url in requests[1:]))
        self.assertEqual((deployed, exists), ([], False))


if __name__ == "__main__":
    unittest.main()
