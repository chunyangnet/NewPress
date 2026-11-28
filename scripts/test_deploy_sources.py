"""Tests for actual-artifact probing, pinned identity, and source fallback."""
import hashlib
import io
import json
from pathlib import Path
import tempfile
import time
import types
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch

import deploy_sources as sources


def response(data, status=200, content_type="application/octet-stream"):
    result = io.BytesIO(data)
    result.status = status
    result.headers = {"Content-Type": content_type}
    return result


def packed(data):
    payload = json.dumps(data).encode()
    return payload, "sha256:" + hashlib.sha256(payload).hexdigest()


class SourceTests(unittest.TestCase):
    def setUp(self):
        self.logs = []
        self.selector = sources.Selector("auto", 2, self.logs.append)

    def test_candidates_preserve_original_digest_and_repository(self):
        original = "postgres:18@sha256:" + "a" * 64
        candidates = sources.image_candidates(original, "auto", ["https://mirror.example/docker.io"])
        self.assertEqual(candidates[0][1], original)
        self.assertEqual(candidates[-1][1], "mirror.example/docker.io/library/postgres@sha256:" + "a" * 64)
        self.assertTrue(all(item.endswith("@sha256:" + "a" * 64) for _, item in candidates))
        self.assertEqual(sources.image_candidates(original, "official", ["https://mirror.example"]), [("官方", original)])
        with self.assertRaises(sources.SourceError):
            sources.image_candidates("postgres:latest")

    def test_github_proxies_only_transport_official_public_release_urls(self):
        url = 'https://github.com/chunyangnet/NewPress/releases/download/v1.0.0-beta.2/file.part000'
        choices = sources.github_candidates(url)
        self.assertEqual(len(choices), 4)
        self.assertTrue(all(target.endswith(url) for _, target in choices))
        self.assertEqual(sources.github_candidates(url, 'official'), [('GitHub', url)])
        for invalid in ('https://github.com/other/private/archive.zip', url + '?token=secret',
                        url.replace('github.com/', 'github.com.evil/')):
            with self.assertRaises(sources.SourceError):
                sources.github_candidates(invalid)

    def test_github_probe_requires_same_amount_of_real_archive_bytes(self):
        url = 'https://github.com/chunyangnet/NewPress/releases/download/v1.0.0-beta.2/file.part000'
        self.selector.policy = 'official'
        with patch.object(self.selector, 'read', return_value=b'\x1f\x8b' + b'x' * 98) as read:
            self.assertEqual(len(self.selector.github_release(url, 100)), 1)
            read.assert_called_once_with(url, sample=True, limit=100)
        with patch.object(self.selector, 'read', return_value=b'\x1f\x8b'), self.assertRaises(sources.SourceError):
            self.selector.github_release(url, 100)

    def test_invalid_mirror_url_is_rejected(self):
        for url in ("http://mirror.example", "https://user:pass@mirror.example", "https://mirror.example?q=token",
                    "https://mirror.example/#token", "https://mirror.example:0", "https://mirror.example:99999"):
            with self.subTest(url=url), self.assertRaises(sources.SourceError):
                sources.https_base(url)

    def test_cross_host_redirect_drops_authorization_and_forbids_downgrade(self):
        handler = sources.SafeRedirect()
        request = urllib.request.Request("https://registry.example/blob", headers={"Authorization": "Bearer secret"})
        redirected = handler.redirect_request(request, None, 307, "Temporary", {}, "https://cdn.example/blob?signature=one")
        self.assertFalse(redirected.has_header("Authorization"))
        same = handler.redirect_request(request, None, 307, "Temporary", {}, "https://registry.example/other")
        self.assertEqual(same.get_header("Authorization"), "Bearer secret")
        with self.assertRaises(sources.SourceError):
            handler.redirect_request(request, None, 302, "Found", {}, "http://cdn.example/blob")

    def test_response_requires_file_content_with_bounded_size(self):
        for data, mime in ((b"<html>blocked</html>", "text/html"), (b"<!DOCTYPE html>blocked", "application/octet-stream"),
                           (b"", "application/octet-stream"), (b"12345", "application/octet-stream")):
            with self.subTest(data=data):
                self.selector.opener = types.SimpleNamespace(open=lambda *a, **k: response(data, content_type=mime))
                with self.assertRaises(sources.SourceError):
                    self.selector.read("https://files.example/a", limit=4)
        self.selector.opener = types.SimpleNamespace(open=lambda *a, **k: response(b"12345"))
        self.assertEqual(self.selector.read("https://files.example/a", limit=4, sample=True), b"1234")

    def test_rank_excludes_failed_sources_and_uses_measured_time(self):
        def probe(url):
            if url == "broken":
                raise sources.SourceError("token=must-not-appear")
            time.sleep(0.08 if url == "slow" else 0.001)
        choices = self.selector.rank("test", [("one", "slow"), ("two", "broken"), ("three", "fast")], probe)
        self.assertEqual([item["url"] for item in choices], ["fast", "slow"])
        self.assertNotIn("must-not-appear", "".join(self.logs))
        with self.assertRaises(sources.SourceError):
            self.selector.rank("test", [("one", "broken")], probe)

    def test_image_probe_reads_exact_manifest_and_actual_blob(self):
        manifest, manifest_digest = packed({"layers": [{"digest": "sha256:" + "b" * 64}]})
        index, index_digest = packed({"manifests": [
            {"digest": manifest_digest, "platform": {"os": "linux", "architecture": "arm64"}}]})
        reads = []
        def read(registry, repository, suffix, auth, **kwargs):
            reads.append((suffix, kwargs.get("sample", False)))
            return {"manifests/" + index_digest: index, "manifests/" + manifest_digest: manifest,
                    "blobs/sha256:" + "b" * 64: b"\x1f\x8bdata"}[suffix]
        self.selector.policy = "official"
        with patch.object(self.selector, "registry_read", side_effect=read):
            choices = self.selector.image("test", "gcr.io/distroless/static@" + index_digest, "arm64")
            self.assertEqual(len(choices), 1)
            self.assertEqual(reads[-1], ("blobs/sha256:" + "b" * 64, True))
            with self.assertRaises(sources.SourceError):
                self.selector.image("test", "gcr.io/distroless/static@" + index_digest, "amd64")
        with patch.object(self.selector, "registry_read", return_value=b'{"manifests": []}'), self.assertRaises(sources.SourceError):
            self.selector.image("test", "gcr.io/distroless/static@" + index_digest, "arm64")

    def test_single_manifest_validates_cpu_from_config(self):
        config, config_digest = packed({"os": "linux", "architecture": "arm64"})
        manifest, digest = packed({"config": {"digest": config_digest}, "layers": [{"digest": "sha256:" + "b" * 64}]})
        self.selector.policy = "official"
        with patch.object(self.selector, "registry_read", side_effect=[manifest, config]), self.assertRaises(sources.SourceError):
            self.selector.image("test", "image@" + digest, "amd64")

    def test_registry_401_requires_a_working_pull_token_and_file(self):
        unauthorized = urllib.error.HTTPError("https://registry.example/v2/", 401, "unauthorized",
            {"WWW-Authenticate": 'Bearer realm="https://auth.example/token",service="registry.example"'}, None)
        self.addCleanup(unauthorized.close)
        auth = {}
        with patch.object(self.selector, "read", side_effect=[unauthorized, b'{"token":"read-only"}', b"payload"]) as read:
            self.assertEqual(self.selector.registry_read("registry.example", "library/go", "manifests/sha256:a", auth), b"payload")
        self.assertIn("scope=repository%3Alibrary%2Fgo%3Apull", read.call_args_list[1].args[0])
        self.assertEqual(read.call_args_list[-1].kwargs["headers"]["Authorization"], "Bearer read-only")
        with patch.object(self.selector, "read", side_effect=unauthorized), self.assertRaises(urllib.error.HTTPError):
            self.selector.registry_read("registry.example", "library/go", "manifests/sha256:a", {})

    def test_go_proxy_requires_zip_and_checksum_relay(self):
        self.selector.policy = "official"
        with patch.object(self.selector, "read", side_effect=[b"", b"PK\x03\x04zip", b"github.com/jackc/pgx/v5 v5.10.0 h1:test"]) as read:
            self.assertEqual(len(self.selector.go_modules()), 1)
        self.assertIn("/sumdb/sum.golang.org/supported", read.call_args_list[0].args[0])
        with patch.object(self.selector, "read", side_effect=[b"", b"PK\x03\x04zip", b"blocked"]), self.assertRaises(sources.SourceError):
            self.selector.go_modules()

    def test_official_go_proxy_can_use_direct_checksum_service(self):
        self.selector.policy = "official"
        missing = urllib.error.HTTPError("https://proxy.golang.org/sumdb/sum.golang.org/supported", 404, "missing", {}, None)
        self.addCleanup(missing.close)
        with patch.object(self.selector, "read", side_effect=[missing, b"PK\x03\x04zip", b"github.com/jackc/pgx/v5 v5.10.0 h1:test"]) as read:
            self.assertEqual(len(self.selector.go_modules()), 1)
        self.assertEqual(read.call_args_list[-1].args[0], "https://sum.golang.org/lookup/github.com/jackc/pgx/v5@v5.10.0")

    def test_download_falls_back_after_checksum_mismatch(self):
        content = b"verified archive bytes"
        digest = hashlib.sha256(content).hexdigest()
        opener = types.SimpleNamespace(open=lambda url, **kwargs: response(b"bad" if "first" in url else content))
        choices = [{"name": "first", "url": "https://first.example/file"}, {"name": "second", "url": "https://second.example/file"}]
        with tempfile.TemporaryDirectory() as directory, patch.object(sources.urllib.request, "build_opener", return_value=opener):
            destination = Path(directory) / "archive"
            sources.download_verified(choices, destination, digest, self.logs.append)
            self.assertEqual(destination.read_bytes(), content)
            with self.assertRaises(sources.SourceError):
                sources.download_verified(choices[:1], destination, digest, self.logs.append)
            self.assertFalse(destination.exists())

    def test_apt_repositories_keep_security_suite_and_signed_by(self):
        text = sources.apt_stanzas({"ID": "debian", "VERSION_CODENAME": "bookworm"}, "https://mirror.example/debian", "amd64", "/key.gpg")
        self.assertIn("https://mirror.example/debian-security", text)
        self.assertIn("Suites: bookworm-security", text)
        self.assertEqual(text.count("Signed-By: /key.gpg"), 2)
        self.assertEqual(sources.security_repo({"ID": "ubuntu"}, "https://ports.ubuntu.com/ubuntu-ports"), "https://ports.ubuntu.com/ubuntu-ports")

    def test_repository_pins_and_toolchain_metadata_match_project(self):
        root = Path(__file__).resolve().parent.parent
        images = sources.pinned_images(root)
        self.assertEqual(set(images), {"go", "runtime", "postgres", "proxy"})
        compose = (root / "compose.yaml").read_text(encoding="utf-8")
        self.assertTrue(all(reference in compose for reference in images.values()))
        for arch in ("amd64", "arm64"):
            metadata = sources.go_metadata(root, arch)
            self.assertEqual(metadata["filename"], metadata["version"] + ".linux-" + arch + ".tar.gz")
        key = (root / "deploy/docker-release-key.asc").read_bytes()
        self.assertEqual(hashlib.sha256(key).hexdigest(), "1500c1f56fa9e26b9b8f42452a553675796ade0807cdce11975eb98170b3a570")


if __name__ == "__main__":
    unittest.main()
