"""Execute the Bash bootstrap against isolated fake apt/curl/dpkg commands."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


BASH = shutil.which("bash") if os.name != "nt" else next((str(path) for path in (
    Path("D:/Program Files/Git/bin/bash.exe"), Path("C:/Program Files/Git/bin/bash.exe")) if path.exists()), None)


@unittest.skipUnless(BASH, "Bash unavailable")
class BootstrapTests(unittest.TestCase):
    def run_bootstrap(self, policy="auto", all_fail=False, ca_ready=True, action="install"):
        with tempfile.TemporaryDirectory(prefix="newpress-bootstrap-test-") as directory:
            root = Path(directory)
            binary = root / "bin"
            binary.mkdir()
            (root / "archive-key.gpg").write_bytes(b"test-only")
            programs = {
                "dpkg": "#!/usr/bin/env bash\nprintf '%s\\n' amd64\n",
                "curl": r'''#!/usr/bin/env bash
set -eu
np_url='' np_out=''
while (( $# )); do
  case "$1" in
    https://*) np_url="$1" ;;
    -o) shift; np_out="$1" ;;
  esac
  shift
done
printf '%s\n' "$np_url" >> "$NP_TEST_DIR/requests"
[[ "${NP_TEST_FAIL:-0}" != 1 ]] || exit 7
case "$np_url" in
  *tuna*) sleep 0.01 ;;
  *ustc*) sleep 0.15 ;;
  *aliyun*) sleep 0.25 ;;
  *) exit 7 ;;
esac
printf '%s\n' '-----BEGIN PGP SIGNED MESSAGE-----' 'Codename: bookworm' > "$np_out"
''',
                "apt-get": r'''#!/usr/bin/env bash
set -eu
np_source=''
for np_arg in "$@"; do
  case "$np_arg" in Dir::Etc::sourcelist=*) np_source="${np_arg#*=}" ;; esac
done
printf 'COMMAND %s\n' "$*" >> "$NP_TEST_DIR/apt.log"
cat "$np_source" >> "$NP_TEST_DIR/apt.log"
grep -q '^Signed-By:' "$np_source" || exit 8
grep -q 'bookworm-security' "$np_source" || exit 8
if grep -qE 'deb.debian.org|tuna' "$np_source"; then exit 3; fi
[[ "${NP_TEST_FAIL:-0}" != 1 ]]
''',
            }
            for name, content in programs.items():
                path = binary / name
                with path.open("w", encoding="utf-8", newline="\n") as stream:
                    stream.write(content)
                path.chmod(0o755)
            source = Path(__file__).with_name("bootstrap-python.sh").resolve().as_posix()
            script = r'''
set -euo pipefail
NP_TEST_DIR="$1"
if command -v cygpath >/dev/null; then NP_TEST_DIR="$(cygpath -u "$NP_TEST_DIR")"; fi
export NP_TEST_DIR
source "$2"
export PATH="$NP_TEST_DIR/bin:$PATH"
np_archive_key() { printf '%s/archive-key.gpg' "$NP_TEST_DIR"; }
np_ca_bundle_ready() { [[ "$NP_TEST_CA" == 1 ]]; }
np_policy="$3" np_timeout=1 ID=debian VERSION_CODENAME=bookworm
if [[ "$4" != install ]]; then
  np_bootstrap_python "$4"
else
  np_bootstrap_apt
fi
'''
            environment = dict(os.environ, NP_TEST_FAIL="1" if all_fail else "0", NP_TEST_CA="1" if ca_ready else "0")
            result = subprocess.run([BASH, "-c", script, "bootstrap-test", root.as_posix(), source, policy, action],
                                    env=environment, capture_output=True, text=True, encoding="utf-8", timeout=30)
            requests = (root / "requests").read_text() if (root / "requests").exists() else ""
            apt = (root / "apt.log").read_text() if (root / "apt.log").exists() else ""
            return result, requests, apt

    def test_fastest_usable_mirror_then_retry_installer_failure(self):
        result, requests, apt = self.run_bootstrap()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("deb.debian.org", requests)
        self.assertIn("mirrors.aliyun.com", requests)
        self.assertIn("mirrors.tuna.tsinghua.edu.cn", apt)
        self.assertIn("mirrors.ustc.edu.cn", apt)
        self.assertNotIn("mirrors.aliyun.com", apt)
        self.assertIn("install -y python3 ca-certificates", apt)
        self.assertNotIn("trusted=yes", apt)

    def test_all_failed_probes_do_not_install(self):
        result, requests, apt = self.run_bootstrap(all_fail=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(requests)
        self.assertEqual(apt, "")

    def test_official_policy_never_uses_other_mirrors(self):
        result, requests, apt = self.run_bootstrap(policy="official")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("mirrors.", requests)
        self.assertEqual(apt, "")

    def test_missing_ca_uses_signature_checked_apt_fallback(self):
        result, requests, apt = self.run_bootstrap(ca_ready=False)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(requests, "")
        self.assertIn("URIs: http://", apt)
        self.assertIn("Signed-By:", apt)
        self.assertIn("install -y python3 ca-certificates", apt)
        self.assertNotIn("AllowUnauthenticated", apt)

    def test_check_never_installs_bootstrap_dependencies(self):
        for action in ("--check", "check", "status", "logs", "restart", "stop", "admin", "admin-list", "admin-reset", "admin-rename", "--password-stdin"):
            with self.subTest(action=action):
                result, requests, apt = self.run_bootstrap(action=action)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual((requests, apt), ("", ""))


if __name__ == "__main__":
    unittest.main()
