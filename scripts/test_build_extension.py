"""License retention and immutable-output checks for extension packaging."""
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

import build_extension


class ExtensionLicenseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        (self.source / "theme.json").write_text(json.dumps({
            "schema": 1, "slug": "fixture", "version": "1.0.0", "license": "Apache-2.0"
        }), encoding="utf-8")
        self.output = self.root / "fixture.zip"
        self.apache = (Path(build_extension.__file__).resolve().parents[1] / "LICENSE").read_bytes()

    def test_missing_empty_or_abbreviated_license_never_creates_package(self):
        for text in (None, " \n", "Apache-2.0\n", "Apache License\nEND OF TERMS AND CONDITIONS"):
            with self.subTest(text=text):
                if text is not None:
                    (self.source / "LICENSE").write_text(text, encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "LICENSE|license"):
                    build_extension.build(self.source, self.output, "theme")
                self.assertFalse(self.output.exists())

    def test_full_license_and_separate_notices_survive_archive_unchanged(self):
        (self.source / "LICENSE").write_bytes(self.apache)
        notices = {"THIRD_PARTY_NOTICES.md": b"Separate font license: assets/fonts/OFL.txt\n",
                   "assets/fonts/OFL.txt": b"Fixture upstream font license\n"}
        for name, body in notices.items():
            path = self.source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(body)
        build_extension.build(self.source, self.output, "theme")
        before = self.output.read_bytes()
        with zipfile.ZipFile(self.output) as archive:
            self.assertEqual(archive.read("LICENSE"), self.apache)
            for name, body in notices.items():
                self.assertEqual(archive.read(name), body)
        with self.assertRaisesRegex(ValueError, "already exists"):
            build_extension.build(self.source, self.output, "theme")
        self.assertEqual(before, self.output.read_bytes())

    def test_child_manifest_packages_only_child_files(self):
        manifest = {"schema": 2, "slug": "fixture", "version": "1.0.0", "license": "Apache-2.0",
                    "parent": {"slug": "starter", "version": ">=1.0.0 <2.0.0"}}
        (self.source / "theme.json").write_text(json.dumps(manifest), encoding="utf-8")
        (self.source / "LICENSE").write_bytes(self.apache)
        build_extension.build(self.source, self.output, "theme")
        with zipfile.ZipFile(self.output) as archive:
            self.assertEqual(json.loads(archive.read("theme.json")), manifest)
            self.assertFalse(any(name.startswith("templates/") for name in archive.namelist()))

    def test_child_requires_schema_two(self):
        manifest = {"schema": 1, "slug": "fixture", "version": "1.0.0", "license": "Apache-2.0",
                    "parent": {"slug": "starter", "version": "*"}}
        (self.source / "theme.json").write_text(json.dumps(manifest), encoding="utf-8")
        (self.source / "LICENSE").write_bytes(self.apache)
        with self.assertRaises(ValueError):
            build_extension.build(self.source, self.output, "theme")
        self.assertFalse(self.output.exists())

    def test_application_copyright_can_differ_without_rewriting_terms(self):
        text = self.apache.decode("utf-8").replace("Copyright 2026 NewPress contributors", "Copyright 2026 Example Contributor")
        (self.source / "LICENSE").write_text(text, encoding="utf-8")
        build_extension.build(self.source, self.output, "theme")
        with zipfile.ZipFile(self.output) as archive:
            self.assertEqual(archive.read("LICENSE"), (self.source / "LICENSE").read_bytes())


if __name__ == "__main__":
    unittest.main()
