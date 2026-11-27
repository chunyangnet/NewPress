"""Regression coverage for terminal sizes reported by web-based consoles."""
import io
import os
import unittest
from unittest import mock

from deploy_terminal import Terminal, cells


class TerminalWidthTests(unittest.TestCase):
    def test_invalid_detected_sizes_keep_menu_readable(self):
        for columns in (0, 1, 16, 24):
            with self.subTest(columns=columns), mock.patch(
                "deploy_terminal.shutil.get_terminal_size",
                return_value=os.terminal_size((columns, 24)),
            ):
                output = io.StringIO()
                terminal = Terminal(color="never", stream=output)
                self.assertEqual(terminal.width, 80)
                terminal.menu("操作菜单", [("7", "管理员账号", "查看登录名、修改登录名、重置密码"),
                                           ("0", "退出", "")], "0")
                self.assertIn("管理员账号", output.getvalue())
                self.assertIn("查看登录名、修改登录名、重置密码", output.getvalue())
                self.assertTrue(all(cells(line) <= 80 for line in output.getvalue().splitlines()))

    def test_output_device_takes_priority_over_environment(self):
        stream = mock.Mock()
        stream.fileno.return_value = 42
        with mock.patch.dict(os.environ, {"COLUMNS": "16"}), mock.patch(
            "deploy_terminal.os.get_terminal_size", return_value=os.terminal_size((100, 24))
        ) as size:
            self.assertEqual(Terminal(stream=stream).width, 96)
            size.assert_called_once_with(42)

    def test_unavailable_device_uses_environment_and_tracks_resize(self):
        with mock.patch.dict(os.environ, {"COLUMNS": "40"}):
            terminal = Terminal(stream=io.StringIO())
            self.assertEqual(terminal.width, 40)
            os.environ["COLUMNS"] = "80"
            self.assertEqual(terminal.width, 80)

    def test_explicit_width_retains_narrow_layout_support(self):
        self.assertEqual(Terminal(width=16).width, 16)
        self.assertEqual(Terminal(width=40).width, 40)


if __name__ == "__main__":
    unittest.main()
