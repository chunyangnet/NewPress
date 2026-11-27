"""Public documentation must work without the maintainer's private directory."""
import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("check_public_source", Path(__file__).with_name("check_public_source.py"))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class PublicDocumentationTests(unittest.TestCase):
    def test_rejects_links_reference_definitions_and_code_paths(self):
        paths = ("docs/THEME_DEVELOPMENT.md", "../../docs/THEME_DEVELOPMENT.md",
                 "/docs/THEME_DEVELOPMENT.md", "..\\docs\\THEME_DEVELOPMENT.md")
        tick = chr(96)
        for path in paths:
            for line in (f"[guide]({path})", f"[guide]: {path}", f"See {tick}{path}{tick}."):
                with self.subTest(line=line):
                    errors = checker.documentation_errors("themes/example/README.md", ("# Example\n" + line).encode())
                    self.assertEqual(len(errors), 1)
                    self.assertIn("README.md:2:", errors[0])

    def test_allows_public_guides_external_docs_and_boundary_explanations(self):
        content = ("[guide](../README.md)\n"
                   "[external](https://example.test/docs/guide.md)\n"
                   "The local docs/ directory is private and is not distributed.\n")
        self.assertEqual(checker.documentation_errors("README.md", content.encode()), [])
        self.assertEqual(checker.documentation_errors("test.py", b"docs/fixture.md"), [])

    def test_public_check_reports_broken_reference_from_git_file_list(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            readme = root / "README.md"
            readme.write_text("[guide](docs/GUIDE.md)\n", encoding="utf-8")
            listing = subprocess.CompletedProcess([], 0, b"README.md\0", b"")
            with patch.object(checker, "ROOT", root), \
                    patch.object(checker.subprocess, "run", return_value=listing), \
                    patch.object(checker.sys, "argv", ["check_public_source.py"]), \
                    contextlib.redirect_stderr(io.StringIO()) as errors:
                self.assertEqual(checker.main(), 1)
                self.assertIn("README.md:1: reference to private documentation", errors.getvalue())
                readme.write_text("Public instructions are self-contained.\n", encoding="utf-8")
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(checker.main(), 0)


if __name__ == "__main__":
    unittest.main()
