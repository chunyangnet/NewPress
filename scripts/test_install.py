"""Execute the public installer using isolated fake Git and deployment commands."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

BASH = shutil.which("bash") if os.name != "nt" else next((str(p) for p in (
    Path("D:/Program Files/Git/bin/bash.exe"), Path("C:/Program Files/Git/bin/bash.exe")) if p.exists()), None)
ROOT = Path(__file__).resolve().parents[1]


@unittest.skipUnless(BASH, "Bash unavailable")
class SourceInstallerTests(unittest.TestCase):
    def run_installer(self, source="auto", fail="", existing=False, expected="", mismatch=False):
        with tempfile.TemporaryDirectory(prefix="newpress-install-test-") as temporary:
            root = Path(temporary)
            binary = root / "bin"
            binary.mkdir()
            scripts = {
                "uname": "#!/usr/bin/env bash\nprintf '%s\\n' Linux\n",
                "git": r'''#!/usr/bin/env bash
set -eu
if [[ "$1" == check-ref-format ]]; then exit 0; fi
if [[ "$1" == -C ]]; then
  if [[ "$2" == */github && "$NP_MISMATCH" == 1 ]]; then printf '%040d\n' 2; else printf '%040d\n' 1; fi
  exit 0
fi
np_url='' np_dir="${!#}"
for np_arg in "$@"; do [[ "$np_arg" != https://* ]] || np_url="$np_arg"; done
printf '%s\n' "$np_url" >> "$NP_TEST_DIR/requests"
[[ "$NP_FAIL" != all ]] || exit 7
[[ "$NP_FAIL" != github || "$np_url" != *github.com* ]] || exit 7
mkdir -p "$np_dir/scripts"
printf '%s\n' 'module fixture' > "$np_dir/go.mod"
printf '%s\n' 'services: {}' > "$np_dir/compose.yaml"
cat > "$np_dir/scripts/deploy.sh" <<'DEPLOY'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$NP_TEST_DIR/deployed"
DEPLOY
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
            environment = dict(os.environ, NP_FAIL=fail, NP_MISMATCH="1" if mismatch else "0")
            result = subprocess.run(arguments, env=environment, capture_output=True, text=True,
                                    encoding="utf-8", timeout=30)
            requests = (root / "requests").read_text().splitlines() if (root / "requests").exists() else []
            deployed = (root / "deployed").read_text().splitlines() if (root / "deployed").exists() else []
            preserved = (destination / "keep.txt").read_text() if (destination / "keep.txt").exists() else None
            return result, requests, deployed, destination.exists(), preserved, list(root.glob(".newpress-install.*"))

    def test_gitee_entry_only_fetches_gitee_and_forwards_deployment_arguments(self):
        result, requests, deployed, exists, _, temporary = self.run_installer(source="gitee")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(requests, ["https://gitee.com/chunyangnet_0/newpress.git"])
        self.assertEqual(deployed, ["--mode", "native", "--url", "https://example.test"])
        self.assertTrue(exists)
        self.assertEqual(temporary, [])

    def test_auto_falls_back_to_gitee_when_github_unavailable(self):
        result, requests, deployed, _, _, _ = self.run_installer(fail="github")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(requests, ["https://github.com/chunyangnet/NewPress.git", "https://gitee.com/chunyangnet_0/newpress.git"])
        self.assertTrue(deployed)

    def test_existing_directory_is_preserved_without_network_requests(self):
        result, requests, deployed, _, preserved, temporary = self.run_installer(existing=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((requests, deployed, preserved, temporary), ([], [], "original", []))

    def test_all_sources_missing_do_not_create_a_partial_installation(self):
        result, requests, deployed, exists, _, temporary = self.run_installer(fail="all")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(requests), 2)
        self.assertEqual((deployed, exists, temporary), ([], False, []))

    def test_commit_mismatch_is_not_executed_and_can_use_matching_mirror(self):
        result, requests, deployed, _, _, _ = self.run_installer(expected="0" * 39 + "1", mismatch=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(requests), 2)
        self.assertTrue(deployed)

    def test_explicit_github_does_not_silently_use_gitee(self):
        result, requests, deployed, exists, _, _ = self.run_installer(source="github", fail="github")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(requests, ["https://github.com/chunyangnet/NewPress.git"])
        self.assertEqual((deployed, exists), ([], False))


if __name__ == "__main__":
    unittest.main()
