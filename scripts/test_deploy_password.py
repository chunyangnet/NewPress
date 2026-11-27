"""Keystroke editing, live counts and terminal restoration for hidden input."""
import contextlib
import os
import sys
import types
import unittest
from unittest.mock import Mock, patch

import deploy_password as password


class PasswordTests(unittest.TestCase):
    def test_unicode_backspace_word_erase_and_clear_count_characters(self):
        state = password.PasswordBuffer()
        for char in "abc中文":
            state.feed(char)
        self.assertEqual(len(state.characters), 5)
        state.feed("\x7f")
        self.assertEqual(state.value, "abc中")
        for char in " word ":
            state.feed(char)
        state.feed("\x17")
        self.assertEqual(state.value, "abc中 ")
        state.feed("\x15")
        self.assertEqual(state.value, "")
        state.feed("\x7f")
        self.assertTrue(state.feed("\r"))

    def test_arrow_keys_do_not_become_password_characters(self):
        state = password.PasswordBuffer()
        for char in "ab\x1b[D\x1b[3~cd":
            state.feed(char)
        self.assertEqual(state.value, "abcd")

    def test_paste_is_literal_and_multiline_or_control_paste_is_rejected(self):
        state = password.PasswordBuffer()
        for char in "\x1b[200~A 中文 password!\x1b[201~":
            self.assertFalse(state.feed(char))
        self.assertEqual(state.value, "A 中文 password!")
        self.assertTrue(state.feed("\r"))
        for value in ("first\nsecond", "first\x15second", "first\x1b[Dsecond", "x" * 1025):
            state = password.PasswordBuffer()
            with self.subTest(value=value), self.assertRaises(password.PasswordInputError):
                for char in "\x1b[200~" + value + "\x1b[201~":
                    state.feed(char)

    def test_control_cancel_and_size_bound_never_return_partial_password(self):
        for char, error in (("\x03", KeyboardInterrupt), ("\x04", EOFError), ("\x1a", password.PasswordInputError)):
            with self.subTest(char=repr(char)), self.assertRaises(error):
                password.PasswordBuffer().feed(char)
        state = password.PasswordBuffer()
        with self.assertRaises(password.PasswordInputError):
            for char in "中" * 342:
                state.feed(char)

    def terminal(self, data):
        stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        original = [65535, 0, 0, 65535, 0, 0, [0] * 32]
        termios = types.SimpleNamespace(IXON=1, ICRNL=2, INLCR=4, IGNCR=8, ECHO=16, ECHONL=32,
            ICANON=64, ISIG=128, IEXTEN=256, VMIN=0, VTIME=1, TCSAFLUSH=2, error=OSError,
            tcgetattr=Mock(return_value=original), tcsetattr=Mock())
        stack.enter_context(patch.dict(sys.modules, {"termios": termios}))
        stack.enter_context(patch.dict(os.environ, {"TERM": "xterm"}))
        stack.enter_context(patch.object(password.sys.stdin, "isatty", return_value=True))
        stack.enter_context(patch.object(password.os, "O_NOCTTY", 0, create=True))
        stack.enter_context(patch.object(password.os, "open", return_value=88))
        stack.enter_context(patch.object(password.os, "read", side_effect=[bytes([value]) for value in data] + [b""]))
        output = bytearray()
        def write(fd, value):
            output.extend(value)
            return len(value)
        stack.enter_context(patch.object(password.os, "write", side_effect=write))
        closed = stack.enter_context(patch.object(password.os, "close"))
        return original, termios, output, closed

    def test_live_counts_update_without_password_and_restore_terminal(self):
        original, terminal, output, closed = self.terminal("abc中\x7f文\r".encode())
        self.assertEqual(password.password_prompt("新密码："), "abc文")
        text = output.decode()
        self.assertIn("已输入 4 位", text)
        self.assertGreater(text.count("已输入 3 位"), 1)
        self.assertNotIn("abc", text)
        self.assertNotIn("文", text)
        hidden = terminal.tcsetattr.call_args_list[0].args[2]
        self.assertEqual(hidden[3] & (terminal.ECHO | terminal.ICANON | terminal.ISIG), 0)
        self.assertEqual(terminal.tcsetattr.call_args.args[2], original)
        closed.assert_called_once_with(88)
        self.assertIn("\x1b[?2004l", text)

    def test_cancel_eof_and_decode_failure_restore_echo(self):
        for data, error in ((b"abc\x03", KeyboardInterrupt), (b"abc\x04", EOFError), (b"abc", EOFError), (b"\xff", password.PasswordInputError)):
            with self.subTest(data=data):
                original, terminal, output, closed = self.terminal(data)
                with self.assertRaises(error):
                    password.password_prompt("新密码：")
                self.assertEqual(terminal.tcsetattr.call_args.args[2], original)
                closed.assert_called_once_with(88)
                self.assertNotIn(b"abc", output)

    def test_terminal_setup_failure_reads_no_password_and_restores(self):
        original, terminal, output, closed = self.terminal(b"unused secret\r")
        terminal.tcsetattr.side_effect = [OSError("terminal failure"), None]
        with self.assertRaises(password.PasswordInputError):
            password.password_prompt("新密码：")
        password.os.read.assert_not_called()
        self.assertEqual(terminal.tcsetattr.call_args.args[2], original)
        closed.assert_called_once_with(88)


if __name__ == "__main__":
    unittest.main()
