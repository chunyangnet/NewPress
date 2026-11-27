"""Check version admission before a core release creates output or invokes tools."""
import contextlib
import datetime
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import build_update


CASES = json.loads((build_update.ROOT / "internal/update/testdata/core_versions.json").read_text(encoding="utf-8"))


class BuildUpdateVersionTests(unittest.TestCase):
    pass


class BuildUpdateChannelTests(unittest.TestCase):
    def test_beta_channel_creates_the_official_beta_manifest(self):
        with tempfile.TemporaryDirectory(prefix="newpress-beta-") as directory:
            root = Path(directory)
            key = root / "private.key"
            key.write_bytes(b"fixture; real signing is covered by Go tests")
            output = root / "release"
            expires = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=1)).isoformat()
            argv = ["build_update.py", "--version", "1.0.0-beta.1", "--output", str(output),
                    "--signing-key", str(key), "--sequence", "1", "--expires", expires, "--withdraw", "--channel", "beta"]
            with mock.patch("sys.argv", argv), contextlib.redirect_stdout(io.StringIO()), \
                    mock.patch.object(build_update.subprocess, "check_output", return_value=b"signed-beta"):
                build_update.main()
            self.assertEqual((output / "updates/beta.json").read_bytes(), b"signed-beta")
            self.assertFalse((output / "updates/stable.json").exists())


class BuildUpdateKeyLocationTests(unittest.TestCase):
    def test_environment_path_precedes_local_git_configuration(self):
        with mock.patch.dict(build_update.os.environ, {"NEWPRESS_CORE_SIGNING_KEY": " /outside/core.key "}), \
                mock.patch.object(build_update.subprocess, "run") as git:
            self.assertEqual(build_update.configured_signing_key(), Path("/outside/core.key"))
            git.assert_not_called()

    def test_local_git_path_is_used_without_reading_key_contents(self):
        result = mock.Mock(returncode=0, stdout="/outside/core.key\n")
        with mock.patch.dict(build_update.os.environ, {"NEWPRESS_CORE_SIGNING_KEY": ""}), \
                mock.patch.object(build_update.subprocess, "run", return_value=result) as git:
            self.assertEqual(build_update.configured_signing_key(), Path("/outside/core.key"))
            self.assertIn("--local", git.call_args.args[0])

    def test_absent_configuration_never_generates_a_replacement_key(self):
        with tempfile.TemporaryDirectory(prefix="newpress-missing-key-") as directory:
            output = Path(directory) / "release"
            expires = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=1)).isoformat()
            argv = ["build_update.py", "--version", "1.0.0-beta.1", "--output", str(output),
                    "--sequence", "1", "--expires", expires, "--withdraw"]
            with mock.patch("sys.argv", argv), mock.patch.object(build_update, "configured_signing_key", return_value=None), \
                    mock.patch.object(build_update.subprocess, "check_output") as signer, \
                    contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    build_update.main()
                self.assertEqual(raised.exception.code, 2)
                signer.assert_not_called()
            self.assertFalse(output.exists())

    def test_local_key_location_is_used_when_argument_is_omitted(self):
        with tempfile.TemporaryDirectory(prefix="newpress-local-key-") as directory:
            root = Path(directory)
            key = root / "core.key"
            key.write_bytes(b"fixture")
            output = root / "release"
            expires = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=1)).isoformat()
            argv = ["build_update.py", "--version", "1.0.0-beta.1", "--output", str(output),
                    "--sequence", "1", "--expires", expires, "--withdraw", "--channel", "beta"]
            with mock.patch("sys.argv", argv), mock.patch.object(build_update, "configured_signing_key", return_value=key), \
                    mock.patch.object(build_update.subprocess, "check_output", return_value=b"signed") as signer, \
                    contextlib.redirect_stdout(io.StringIO()):
                build_update.main()
                command = signer.call_args.args[0]
                self.assertEqual(command[command.index("-key") + 1], str(key.absolute()))
            self.assertEqual((output / "updates/beta.json").read_bytes(), b"signed")


def version_case(case):
    def test(self):
        with tempfile.TemporaryDirectory(prefix="newpress-version-") as directory:
            root = Path(directory)
            key = root / "private.key"
            key.write_bytes(b"fixture key; signing is isolated in Go tests")
            output = root / "release"
            expires = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=1)).isoformat()
            argv = ["build_update.py", "--version", case["version"], "--output", str(output),
                    "--signing-key", str(key), "--sequence", "1", "--expires", expires, "--withdraw"]
            with mock.patch("sys.argv", argv), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()), \
                    mock.patch.object(build_update.subprocess, "check_output", return_value=b"signed-fixture") as signer, \
                    mock.patch.object(build_update, "build_packages") as builder:
                if case["builder"]:
                    build_update.main()
                    self.assertEqual((output / "updates/stable.json").read_bytes(), b"signed-fixture")
                    signer.assert_called_once()
                else:
                    with self.assertRaises(SystemExit) as raised:
                        build_update.main()
                    self.assertEqual(raised.exception.code, 2)
                    self.assertFalse(output.exists(), "invalid version created a publication directory")
                    signer.assert_not_called()
                builder.assert_not_called()
            self.assertEqual(key.read_bytes(), b"fixture key; signing is isolated in Go tests")
    return test


for case in CASES:
    setattr(BuildUpdateVersionTests, "test_" + case["name"], version_case(case))


if __name__ == "__main__":
    unittest.main()
