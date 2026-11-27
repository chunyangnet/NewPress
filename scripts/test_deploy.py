"""Run: python3 -m unittest discover -s scripts -p 'test_deploy*.py' -v."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("newpress_deploy", Path(__file__).with_name("deploy.py"))
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


def result(stdout="", code=0):
    return subprocess.CompletedProcess([], code, stdout, "")


class DefaultThemeImageTests(unittest.TestCase):
    def test_docker_stages_only_chunnel_and_copies_owned_theme_root(self):
        dockerfile = (Path(__file__).resolve().parents[1] / "Dockerfile").read_text(encoding="utf-8")
        copies = []
        for line in dockerfile.splitlines():
            if not line.startswith("COPY "):
                continue
            words = shlex.split(line)
            if words[-1].startswith("/app/themes"):
                copies.append(words[1:])
        self.assertEqual(copies, [["--from=build", "--chown=65532:65532", "/out/themes", "/app/themes"]])
        staging = next(line for line in dockerfile.splitlines()
                       if line.startswith("RUN ") and "/out/themes" in line)
        words = shlex.split(staging)
        self.assertEqual(words[words.index("cp"):], ["cp", "-a", "themes/chunnel", "/out/themes/chunnel"])


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.patch_root = patch.object(deploy, "ROOT", self.root)
        self.patch_root.start()
        self.addCleanup(self.patch_root.stop)
        endpoint = patch.object(deploy, "DOCKER_ENDPOINT", None)
        endpoint.start()
        self.addCleanup(endpoint.stop)

    def test_saved_env_is_literal_and_comments_survive(self):
        path = self.root / ".env"
        path.write_text('# keep this\nNEWPRESS_SITE_TITLE="后台 站点"\nUNCHANGED=123\n', encoding="utf-8")
        deploy.write_env(path, {"NEWPRESS_SITE_TITLE": "后台优先", "PRICE": "$10", "QUOTE": 'A "quote"'}, compose=True)
        values = deploy.read_env(path, compose=True)
        self.assertEqual(values["NEWPRESS_SITE_TITLE"], "后台优先")
        self.assertEqual(values["PRICE"], "$10")
        self.assertEqual(values["QUOTE"], 'A "quote"')
        self.assertEqual(values["UNCHANGED"], "123")
        self.assertIn("# keep this", path.read_text(encoding="utf-8"))
        # Shell syntax is data; configuration is never executed.
        path.write_text("X='`touch should-not-exist`'\n", encoding="utf-8")
        self.assertEqual(deploy.read_env(path)["X"], "`touch should-not-exist`")

    def test_ambiguous_env_is_rejected(self):
        for value in ("X=1\nX=2\n", "source /tmp/config\n", "X=${HOME}\n", "X='unterminated\n"):
            with self.subTest(value=value):
                path = self.root / ".env"
                path.write_text(value, encoding="utf-8")
                with self.assertRaises(deploy.DeployError):
                    deploy.read_env(path, compose=True)

    def test_url_validation(self):
        for value in ("https://example.com/path", "ftp://example.com", "https://u:p@example.com",
                      "https://example.com?x=1", "https://example.com#x", "http://host:0", "http://host:99999"):
            with self.subTest(value=value), self.assertRaises(deploy.DeployError):
                deploy.validate_origin(value)
        self.assertEqual(deploy.validate_origin("https://example.com/"), "https://example.com")
        self.assertEqual(deploy.validate_origin("http://[::1]:8080"), "http://[::1]:8080")

    def test_first_install_requires_public_address(self):
        args = deploy.parser().parse_args([])
        with self.assertRaises(deploy.DeployError):
            deploy.resolve_network({}, args)
        values = {"NEWPRESS_PUBLIC_URL": "https://saved.example", "NEWPRESS_HTTP_PORT": "9090"}
        self.assertEqual(deploy.resolve_network(values, args), ("https://saved.example", 9090, "0.0.0.0"))

    def test_mode_detection_and_storage_switch_protection(self):
        for requested, native, docker, ready, systemd, wanted in (
                ("auto", True, False, True, True, "native"),
                ("auto", False, True, True, True, "docker"),
                ("auto", False, False, True, True, "docker"),
                ("auto", False, False, False, True, "native"),
                ("native", False, False, True, True, "native")):
            self.assertEqual(deploy.select_mode(requested, native, docker, ready, systemd), wanted)
        for arguments in (("docker", True, False, True, True), ("native", False, True, True, True),
                          ("auto", True, True, True, True), ("auto", False, False, False, False)):
            with self.subTest(arguments=arguments), self.assertRaises(deploy.DeployError):
                deploy.select_mode(*arguments)

    def test_restored_volume_without_compose_labels_is_existing_data(self):
        with patch.object(deploy, "docker_ready", return_value=True), \
                patch.object(deploy, "run", side_effect=[result(), result(), result("newpress_postgres_data\n")]):
            self.assertTrue(deploy.docker_existing("newpress"))

    def test_partial_or_lost_secrets_never_generate_replacements(self):
        directory = self.root / "secrets"
        self.assertFalse(deploy.secret_state(directory, deploy.SECRET_NAMES, False))
        with self.assertRaises(deploy.DeployError):
            deploy.secret_state(directory, deploy.SECRET_NAMES, True)
        directory.mkdir()
        (directory / "postgres_password").write_text("original-password")
        with self.assertRaises(deploy.DeployError):
            deploy.secret_state(directory, deploy.SECRET_NAMES, False)
        self.assertEqual((directory / "postgres_password").read_text(), "original-password")

    def test_custom_secrets_cannot_enter_docker_build_context(self):
        self.assertEqual(deploy.docker_secret_directory({}), self.root / ".secrets")
        with self.assertRaises(deploy.DeployError):
            deploy.docker_secret_directory({"NEWPRESS_SECRETS_DIR": "themes/private"})
        outside = self.root.parent / "outside-secrets"
        self.assertEqual(deploy.docker_secret_directory({"NEWPRESS_SECRETS_DIR": str(outside)}), outside)

    def test_detects_busy_port(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            self.assertFalse(deploy.port_available("127.0.0.1", listener.getsockname()[1]))

    def test_native_requires_unprivileged_port_and_fixed_data_location(self):
        with self.assertRaises(deploy.DeployError):
            deploy.native_values({}, 80, "0.0.0.0")
        with self.assertRaises(deploy.DeployError):
            deploy.native_values({"NEWPRESS_DATA_DIR": "/other/site"}, 8080, "0.0.0.0")
        values = deploy.native_values({}, 9090, "127.0.0.1")
        self.assertEqual(values["NEWPRESS_ADDR"], "127.0.0.1:9090")
        self.assertEqual(values["NEWPRESS_TRUSTED_PROXY_CIDRS"], "")
        self.assertEqual(values["NEWPRESS_SEED_DEMO"] if "NEWPRESS_SEED_DEMO" in values else "false", "false")

    def test_elf_architecture_validation(self):
        path = self.root / "newpress"
        for arch, machine in (("amd64", 62), ("arm64", 183)):
            header = b"\x7fELF\x02\x01" + bytes(12) + machine.to_bytes(2, "little")
            path.write_bytes(header)
            deploy.validate_binary(path, arch)
            with self.assertRaises(deploy.DeployError):
                deploy.validate_binary(path, "arm64" if arch == "amd64" else "amd64")
        path.write_bytes(b"MZ Windows executable")
        with self.assertRaises(deploy.DeployError):
            deploy.validate_binary(path, "amd64")

    def test_tar_rejects_traversal_and_symlinks(self):
        for name, kind in (("../outside", tarfile.REGTYPE), ("go/link", tarfile.SYMTYPE)):
            with self.subTest(name=name):
                archive = self.root / "go.tar.gz"
                with tarfile.open(archive, "w:gz") as stream:
                    entry = tarfile.TarInfo(name)
                    entry.type = kind
                    entry.linkname = "/etc/passwd" if kind == tarfile.SYMTYPE else ""
                    stream.addfile(entry)
                with self.assertRaises(deploy.DeployError):
                    deploy.extract_toolchain(archive, self.root / "toolchain")

    def test_remote_docker_is_rejected(self):
        with patch.dict(os.environ, {"DOCKER_HOST": "ssh://remote"}):
            with self.assertRaises(deploy.DeployError):
                deploy.inspect_local_docker()

    def test_shell_variables_do_not_override_saved_compose_config(self):
        with patch.dict(os.environ, {"NEWPRESS_PUBLIC_URL": "http://wrong", "COMPOSE_FILE": "wrong.yaml"}):
            self.assertNotIn("NEWPRESS_PUBLIC_URL", deploy.docker_env())
            self.assertNotIn("COMPOSE_FILE", deploy.docker_env())

    def test_existing_network_is_reused(self):
        network = {"IPAM": {"Config": [{"Subnet": "172.25.8.0/24"}]}, "Labels": {
            "com.docker.compose.project": "newpress", "com.docker.compose.network": "edge"}}
        with patch.object(deploy, "run", side_effect=[result("network1"), result(json.dumps([network]))]):
            values = {}
            deploy.choose_subnet(values)
        self.assertEqual(values["NEWPRESS_EDGE_SUBNET"], "172.25.8.0/24")
        self.assertEqual(values["NEWPRESS_TRUSTED_PROXY_CIDRS"], "172.25.8.2/32")

    def test_new_network_avoids_existing_docker_subnets(self):
        network = {"IPAM": {"Config": [{"Subnet": "172.30.30.0/24"}]}}
        original_exists = Path.exists

        def exists(path):
            return False if path.as_posix() == "/proc/net/route" else original_exists(path)

        with patch.object(deploy, "run", side_effect=[result("network1"), result(json.dumps([network]))]), \
                patch.object(Path, "exists", exists):
            values = {}
            deploy.choose_subnet(values)
        self.assertNotEqual(values["NEWPRESS_EDGE_SUBNET"], "172.30.30.0/24")

    def test_docker_deployment_and_rerun_preserve_credentials(self):
        (self.root / "compose.yaml").write_text("services: {}\n")
        calls = []

        def fake_run(arguments, **kwargs):
            args = list(map(str, arguments))
            calls.append(args)
            if args[:4] == ["docker", "compose", "version", "--short"]:
                return result("2.30.0")
            if args[:3] == ["docker", "context", "inspect"]:
                return result("unix:///var/run/docker.sock")
            if args[:2] == ["docker", "run"]:
                self.assertNotIn(kwargs["input"].strip(), " ".join(args))
                return result("$argon2id$generated-test-hash\n")
            return result()

        values = {"NEWPRESS_PUBLIC_URL": "http://localhost:8080", "NEWPRESS_SITE_TITLE": "初始站点",
                  "NEWPRESS_EDGE_SUBNET": "172.25.8.0/24"}
        args = deploy.parser().parse_args([])
        args.source_plan = {"goproxy": [{"url": "https://proxy.golang.org"}], "images": {
            name: [{"name": "fixture", "url": name + "@sha256:" + "a" * 64, "cached": True}]
            for name in ("go", "runtime", "postgres", "proxy")}}
        with patch.object(deploy, "run", side_effect=fake_run), patch.object(deploy, "wait_http"), \
                patch.object(deploy, "port_available", return_value=True), contextlib.redirect_stdout(io.StringIO()) as output:
            login = deploy.deploy_docker(values, args, False, 8080, "0.0.0.0", "amd64")
            original = {path.name: path.read_bytes() for path in login.parent.iterdir()}
            deploy.deploy_docker(values, args, True, 8080, "0.0.0.0", "amd64")
            self.assertEqual(original, {path.name: path.read_bytes() for path in login.parent.iterdir()})
            self.assertNotIn("generated-test-hash", output.getvalue())
            self.assertNotIn(login.read_text(encoding="utf-8").splitlines()[2], output.getvalue())
        self.assertEqual(sum(call[:2] == ["docker", "run"] for call in calls), 1)
        self.assertTrue(any("--wait" in call for call in calls))
        self.assertFalse(any("down" in call or "--volumes" in call for call in calls))
        self.assertEqual(deploy.read_env(self.root / ".env", compose=True)["NEWPRESS_SITE_TITLE"], "初始站点")

    def test_failed_readiness_is_not_success(self):
        opener = types.SimpleNamespace(open=lambda *a, **k: (_ for _ in ()).throw(OSError("not ready")))
        with patch.object(deploy.urllib.request, "build_opener", return_value=opener), \
                patch.object(deploy.time, "monotonic", side_effect=[0, 0, 3]), patch.object(deploy.time, "sleep"):
            with self.assertRaises(deploy.DeployError):
                deploy.wait_http("http://127.0.0.1:8080/health/ready", 2)

    def test_check_mode_does_not_create_files_or_start_services(self):
        original_is_dir = Path.is_dir

        def is_dir(path):
            return True if path.as_posix() == "/run/systemd/system" else original_is_dir(path)

        with contextlib.ExitStack() as stack:
            for key, value in (("NATIVE_ENV", self.root / "etc/newpress.env"),
                               ("NATIVE_DATA", self.root / "data"), ("UNIT", self.root / "newpress.service")):
                stack.enter_context(patch.object(deploy, key, value))
            stack.enter_context(patch.object(deploy.platform, "system", return_value="Linux"))
            stack.enter_context(patch.object(deploy.platform, "machine", return_value="x86_64"))
            stack.enter_context(patch.object(deploy, "docker_ready", return_value=False))
            stack.enter_context(patch.object(deploy.shutil, "which", side_effect=lambda name: "systemctl" if name == "systemctl" else None))
            stack.enter_context(patch.object(Path, "is_dir", is_dir))
            stack.enter_context(patch.object(deploy, "port_available", return_value=True))
            stack.enter_context(patch.object(deploy, "prepare_sources", return_value={}))
            commands = stack.enter_context(patch.object(deploy, "run"))
            writes = stack.enter_context(patch.object(deploy, "atomic_write"))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            self.assertEqual(deploy.main(["--mode", "native", "--check", "--url", "https://example.test"]), 0)
            commands.assert_not_called()
            writes.assert_not_called()
        self.assertEqual(list(self.root.iterdir()), [])

    def test_native_install_and_rerun_preserve_site_and_extensions(self):
        env_path = self.root / "etc/newpress.env"
        data = self.root / "data"
        binary_path = self.root / "bin/newpress"
        unit = self.root / "units/newpress.service"
        supplied_binary = self.root / "supplied"
        supplied_binary.write_bytes(b"\x7fELF\x02\x01" + bytes(12) + (62).to_bytes(2, "little"))
        for name in ("themes", "plugins", "deploy"):
            (self.root / name).mkdir()
        (self.root / "themes/chunnel").mkdir()
        (self.root / "themes/chunnel/theme.txt").write_text("original")
        for name in ("starter", "starter-child", "docshub", "nexora", "future-theme"):
            (self.root / "themes" / name).mkdir()
            (self.root / "themes" / name / "theme.json").write_text("{}")
        (self.root / "themes/.active-theme.json").write_text('{"schema":1,"slug":"starter"}')
        (self.root / "deploy/newpress.service").write_text(deploy.MANAGED + "\n[Service]\n")
        account = types.SimpleNamespace(pw_uid=999, pw_gid=999)
        pwd = types.SimpleNamespace(getpwnam=lambda name: account)
        calls = []

        def fake_run(arguments, **kwargs):
            args = list(map(str, arguments))
            calls.append(args)
            return result("$argon2id$native-test-hash\n" if args[-2:] == ["admin", "hash-password"] else "version")

        original_is_dir = Path.is_dir

        def is_dir(path):
            return True if path.as_posix() == "/run/systemd/system" else original_is_dir(path)

        with contextlib.ExitStack() as stack:
            for key, value in (("NATIVE_ENV", env_path), ("NATIVE_DATA", data), ("NATIVE_BIN", binary_path), ("UNIT", unit)):
                stack.enter_context(patch.object(deploy, key, value))
            stack.enter_context(patch.dict(sys.modules, {"pwd": pwd}))
            stack.enter_context(patch.object(deploy.os, "geteuid", return_value=0, create=True))
            stack.enter_context(patch.object(deploy.os, "chown", create=True))
            stack.enter_context(patch.object(deploy.shutil, "which", return_value="systemctl"))
            stack.enter_context(patch.object(Path, "is_dir", is_dir))
            stack.enter_context(patch.object(deploy, "port_available", return_value=True))
            stack.enter_context(patch.object(deploy, "run", side_effect=fake_run))
            stack.enter_context(patch.object(deploy, "wait_http"))
            args = deploy.parser().parse_args(["--binary", str(supplied_binary)])
            values = {"NEWPRESS_PUBLIC_URL": "https://example.test"}
            login = deploy.deploy_native(values, args, False, 8080, "0.0.0.0", "amd64")
            self.assertEqual([path.name for path in (data / "themes").iterdir()], ["chunnel"])
            self.assertEqual((data / "themes/chunnel/theme.txt").read_text(), "original")
            first_login = login.read_bytes()
            (data / "data/site-settings.json").write_text("saved admin settings")
            (data / "themes/custom").mkdir()
            (data / "themes/custom/theme.txt").write_text("installed custom theme")
            (data / "themes/.active-theme.json").write_text('{"schema":1,"slug":"custom"}')
            deploy.deploy_native(deploy.read_env(env_path), args, True, 8080, "0.0.0.0", "amd64")
            self.assertEqual(login.read_bytes(), first_login)
            current_binary = binary_path.read_bytes()
            supplied_binary.write_bytes(current_binary + b"new candidate")
            with patch.object(deploy, "port_available", return_value=False), self.assertRaises(deploy.DeployError):
                deploy.deploy_native(deploy.read_env(env_path), args, True, 8080, "0.0.0.0", "amd64")
            self.assertEqual(binary_path.read_bytes(), current_binary)
        self.assertEqual((data / "data/site-settings.json").read_text(), "saved admin settings")
        self.assertEqual((data / "themes/custom/theme.txt").read_text(), "installed custom theme")
        self.assertEqual(json.loads((data / "themes/.active-theme.json").read_text())["slug"], "custom")
        self.assertEqual(binary_path.with_suffix(".previous").read_bytes(), current_binary)
        self.assertEqual(sum(call[-2:] == ["admin", "hash-password"] for call in calls), 1)
        self.assertTrue(any(call == ["systemctl", "enable", "--now", "newpress"] for call in calls))


if __name__ == "__main__":
    unittest.main()
