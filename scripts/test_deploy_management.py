"""Regression coverage for deployment safety and the CLI/menu management flow."""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

from test_deploy import deploy, result


class ManagementTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        for key, value in (("ROOT", self.root), ("DOCKER_ENDPOINT", None),
                           ("NATIVE_ENV", self.root / "etc/newpress.env"),
                           ("NATIVE_DATA", self.root / "native"),
                           ("NATIVE_BIN", self.root / "bin/newpress"),
                           ("UNIT", self.root / "units/newpress.service")):
            self.stack.enter_context(patch.object(deploy, key, value))
        self.stack.enter_context(patch.dict(os.environ, {}, clear=True))
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.stack.enter_context(patch.object(deploy.platform, "system", return_value="Linux"))
        self.stack.enter_context(patch.object(deploy.os, "geteuid", return_value=0, create=True))
        self.base = self.root / "compose.yaml"
        self.base.write_text("services: {}\n")

    def overlay(self, name):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("services: {}\n")
        return path

    def live_files(self, lists):
        containers = [{"Config": {"Labels": {"com.docker.compose.project.config_files":
                       ",".join(str(path) for path in paths)}}} for paths in lists]
        return patch.object(deploy, "run", side_effect=[result("container1\n"), result(json.dumps(containers))])

    def docker_management(self):
        self.stack.enter_context(patch.object(deploy, "docker_ready", return_value=True))
        self.stack.enter_context(patch.object(deploy, "docker_existing", return_value=True))
        values = {"NEWPRESS_PUBLIC_URL": "http://localhost:8080", "NEWPRESS_HTTP_PORT": "8080"}
        deploy.write_env(self.root / ".env", values, compose=True)
        return self.stack.enter_context(patch.object(deploy, "run", return_value=result()))

    def native_management(self):
        self.stack.enter_context(patch.object(deploy, "docker_ready", return_value=False))
        self.stack.enter_context(patch.object(deploy.shutil, "which", side_effect=lambda name:
                                             name if name in ("systemctl", "sudo") else None))
        original = Path.is_dir
        self.stack.enter_context(patch.object(Path, "is_dir", lambda path:
            True if path.as_posix() == "/run/systemd/system" else original(path)))
        deploy.UNIT.parent.mkdir(parents=True)
        deploy.UNIT.write_text(deploy.MANAGED + "\n[Service]\n")
        return self.stack.enter_context(patch.object(deploy, "run", return_value=result()))

    def test_remote_context_beats_local_host_and_is_rejected_before_info(self):
        with patch.dict(os.environ, {"DOCKER_HOST": "unix:///var/run/docker.sock", "DOCKER_CONTEXT": "remote"}), \
                patch.object(deploy.shutil, "which", return_value="docker"), \
                patch.object(deploy.subprocess, "run", return_value=result("ssh://remote")) as child:
            with self.assertRaisesRegex(deploy.DeployError, "本机 Docker"):
                deploy.docker_ready()
        self.assertEqual(child.call_count, 1)
        self.assertEqual(child.call_args.args[0][:4], ["docker", "context", "inspect", "remote"])
        self.assertIsNone(deploy.DOCKER_ENDPOINT)

    def test_validated_context_pins_all_children_and_ignores_later_override(self):
        environment = {"DOCKER_CONTEXT": "local", "DOCKER_HOST": "ssh://unused",
                       "BUILDX_BUILDER": "remote", "BUILDKIT_HOST": "tcp://remote:1234"}
        with patch.dict(os.environ, environment), patch.object(deploy.subprocess, "run", side_effect=[
                result("unix:///run/user/1000/docker.sock"), result(), result()]) as child:
            deploy.inspect_local_docker()
            os.environ["DOCKER_CONTEXT"] = "changed"
            deploy.run(["docker", "info"])
            deploy.run(["docker", "build", "."])
        for call in child.call_args_list[1:]:
            env = call.kwargs["env"]
            self.assertEqual(env["DOCKER_HOST"], "unix:///run/user/1000/docker.sock")
            self.assertNotIn("DOCKER_CONTEXT", env)
            self.assertNotIn("BUILDKIT_HOST", env)
            self.assertEqual(env["BUILDX_BUILDER"], "default")

    def test_nonlocal_and_malformed_endpoints_fail(self):
        for endpoint in ("ssh://host", "tcp://127.0.0.1:2375", "unix://host/a", "unix:relative",
                         "unix:///run/docker.sock?host=remote", "unix:///run/docker.sock\n"):
            with self.subTest(endpoint=endpoint), patch.dict(os.environ, {"DOCKER_HOST": endpoint}), \
                    self.assertRaises(deploy.DeployError):
                deploy.inspect_local_docker()

    def test_each_dispatch_resets_endpoint(self):
        with patch.object(deploy, "manage_command", return_value=0) as action:
            deploy.DOCKER_ENDPOINT = "unix:///old.sock"
            deploy.dispatch(deploy.parser().parse_args(["status"]))
        self.assertIsNone(deploy.DOCKER_ENDPOINT)
        action.assert_called_once()

    def test_compose_live_superset_survives_stale_saved_base_and_unchanged_db(self):
        mail = self.overlay("deploy/compose.mail.yaml")
        updates = self.overlay("deploy/compose.updates-secret.yaml")
        values = {"COMPOSE_FILE": str(self.base), "COMPOSE_PATH_SEPARATOR": ";"}
        with self.live_files([[self.base], [self.base, mail], [self.base, mail, updates]]):
            selected = deploy.select_compose_files(values, discover=True)
        self.assertEqual(selected, [self.base, mail, updates])
        deploy.write_env(self.root / ".env", values, compose=True)
        values = deploy.read_env(self.root / ".env", compose=True)
        with patch.object(deploy, "run", return_value=result()) as command, \
                patch.dict(os.environ, {"COMPOSE_FILE": "wrong.yaml"}):
            deploy.compose(values, "ps")
        args = list(map(str, command.call_args.args[0]))
        self.assertEqual([args[i + 1] for i, value in enumerate(args) if value == "-f"], list(map(str, selected)))
        self.assertNotIn("COMPOSE_FILE", command.call_args.kwargs["env"])

    def test_divergent_overlay_order_is_rejected_until_explicitly_selected(self):
        a, b = self.overlay("a.yaml"), self.overlay("b.yaml")
        for lists in ([[self.base, a], [self.base, b]], [[self.base, a, b], [self.base, b, a]]):
            with self.subTest(lists=lists), self.live_files(lists), self.assertRaisesRegex(deploy.DeployError, "不一致"):
                deploy.select_compose_files({}, discover=True)
        with patch.object(deploy, "run") as run:
            self.assertEqual(deploy.select_compose_files({}, ["b.yaml", "a.yaml"], discover=True), [self.base, b, a])
            self.assertEqual(deploy.select_compose_files({}, ["compose.yaml"], discover=True), [self.base])
        run.assert_not_called()

    def test_missing_or_foreign_compose_files_stop_before_mutation(self):
        for names in (["missing.yaml"], [str(self.overlay("other.yaml"))], ["compose.yaml", "compose.yaml"]):
            with self.subTest(names=names), self.assertRaises(deploy.DeployError):
                deploy.compose_paths(names)
        with self.live_files([[]]), self.assertRaises(deploy.DeployError):
            deploy.select_compose_files({}, discover=True)

    def test_saved_image_pins_override_repository_defaults_and_remain_validated(self):
        defaults = {name: f"{name}@sha256:" + "a" * 64 for name in deploy.IMAGE_KEYS}
        values = {key: f"custom.example/{name}@sha256:" + "b" * 64 for name, key in deploy.IMAGE_KEYS.items()}
        args = deploy.parser().parse_args(["--source-mode", "auto"])
        deploy.configure_sources(values, args)
        with patch.object(deploy.sources, "pinned_images", return_value=defaults), \
                patch.object(deploy.sources, "Selector") as selector, patch.object(deploy, "cached_image", return_value=False):
            selector.return_value.go_modules.return_value = []
            selector.return_value.image.side_effect = lambda label, ref, arch, extra: [{"url": ref}]
            plan = deploy.prepare_sources(args, {}, "docker", True, "amd64", values)
        for name, choices in plan["images"].items():
            self.assertEqual(choices[0]["url"], values[deploy.IMAGE_KEYS[name]])
        self.assertEqual(selector.return_value.image.call_count, 4)

    def test_mirror_selection_retains_origin_and_operator_edit_takes_precedence(self):
        original = "postgres@sha256:" + "c" * 64
        mirrored = "mirror.example/library/postgres@sha256:" + "c" * 64
        key = deploy.IMAGE_KEYS["postgres"]
        values = {key: mirrored, key + "_ORIGIN": original, key + "_SELECTED": mirrored}
        args = deploy.parser().parse_args(["--source-mode", "official"])
        deploy.configure_sources(values, args)
        with patch.object(deploy.sources, "pinned_images", return_value={"postgres": "postgres@sha256:" + "a" * 64}), \
                patch.object(deploy.sources, "Selector") as selector, patch.object(deploy, "cached_image", return_value=False):
            deploy.prepare_sources(args, {}, "docker", True, "amd64", values)
            self.assertEqual(selector.return_value.image.call_args.args[1], original)
            values[key] = "patched.example/postgres@sha256:" + "d" * 64
            deploy.prepare_sources(args, {}, "docker", True, "amd64", values)
            self.assertEqual(selector.return_value.image.call_args.args[1], values[key])

    def test_invalid_image_reference_is_never_accepted(self):
        for ref in ("postgres:latest", "-option@sha256:" + "a" * 64, "evil.example/../secret@sha256:" + "a" * 64,
                    "https://example.com/postgres@sha256:" + "a" * 64):
            with self.subTest(ref=ref), self.assertRaises(deploy.sources.SourceError):
                deploy.sources.image_parts(ref)

    def test_empty_native_data_is_retryable_but_real_site_needs_original_secret(self):
        data = deploy.NATIVE_DATA / "data"
        data.mkdir(parents=True)
        self.assertFalse(deploy.native_existing())
        self.assertFalse(deploy.secret_state(deploy.NATIVE_ENV.parent / "secrets", ("admin_password_hash",), False))
        (data / "site-settings.json").write_text("{}")
        self.assertTrue(deploy.native_existing())
        with self.assertRaises(deploy.DeployError):
            deploy.secret_state(deploy.NATIVE_ENV.parent / "secrets", ("admin_password_hash",), deploy.native_existing())

    def test_existing_env_or_unit_preserves_existing_site_protection(self):
        for path in (deploy.NATIVE_ENV, deploy.UNIT):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("# existing")
            self.assertTrue(deploy.native_existing())
            path.unlink()

    def test_empty_admin_hash_is_removed_from_existing_file(self):
        path = self.root / "native.env"
        path.write_text('# keep\nNEWPRESS_ADMIN_PASSWORD_HASH=""\nNEWPRESS_SITE_TITLE=Saved\n', encoding="utf-8")
        values = deploy.native_values(deploy.read_env(path), 8080, "0.0.0.0")
        deploy.write_env(path, values, remove=("NEWPRESS_ADMIN_PASSWORD_HASH",))
        saved = deploy.read_env(path)
        self.assertNotIn("NEWPRESS_ADMIN_PASSWORD_HASH", saved)
        self.assertIn("NEWPRESS_ADMIN_PASSWORD_HASH_FILE", saved)
        self.assertEqual(saved["NEWPRESS_SITE_TITLE"], "Saved")
        self.assertTrue(path.read_text().startswith("# keep"))
        with self.assertRaises(deploy.DeployError):
            deploy.native_values({"NEWPRESS_ADMIN_PASSWORD_HASH": "$argon2id$existing"}, 8080, "0.0.0.0")

    def test_extension_copy_failure_leaves_no_partial_install_and_can_retry(self):
        source, target = self.root / "source", self.root / "installed"
        source.mkdir()
        (source / "theme.txt").write_text("complete")
        account = types.SimpleNamespace(pw_uid=123, pw_gid=123)
        def failed_copy(src, dst):
            dst.mkdir()
            (dst / "partial").write_text("partial")
            raise OSError("interrupted copy")
        with patch.object(deploy, "copy_extension_tree", side_effect=failed_copy), self.assertRaises(OSError):
            deploy.install_extension(source, target, account)
        self.assertFalse(target.exists())
        self.assertEqual(list(self.root.glob(".newpress-extension-*")), [])
        with patch.object(deploy.os, "chown", create=True):
            deploy.install_extension(source, target, account)
        self.assertEqual((target / "theme.txt").read_text(), "complete")
        self.assertFalse((target / "partial").exists())

    def test_interrupted_native_install_can_complete_on_retry(self):
        self.native_management()
        deploy.UNIT.unlink()
        account = types.SimpleNamespace(pw_uid=999, pw_gid=999)
        self.stack.enter_context(patch.dict(sys.modules, {"pwd": types.SimpleNamespace(getpwnam=lambda name: account)}))
        chown = self.stack.enter_context(patch.object(deploy.os, "chown", create=True))
        self.stack.enter_context(patch.object(deploy, "port_available", return_value=True))
        self.stack.enter_context(patch.object(deploy, "wait_http"))
        self.stack.enter_context(patch.object(deploy, "run", side_effect=lambda args, **kwargs:
            result("$argon2id$retry-hash" if list(map(str, args))[-2:] == ["admin", "hash-password"] else "version")))
        for name in ("themes", "plugins", "deploy"):
            (self.root / name).mkdir()
        (self.root / "themes/chunnel").mkdir()
        (self.root / "themes/chunnel/theme.txt").write_text("complete theme")
        (self.root / "deploy/newpress.service").write_text(deploy.MANAGED + "\n[Service]\n")
        supplied = self.root / "candidate"
        supplied.write_bytes(b"\x7fELF\x02\x01" + bytes(12) + (62).to_bytes(2, "little"))
        args = deploy.parser().parse_args(["--binary", str(supplied)])
        values = {"NEWPRESS_PUBLIC_URL": "http://localhost:8080", "NEWPRESS_ADMIN_PASSWORD_HASH": ""}
        with patch.object(deploy, "copy_extension_tree", side_effect=OSError("disk interrupted")), self.assertRaises(OSError):
            deploy.deploy_native(values, args, False, 8080, "0.0.0.0", "amd64")
        self.assertFalse(deploy.native_existing())
        self.assertFalse((deploy.NATIVE_DATA / "themes").exists())
        login = deploy.deploy_native(values, args, deploy.native_existing(), 8080, "0.0.0.0", "amd64")
        self.assertTrue(login.is_file())
        self.assertEqual((deploy.NATIVE_DATA / "themes/chunnel/theme.txt").read_text(), "complete theme")
        self.assertEqual([path.name for path in (deploy.NATIVE_DATA / "themes").iterdir()], ["chunnel"])
        self.assertNotIn("NEWPRESS_ADMIN_PASSWORD_HASH", deploy.read_env(deploy.NATIVE_ENV))
        self.assertIn((deploy.NATIVE_DATA, 0, 999), [call.args for call in chown.call_args_list])

    def test_busy_mutation_lock_prevents_service_commands(self):
        self.docker_management()
        fcntl = types.SimpleNamespace(LOCK_EX=2, LOCK_NB=4,
            flock=lambda *args: (_ for _ in ()).throw(BlockingIOError()))
        with patch.dict(sys.modules, {"fcntl": fcntl}), patch.object(deploy, "compose") as command, \
                self.assertRaisesRegex(deploy.DeployError, "正在运行"):
            deploy.main(["stop", "--yes"])
        command.assert_not_called()

    @unittest.skipUnless(os.name == "posix", "requires Linux no-follow directory descriptors")
    def test_native_copy_rejects_links_and_special_files_without_reading_target(self):
        source = self.root / "source"
        source.mkdir()
        outside = self.root / "private"
        outside.write_text("must not be copied")
        (source / "link").symlink_to(outside)
        with self.assertRaises((deploy.DeployError, OSError)):
            deploy.copy_extension_tree(source, self.root / "copied")
        self.assertFalse((self.root / "copied/link").exists())
        (source / "link").unlink()
        os.mkfifo(source / "fifo")
        with self.assertRaises(deploy.DeployError):
            deploy.copy_extension_tree(source, self.root / "copied2")

    def test_status_and_logs_do_not_probe_install_or_write(self):
        self.docker_management()
        with patch.object(deploy, "prepare_sources") as sources, patch.object(deploy, "atomic_write") as writes, \
                patch.object(deploy, "deploy_docker") as install, patch.object(deploy, "compose", return_value=result()) as command:
            self.assertEqual(deploy.main(["status"]), 0)
            self.assertEqual(deploy.main(["logs", "--tail", "42", "--follow"]), 0)
        self.assertEqual(command.call_args_list[0].args[1:], ("ps", "--all"))
        self.assertEqual(command.call_args_list[1].args[1:], ("logs", "--tail", "42", "--follow"))
        sources.assert_not_called()
        writes.assert_not_called()
        install.assert_not_called()

    def test_restart_and_stop_only_touch_site_and_restart_waits_for_ready(self):
        self.docker_management()
        with patch.object(deploy, "deployment_lock", side_effect=contextlib.nullcontext), \
                patch.object(deploy, "compose", return_value=result()) as command, patch.object(deploy, "wait_http") as ready:
            self.assertEqual(deploy.main(["restart", "--yes"]), 0)
            self.assertEqual(deploy.main(["stop", "--yes"]), 0)
        self.assertEqual(command.call_args_list[0].args[1:], ("restart", "--no-deps", "app", "proxy"))
        self.assertEqual(command.call_args_list[1].args[1:], ("stop", "proxy", "app"))
        ready.assert_called_once_with("http://127.0.0.1:8080/health/ready", 180)

    def test_stop_requires_yes_or_confirmation_and_cancel_does_nothing(self):
        self.docker_management()
        with patch.object(deploy.sys.stdin, "isatty", return_value=False), self.assertRaisesRegex(deploy.DeployError, "--yes"):
            deploy.main(["stop"])
        with patch("builtins.input", return_value="n"), patch.object(deploy, "compose") as command:
            self.assertEqual(deploy.main(["stop", "--interactive"]), 0)
        command.assert_not_called()

    def test_native_logs_sudo_retains_action_and_options(self):
        run = self.native_management()
        with patch.object(deploy.os, "geteuid", return_value=1000, create=True):
            self.assertEqual(deploy.main(["logs", "--tail", "37", "--follow"]), 0)
        command = list(map(str, run.call_args.args[0]))
        self.assertEqual(command[:2], ["sudo", "--"])
        selected = deploy.parser().parse_args(command[4:])
        self.assertEqual((selected.command, selected.mode, selected.tail, selected.follow), ("logs", "native", 37, True))
        self.assertFalse(selected.menu)

    def test_native_stop_sudo_does_not_repeat_confirmation(self):
        run = self.native_management()
        with patch.object(deploy.os, "geteuid", return_value=1000, create=True), patch("builtins.input", return_value="y"):
            deploy.main(["stop", "--interactive"])
        selected = deploy.parser().parse_args(list(map(str, run.call_args.args[0]))[4:])
        self.assertTrue(selected.yes)
        self.assertFalse(selected.interactive)
        self.assertEqual(selected.command, "stop")

    def test_unmanaged_native_unit_is_not_operated(self):
        run = self.native_management()
        deploy.UNIT.write_text("[Service]\nExecStart=/custom\n")
        with self.assertRaisesRegex(deploy.DeployError, "脚本管理"):
            deploy.main(["stop", "--yes"])
        run.assert_not_called()

    def test_native_commands_target_only_managed_unit(self):
        run = self.native_management()
        with patch.object(deploy, "deployment_lock", side_effect=contextlib.nullcontext):
            deploy.main(["status"])
            deploy.main(["logs", "--tail", "5"])
            deploy.main(["stop", "--yes"])
        self.assertEqual(run.call_args_list[0].args[0], ["systemctl", "status", "newpress.service", "--no-pager", "--full"])
        self.assertEqual(run.call_args_list[1].args[0], ["journalctl", "-u", "newpress.service", "-n", "5", "--no-pager"])
        self.assertEqual(run.call_args_list[2].args[0], ["systemctl", "stop", "newpress.service"])

    def test_menu_routes_all_actions_recovers_errors_and_keeps_original_options(self):
        actions = []
        def action(args):
            actions.append(args.command)
            self.assertTrue(args.interactive)
            args.interactive = False
            if args.command == "check":
                raise deploy.DeployError("fixture failure")
            return 0
        with patch("builtins.input", side_effect=["bad", "1", "2", "3", "4", "5", "6", "7", "0"]), \
                patch.object(deploy, "dispatch", side_effect=action):
            self.assertEqual(deploy.main(["--menu", "--interactive"]), 0)
        self.assertEqual(actions, ["deploy", "check", "status", "logs", "restart", "stop", "admin"])

    def test_no_arguments_open_menu_only_with_terminal(self):
        with patch.object(deploy.sys.stdin, "isatty", return_value=True), \
                patch.object(deploy.sys.stdout, "isatty", return_value=True), patch("builtins.input", return_value="0"), \
                patch.object(deploy, "dispatch") as action:
            self.assertEqual(deploy.main([]), 0)
        action.assert_not_called()

    def test_menu_eof_interrupt_and_nonterminal_never_deploy(self):
        with patch.object(deploy, "dispatch") as action:
            for exception in (EOFError, KeyboardInterrupt):
                with patch("builtins.input", side_effect=exception):
                    self.assertEqual(deploy.main(["menu", "--interactive"]), 0)
            with patch.object(deploy.sys.stdin, "isatty", return_value=False), self.assertRaises(deploy.DeployError):
                deploy.main(["--menu"])
            with self.assertRaises(deploy.DeployError):
                deploy.main(["--menu", "--yes"])
        action.assert_not_called()

    def test_direct_check_and_legacy_flags_dispatch_consistently(self):
        with patch.object(deploy, "deploy_command", return_value=0) as action:
            deploy.main(["check"])
            self.assertTrue(action.call_args.args[0].check)
            self.assertFalse(action.call_args.args[0].interactive)
            deploy.main(["--check"])
            self.assertTrue(action.call_args.args[0].check)
            deploy.main(["--yes", "--mode", "docker"])
            self.assertEqual(action.call_args.args[0].command, "deploy")

    def test_fresh_check_with_binary_needs_no_url_or_input_and_writes_nothing(self):
        run = self.native_management()
        deploy.UNIT.unlink()
        candidate = self.root / "candidate"
        candidate.write_bytes(b"\x7fELF\x02\x01" + bytes(12) + (62).to_bytes(2, "little"))
        with patch.object(deploy.platform, "machine", return_value="x86_64"), \
                patch.object(deploy, "port_available", return_value=True), \
                patch.object(deploy, "atomic_write") as writes, patch("builtins.input") as prompt, \
                patch.object(deploy.sources, "Selector") as selector:
            self.assertEqual(deploy.main(["check", "--mode", "native", "--binary", str(candidate)]), 0)
        run.assert_not_called()
        writes.assert_not_called()
        prompt.assert_not_called()
        self.assertEqual(selector.return_value.method_calls, [])

    def test_invalid_command_combinations_and_tail_fail_without_actions(self):
        with patch.object(deploy, "dispatch") as action:
            for arguments in (["status", "--check"], ["stop", "--menu"], ["logs", "--tail", "0"],
                              ["logs", "--tail", "10001"], ["--menu", "--check"]):
                with self.subTest(arguments=arguments), self.assertRaises(deploy.DeployError):
                    deploy.main(arguments)
        action.assert_not_called()

    def test_preview_never_uses_deployment_or_management(self):
        with patch.object(deploy, "dispatch") as action, patch.object(deploy, "run") as run:
            self.assertEqual(deploy.main(["--preview"]), 0)
        action.assert_not_called()
        run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
