"""Account recovery, secret transport, and manual proxy configuration regressions."""
import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

from test_deploy import deploy, result


class AdminTests(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        for key, value in (("ROOT", self.root), ("NATIVE_ENV", self.root / "etc/newpress.env"),
                           ("NATIVE_DATA", self.root / "data"), ("NATIVE_BIN", self.root / "bin/newpress")):
            self.stack.enter_context(patch.object(deploy, key, value))
        self.output = io.StringIO()
        self.stack.enter_context(contextlib.redirect_stdout(self.output))
        self.stack.enter_context(patch.object(deploy.platform, "system", return_value="Linux"))
        self.stack.enter_context(patch.object(deploy.os, "geteuid", return_value=0, create=True))
        self.stack.enter_context(patch.object(deploy, "deployment_lock", side_effect=contextlib.nullcontext))
        self.values = {"NEWPRESS_PUBLIC_URL": "http://localhost:8080", "NEWPRESS_HTTP_PORT": "8080", "NEWPRESS_HTTP_BIND": "127.0.0.1"}
        self.accounts = [{"id": 7, "username": "owner", "status": "active", "version": 4}]
        self.password = "private replacement password!"

    def stdin(self, value):
        stream = io.TextIOWrapper(io.BytesIO(value), encoding="utf-8")
        self.addCleanup(stream.close)
        return patch.object(deploy.sys, "stdin", stream)

    def test_password_stdin_is_bounded_preserves_spaces_and_never_prints(self):
        args = deploy.parser().parse_args(["--password-stdin", "--yes"])
        with self.stdin(b"  secret password with spaces  \r\n"):
            self.assertEqual(deploy.read_password(args), ("  secret password with spaces  ", False))
        for value in (b"short", b"long password\nsecond line", b"x" * 1025, b"x" * 2048, b"password with\x00secret", b"\xff" * 15):
            with self.subTest(value=len(value)), self.stdin(value), self.assertRaises(deploy.DeployError):
                deploy.read_password(args)
        self.assertNotIn("secret password", self.output.getvalue())

    def test_hidden_input_confirms_and_never_falls_back_to_echo(self):
        args = deploy.parser().parse_args([])
        with patch.object(deploy.sys.stdin, "isatty", return_value=True), patch.object(deploy, "password_prompt", side_effect=[self.password, self.password]):
            self.assertEqual(deploy.read_password(args), (self.password, False))
        with patch.object(deploy.sys.stdin, "isatty", return_value=True), patch.object(deploy, "password_prompt", side_effect=deploy.PasswordInputError("无法关闭回显")), self.assertRaisesRegex(deploy.DeployError, "回显"):
            deploy.read_password(args)
        self.assertNotIn(self.password, self.output.getvalue())

    def test_hidden_input_retries_mismatch_and_rejects_nonterminal(self):
        args = deploy.parser().parse_args([])
        with patch.object(deploy.sys.stdin, "isatty", return_value=True), patch.object(deploy, "password_prompt", side_effect=[self.password, "different password", self.password, self.password]):
            self.assertEqual(deploy.read_password(args)[0], self.password)
        with patch.object(deploy.sys.stdin, "isatty", return_value=False), self.assertRaises(deploy.DeployError):
            deploy.read_password(args)
        self.assertNotIn(self.password, self.output.getvalue())

    def test_custom_initial_password_has_no_plaintext_file_content(self):
        args = deploy.parser().parse_args(["--yes", "--password-stdin", "--admin-username", "Site.Owner"])
        with self.stdin((self.password + "\n").encode()), patch.object(deploy, "run"):
            hashed, login = deploy.initial_credentials(self.values, lambda value: "$argon2id$fixture" if value == self.password else "bad", args)
        self.assertEqual(hashed, "$argon2id$fixture")
        self.assertEqual(login, "")
        self.assertEqual(self.values["NEWPRESS_ADMIN_USERNAME"], "site.owner")
        self.assertNotIn(self.password, self.output.getvalue())

    def test_random_initial_password_stays_out_of_console(self):
        with patch.object(deploy.secrets, "token_urlsafe", return_value=self.password):
            _, login = deploy.initial_credentials(self.values, lambda value: "$argon2id$fixture")
        self.assertIn(self.password, login)
        self.assertNotIn(self.password, self.output.getvalue())

    def test_invalid_usernames_and_passwords_are_rejected(self):
        for name in ("ab", "x" * 81, "space name", "option;name", "name\n", "test/../admin"):
            # Trailing whitespace is normalized intentionally.
            if name == "name\n":
                continue
            with self.subTest(name=name), self.assertRaises(deploy.DeployError):
                deploy.validate_admin_username(name)
        self.assertEqual(deploy.validate_admin_username("站点.Admin"), "站点.admin")

    def test_cli_rejects_incompatible_secret_flags_before_dispatch(self):
        with patch.object(deploy, "dispatch") as dispatch:
            for arguments in (["admin-list", "--password-stdin", "--yes"], ["admin-reset", "--password-stdin"],
                              ["check", "--admin-setup"], ["logs", "--admin-username", "owner"],
                              ["admin-reset", "--admin-id", "0"], ["menu", "--password-stdin", "--yes"]):
                with self.subTest(arguments=arguments), self.assertRaises(deploy.DeployError):
                    deploy.main(arguments)
        dispatch.assert_not_called()

    def test_stdin_consumed_before_child_commands_and_forwarded_only_on_pipe(self):
        with self.stdin((self.password + "\n").encode()), patch.object(deploy, "dispatch", return_value=0) as dispatch:
            self.assertEqual(deploy.main(["admin-reset", "--admin-id", "7", "--yes", "--password-stdin"]), 0)
            args = dispatch.call_args.args[0]
            self.assertEqual(deploy.sys.stdin.buffer.read(), b"")
        with patch.object(deploy.shutil, "which", return_value="sudo"), patch.object(deploy, "run", return_value=result()) as run:
            deploy.sudo_command(args, "admin-reset")
        self.assertNotIn(self.password, str(run.call_args.args[0]))
        self.assertEqual(run.call_args.kwargs["input"], self.password + "\n")

    def test_docker_reset_uses_stdin_expected_version_and_safe_projection(self):
        args = deploy.parser().parse_args(["admin-reset", "--admin-id", "7", "--yes"])
        with patch.object(deploy, "management_context", return_value=(self.values, "docker")), \
                patch.object(deploy, "read_password", return_value=(self.password, False)), \
                patch.object(deploy, "compose", side_effect=[result(json.dumps(self.accounts)), result(json.dumps(dict(self.accounts[0], version=5)))]) as compose:
            self.assertEqual(deploy.admin_command(args), 0)
        call = compose.call_args
        self.assertIn("--expected-version", call.args)
        self.assertIn("--password-stdin", call.args)
        self.assertEqual(call.kwargs["input"], self.password + "\n")
        self.assertTrue(call.kwargs["capture"])
        self.assertNotIn(self.password, str(call.args))
        self.assertNotIn(self.password, self.output.getvalue())

    def test_account_selection_and_confirmation_are_required(self):
        for arguments in (["admin-reset", "--yes"], ["admin-reset", "--admin-id", "88", "--yes"], ["admin-reset", "--admin-id", "7"]):
            args = deploy.parser().parse_args(arguments)
            with self.subTest(arguments=arguments), patch.object(deploy, "management_context", return_value=(self.values, "docker")), \
                    patch.object(deploy, "run_admin_backend", return_value=self.accounts) as backend, \
                    patch.object(deploy, "interactive", return_value=False), self.assertRaises(deploy.DeployError):
                deploy.admin_command(args)
            self.assertEqual(backend.call_count, 1)

    def test_cancel_does_not_read_password_or_mutate(self):
        args = deploy.parser().parse_args(["admin-reset", "--admin-id", "7", "--interactive"])
        with patch.object(deploy, "management_context", return_value=(self.values, "docker")), \
                patch.object(deploy, "run_admin_backend", return_value=self.accounts) as backend, \
                patch.object(deploy, "yes_no", return_value=False), patch.object(deploy, "read_password") as password:
            self.assertEqual(deploy.admin_command(args), 0)
        self.assertEqual(backend.call_count, 1)
        password.assert_not_called()

    def test_native_failed_mutation_restores_service_and_runs_as_service_user(self):
        args = deploy.parser().parse_args(["admin-reset", "--admin-id", "7", "--yes"])
        identity = {"user": 123, "group": 456, "extra_groups": []}
        for code in (0, 1):
            calls = []
            def child(arguments, **kwargs):
                calls.append((arguments, kwargs))
                if arguments[0] == deploy.NATIVE_BIN:
                    return result(json.dumps(dict(self.accounts[0], version=5)), code=code)
                return result("active" if "show" in arguments else "")
            with self.subTest(code=code), patch.object(deploy, "native_admin_identity", return_value=identity), \
                    patch.object(deploy, "run", side_effect=child), patch.object(deploy, "wait_http") as health, \
                    patch.dict(os.environ, {"NEWPRESS_DATABASE_URL": "malicious override"}):
                if code:
                    with self.assertRaises(deploy.DeployError):
                        deploy.run_admin_backend(self.values, "native", args, ["reset-password"], self.password)
                else:
                    deploy.run_admin_backend(self.values, "native", args, ["reset-password"], self.password)
            self.assertEqual(calls[1][0], ["systemctl", "stop", "newpress.service"])
            self.assertEqual(calls[-1][0], ["systemctl", "start", "newpress.service"])
            mutation = calls[2][1]
            self.assertEqual(mutation["identity"], identity)
            self.assertNotIn("NEWPRESS_DATABASE_URL", mutation["env"])
            self.assertNotIn(self.password, str(mutation["env"]))
            health.assert_called_once()

    def test_stopped_native_service_remains_stopped(self):
        args = deploy.parser().parse_args([])
        with patch.object(deploy, "native_admin_identity", return_value={"user": 123}), \
                patch.object(deploy, "run", side_effect=[result("inactive"), result("{}")]) as run:
            deploy.run_admin_backend(self.values, "native", args, ["rename"])
        self.assertEqual(run.call_count, 2)

    def test_native_list_needs_no_service_interruption(self):
        args = deploy.parser().parse_args([])
        with patch.object(deploy, "native_admin_identity", return_value={"user": 123}), \
                patch.object(deploy, "run", return_value=result("[]")) as run:
            self.assertEqual(deploy.run_admin_backend(self.values, "native", args, ["list"]), [])
        self.assertEqual(run.call_count, 1)

    def test_initial_login_file_removed_only_after_successful_mutation(self):
        secret = self.root / ".secrets"
        secret.mkdir()
        login = secret / "first-login.txt"
        login.write_text("old initial password")
        args = deploy.parser().parse_args([])
        with patch.object(deploy, "compose", return_value=result(code=1)), self.assertRaises(deploy.DeployError):
            deploy.run_admin_backend(self.values, "docker", args, ["reset-password"], self.password)
        self.assertTrue(login.exists())
        with patch.object(deploy, "compose", return_value=result("{}")):
            deploy.run_admin_backend(self.values, "docker", args, ["reset-password"], self.password)
        self.assertFalse(login.exists())

    def test_proxy_arguments_are_removed_and_manual_config_not_overwritten(self):
        with contextlib.redirect_stderr(io.StringIO()):
            for arguments in (["--reverse-proxy", "local"], ["--proxy-cidrs", "10.0.0.1"]):
                with self.assertRaises(SystemExit):
                    deploy.parser().parse_args(arguments)
        custom = self.root / "manual-nginx.conf"
        custom.write_text("operator maintained")
        values = {"NEWPRESS_NGINX_CONFIG": str(custom), "NEWPRESS_PROXY_MODE": "local"}
        deploy.write_proxy_config(values)
        self.assertEqual(custom.read_text(), "operator maintained")
        self.assertEqual(values["NEWPRESS_NGINX_CONFIG"], str(custom))


if __name__ == "__main__":
    unittest.main()
