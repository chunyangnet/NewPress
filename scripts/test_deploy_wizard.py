"""Wizard decisions, read-only preflight, and install fallback contracts."""
import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from test_deploy import deploy, result


class WizardTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(patch.object(deploy, "ROOT", self.root))
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))

    def mock_linux(self):
        for key, value in (("NATIVE_ENV", self.root / "etc/newpress.env"),
                           ("NATIVE_DATA", self.root / "data"), ("UNIT", self.root / "newpress.service")):
            self.stack.enter_context(patch.object(deploy, key, value))
        self.stack.enter_context(patch.object(deploy.platform, "system", return_value="Linux"))
        self.stack.enter_context(patch.object(deploy.platform, "machine", return_value="x86_64"))
        self.stack.enter_context(patch.object(deploy.os, "geteuid", return_value=0, create=True))
        original_is_dir = Path.is_dir
        self.stack.enter_context(patch.object(Path, "is_dir", lambda path:
            True if path.as_posix() == "/run/systemd/system" else original_is_dir(path)))
        self.stack.enter_context(patch.object(deploy.shutil, "which", side_effect=lambda name:
            "systemctl" if name == "systemctl" else None))
        self.stack.enter_context(patch.object(deploy, "docker_ready", return_value=False))
        self.stack.enter_context(patch.object(deploy, "port_available", return_value=True))

    def test_manual_local_proxy_defaults_and_retry_busy_or_invalid_port(self):
        args = deploy.parser().parse_args(["--interactive"])
        values = {"NEWPRESS_PROXY_MODE": "local"}
        with patch("builtins.input", side_effect=["", "no", "80", "8080", "9090", "https://news.example", ""]) as prompts, \
                patch.object(deploy, "port_available", side_effect=[False, True]):
            deploy.wizard_network(args, values, "native", False)
        deploy.configure_proxy(values, args, "native")
        self.assertEqual((args.port, args.bind, args.source_mode), (9090, "127.0.0.1", "auto"))
        self.assertEqual(values["NEWPRESS_TRUSTED_PROXY_CIDRS"], "127.0.0.1/32")
        self.assertEqual(deploy.resolve_network(values, args)[0], "https://news.example")
        self.assertEqual(prompts.call_count, 7)

    def test_existing_values_and_busy_existing_port_are_kept(self):
        values = {"NEWPRESS_PROXY_MODE": "remote", "NEWPRESS_HTTP_BIND": "10.1.1.5", "NEWPRESS_HTTP_PORT": "9090",
                  "NEWPRESS_EXTERNAL_PROXY_CIDRS": "10.1.1.1/32", "NEWPRESS_PUBLIC_URL": "https://news.example",
                  "NEWPRESS_SOURCE_MODE": "official"}
        args = deploy.parser().parse_args(["--interactive"])
        with patch("builtins.input", return_value=""), patch.object(deploy, "port_available") as available:
            deploy.wizard_network(args, values, "docker", True)
        available.assert_not_called()
        self.assertEqual((args.port, args.source_mode), (9090, "official"))
        deploy.configure_proxy(values, args, "docker")
        self.assertEqual(values["NEWPRESS_EXTERNAL_PROXY_CIDRS"], "10.1.1.1/32")

    def test_explicit_arguments_skip_questions(self):
        args = deploy.parser().parse_args(["--yes", "--port", "9000", "--source-mode", "official"])
        values = {"NEWPRESS_PROXY_MODE": "remote", "NEWPRESS_EXTERNAL_PROXY_CIDRS": "10.8.1.6"}
        with patch("builtins.input") as prompt:
            deploy.wizard_network(args, values, "native", False)
        prompt.assert_not_called()
        deploy.configure_proxy(values, args, "native")
        deploy.configure_sources(values, args)
        self.assertEqual(values["NEWPRESS_TRUSTED_PROXY_CIDRS"], "10.8.1.6/32")
        self.assertIsNone(args.bind)
        self.assertEqual(args.docker_mirror, [])

    def test_direct_http_port_change_updates_default_public_url(self):
        values = {"NEWPRESS_PROXY_MODE": "none", "NEWPRESS_HTTP_BIND": "0.0.0.0", "NEWPRESS_HTTP_PORT": "8080",
                  "NEWPRESS_PUBLIC_URL": "http://192.0.2.10:8080"}
        args = deploy.parser().parse_args(["--interactive", "--port", "9090"])
        with patch("builtins.input", side_effect=["", "", ""]):
            deploy.wizard_network(args, values, "native", True)
        self.assertEqual(args.url, "http://192.0.2.10:9090")

    def test_manual_native_trust_is_preserved(self):
        values = {"NEWPRESS_PROXY_MODE": "remote", "NEWPRESS_EXTERNAL_PROXY_CIDRS": "10.1.1.1/32",
                  "NEWPRESS_TRUSTED_PROXY_CIDRS": "10.1.1.1/32"}
        args = deploy.parser().parse_args(["--yes"])
        deploy.configure_proxy(values, args, "native")
        self.assertEqual(values["NEWPRESS_TRUSTED_PROXY_CIDRS"], "10.1.1.1/32")

    def test_unbounded_or_invalid_trusted_proxy_is_rejected(self):
        for value in ("", "0.0.0.0/0", "::/0", "224.0.0.1", "garbage", "10.0.0.1; quit"):
            with self.subTest(value=value), self.assertRaises((ValueError, deploy.DeployError)):
                deploy.validate_proxy_cidrs(value)
        self.assertEqual(deploy.validate_proxy_cidrs("10.2.3.4,10.2.3.4/32,2001:db8::1"), "10.2.3.4/32,2001:db8::1/128")

    def test_eof_cancels_instead_of_accepting_defaults(self):
        with patch("builtins.input", side_effect=EOFError), self.assertRaisesRegex(deploy.DeployError, "取消"):
            deploy.yes_no("开始部署")

    def test_source_preferences_roundtrip_and_official_policy(self):
        values = {"NEWPRESS_SOURCE_MODE": "official", "NEWPRESS_DOCKER_MIRRORS": "https://mirror.example/prefix"}
        args = deploy.parser().parse_args([])
        deploy.configure_sources(values, args)
        self.assertEqual(args.docker_mirror, ["https://mirror.example/prefix"])
        candidates = deploy.sources.image_candidates("golang@sha256:" + "a" * 64, args.source_mode, args.docker_mirror)
        self.assertEqual(len(candidates), 1)
        args = deploy.parser().parse_args(["--source-mode", "auto", "--docker-mirror", "http://bad"])
        with self.assertRaises(deploy.sources.SourceError):
            deploy.configure_sources({}, args)

    def test_generated_proxy_config_trusts_only_selected_sources(self):
        (self.root / "deploy").mkdir()
        (self.root / "deploy/nginx.conf").write_text("server {\n    server_name _;\n}\n")
        values = {"NEWPRESS_PROXY_MODE": "local", "NEWPRESS_EDGE_SUBNET": "172.30.30.0/24"}
        deploy.write_proxy_config(values)
        generated = self.root / "deploy/nginx.generated.conf"
        self.assertIn("set_real_ip_from 172.30.30.1/32;", generated.read_text())
        self.assertNotIn("set_real_ip_from 172.30.30.0/24", generated.read_text())
        values.update(NEWPRESS_PROXY_MODE="remote", NEWPRESS_EXTERNAL_PROXY_CIDRS="10.0.0.8")
        deploy.write_proxy_config(values)
        self.assertIn("set_real_ip_from 10.0.0.8/32;", generated.read_text())
        self.assertNotIn("172.30.30.1", generated.read_text())
        values["NEWPRESS_PROXY_MODE"] = "none"
        deploy.write_proxy_config(values)
        self.assertEqual(values["NEWPRESS_NGINX_CONFIG"], "./deploy/nginx.conf")

    def test_cancel_after_preflight_does_not_write_or_install(self):
        self.mock_linux()
        with patch("builtins.input", return_value="n"), patch.object(deploy, "prepare_sources", return_value={}) as probes, \
                patch.object(deploy, "deploy_native") as install, patch.object(deploy, "atomic_write") as writes:
            code = deploy.main(["--interactive", "--mode", "native", "--url", "http://news.example:9090", "--port", "9090",
                                "--bind", "0.0.0.0", "--source-mode", "auto"])
        self.assertEqual(code, 0)
        probes.assert_called_once()
        install.assert_not_called()
        writes.assert_not_called()
        self.assertEqual(list(self.root.iterdir()), [])

    def test_check_with_prebuilt_binary_is_offline_and_read_only(self):
        self.mock_linux()
        binary = self.root / "candidate"
        binary.write_bytes(b"\x7fELF\x02\x01" + bytes(12) + (62).to_bytes(2, "little"))
        with patch.object(deploy.sources, "Selector") as selector, patch.object(deploy, "atomic_write") as writes:
            self.assertEqual(deploy.main(["--yes", "--check", "--mode", "native", "--binary", str(binary),
                                          "--url", "https://news.example"]), 0)
        self.assertEqual(selector.return_value.method_calls, [])
        writes.assert_not_called()
        self.assertEqual(list(self.root.iterdir()), [binary])

    def test_nonterminal_requires_explicit_yes(self):
        self.mock_linux()
        with patch.object(deploy.sys.stdin, "isatty", return_value=False), self.assertRaisesRegex(deploy.DeployError, "--yes"):
            deploy.main(["--mode", "native", "--url", "http://news.example"])

    def test_docker_pull_fallback_keeps_pin(self):
        digest = "sha256:" + "c" * 64
        plan = {"images": {"go": [
            {"name": "fast", "url": "first.example/library/golang@" + digest},
            {"name": "fallback", "url": "second.example/library/golang@" + digest}]}}
        with patch.object(deploy, "run", side_effect=[result(code=124), result()]) as run:
            images = deploy.pull_images(plan, "arm64", 30)
        self.assertEqual(images["go"], plan["images"]["go"][1]["url"])
        self.assertTrue(all(digest in call.args[0][-1] for call in run.call_args_list))
        with patch.object(deploy, "run", return_value=result(code=1)), self.assertRaises(deploy.DeployError):
            deploy.pull_images(plan, "arm64", 30)

    def test_toolchain_detection_never_triggers_implicit_download(self):
        (self.root / "go.mod").write_text("module test\ntoolchain go1.26.10\n")
        with patch.object(deploy.shutil, "which", return_value="go"), \
                patch.object(deploy, "run", return_value=result("go version go1.26.8 linux/amd64")) as run:
            self.assertIsNone(deploy.matching_go())
        self.assertEqual(run.call_args.kwargs["env"]["GOTOOLCHAIN"], "local")

    def test_fresh_install_sudo_keeps_answers_and_does_not_prompt_again(self):
        self.mock_linux()
        with patch.object(deploy.os, "geteuid", return_value=1000, create=True), \
                patch.object(deploy.shutil, "which", side_effect=lambda name: name if name in ("systemctl", "sudo") else None), \
                patch.object(deploy, "prepare_sources", return_value={}), \
                patch("builtins.input", side_effect=["2", "", "9090", "https://news.example", "", "y"]), \
                patch.object(deploy, "run", return_value=result()) as run:
            self.assertEqual(deploy.main(["--interactive"]), 0)
        command = list(map(str, run.call_args.args[0]))
        self.assertEqual(command[:2], ["sudo", "--"])
        forwarded = deploy.parser().parse_args(command[4:])
        self.assertTrue(forwarded.yes)
        self.assertFalse(forwarded.interactive)
        self.assertEqual((forwarded.mode, forwarded.port, forwarded.bind), ("native", 9090, "0.0.0.0"))
        self.assertTrue(forwarded.admin_setup)
        self.assertEqual(forwarded.url, "https://news.example")
        self.assertEqual(list(self.root.iterdir()), [])

    def test_docker_install_retries_and_preserves_host_repositories(self):
        actual_root = Path(__file__).resolve().parent.parent
        plan = {"os_apt": [{"name": "system", "url": "https://os.example/debian", "seconds": 1}],
                "docker_apt": [{"name": "first", "url": "https://first.example/docker/linux/debian", "seconds": 1},
                               {"name": "second", "url": "https://second.example/docker/linux/debian", "seconds": 2}]}
        writes, attempts = [], []
        def fake_run(arguments, **kwargs):
            if arguments[0] == "dpkg":
                return result("amd64")
            if arguments[0] == "apt-get":
                attempts.append(arguments)
                return result(code=1 if len(attempts) == 1 else 0)
            return result()
        original_is_file = Path.is_file
        with patch.object(deploy, "ROOT", actual_root), patch.object(deploy.os, "geteuid", return_value=0, create=True), \
                patch.object(deploy.shutil, "which", return_value=None), patch.object(deploy, "run", side_effect=fake_run), \
                patch.object(Path, "is_file", lambda p: True if "archive-keyring.gpg" in str(p) else original_is_file(p)), \
                patch.object(deploy, "atomic_write", side_effect=lambda p, d, *a: writes.append((p, d))):
            deploy.install_docker({"ID": "debian", "VERSION_CODENAME": "bookworm"}, plan)
        self.assertEqual(len(attempts), 3)
        self.assertIn("install", attempts[-1])
        final = [text for path, text in writes if path.name == "newpress-docker.sources"]
        self.assertIn("https://second.example", final[0])
        self.assertTrue(all(path.name != "sources.list" for path, _ in writes))
        temporary = [text for path, text in writes if path.name == "install.sources"]
        self.assertTrue(all("bookworm-security" in text and "Signed-By:" in text for text in temporary))
        self.assertNotIn("trusted=yes", "".join(temporary))


if __name__ == "__main__":
    unittest.main()
