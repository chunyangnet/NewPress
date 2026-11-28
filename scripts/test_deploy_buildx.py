"""Buildx installation consent and package manager failure handling."""
import types
import unittest
from unittest.mock import patch

from test_deploy import deploy, result


class BuildxTests(unittest.TestCase):
    def test_interactive_choice_installs_only_when_accepted(self):
        for choice in ("0", "1"):
            with self.subTest(choice=choice), patch.object(deploy, "run", side_effect=[result("2.20.0"), result(code=1)]), \
                    patch.object(deploy, "interactive", return_value=True), patch.object(deploy, "ask", return_value=choice), \
                    patch.object(deploy, "install_buildx") as install, patch.object(deploy, "ui"):
                if choice == "1":
                    deploy.check_docker_plugins(types.SimpleNamespace(check=False))
                    install.assert_called_once_with()
                else:
                    with self.assertRaises(deploy.DeployError):
                        deploy.check_docker_plugins(types.SimpleNamespace(check=False))
                    install.assert_not_called()

    def test_check_and_noninteractive_never_prompt_or_install(self):
        for checking, interactive in ((True, True), (False, False)):
            with self.subTest(checking=checking), patch.object(deploy, "run", side_effect=[result("2.20.0"), result(code=1)]), \
                    patch.object(deploy, "interactive", return_value=interactive), patch.object(deploy, "ask") as ask, \
                    patch.object(deploy, "install_buildx") as install:
                with self.assertRaises(deploy.DeployError):
                    deploy.check_docker_plugins(types.SimpleNamespace(check=checking))
                ask.assert_not_called()
                install.assert_not_called()

    def test_package_install_and_verification(self):
        for manager in ("apt-get", "dnf", "yum"):
            with self.subTest(manager=manager), patch.object(deploy.shutil, "which", side_effect=lambda name: name if name == manager else None), \
                    patch.object(deploy.os, "geteuid", return_value=0, create=True), \
                    patch.object(deploy, "run", return_value=result()) as run, patch.object(deploy, "log"):
                deploy.install_buildx()
                commands = [call.args[0] for call in run.call_args_list]
                self.assertIn([manager, "install", "-y", "docker-buildx-plugin"], commands)
                self.assertEqual(commands[-1], ["docker", "buildx", "version"])

    def test_package_failure_stops_installation(self):
        with patch.object(deploy.shutil, "which", return_value="apt-get"), \
                patch.object(deploy.os, "geteuid", return_value=0, create=True), \
                patch.object(deploy, "run", return_value=result(code=1)) as run:
            with self.assertRaisesRegex(deploy.DeployError, "Buildx 安装失败"):
                deploy.install_buildx()
            self.assertEqual(run.call_count, 1)


if __name__ == "__main__":
    unittest.main()
