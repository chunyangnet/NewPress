#!/usr/bin/env python3
"""Build signed NewPress update ZIPs for synchronized GitHub / Gitee release branches.

Signs locally with a deployment-specific Ed25519 release key. Publish packages and
updates/<channel>.json together; official releases share one commit on both hosts.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
PLATFORMS = {"linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64"}


def configured_signing_key():
    """Read a local path only; private key contents never enter Git config."""
    if value := os.environ.get("NEWPRESS_CORE_SIGNING_KEY", "").strip():
        return Path(value).expanduser()
    try:
        result = subprocess.run(["git", "config", "--local", "--get", "newpress.coreSigningKey"],
                                cwd=ROOT, capture_output=True, text=True, check=False, timeout=10)
    except (OSError, subprocess.TimeoutExpired):
        return None
    value = result.stdout.strip() if result.returncode == 0 else ""
    return Path(value).expanduser() if value else None


def valid_version(value):
    if len(value) > 64:
        return False
    match = re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?", value)
    if match is None:
        return False
    return all(not (part.isdigit() and len(part) > 1 and part.startswith("0"))
               for part in (match[4] or "").split("."))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="Release version, for example 1.0.0-beta.1")
    parser.add_argument("--output", type=Path, required=True, help="New local publication directory")
    parser.add_argument("--platform", action="append", choices=sorted(PLATFORMS), help="Repeat for multiple platforms")
    parser.add_argument("--commit", default="unknown")
    parser.add_argument("--channel", choices=("stable", "beta"), default="stable", help="Official beta installs read updates/beta.json; stable remains the private-source default")
    parser.add_argument("--notes", type=Path, help="UTF-8 plain-text release notes")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--data-compatible", action="store_true", help="Confirm the old release can still read all data written by this release")
    mode.add_argument("--withdraw", action="store_true", help="Publish only a signed withdrawal. Existing packages are retained.")
    parser.add_argument("--signing-key", type=Path, help="Private Ed25519 key outside source/output; defaults to NEWPRESS_CORE_SIGNING_KEY or local Git newpress.coreSigningKey")
    parser.add_argument("--sequence", required=True, type=int, help="Increasing release sequence, also for withdrawal or renewal")
    parser.add_argument("--expires", required=True, help="RFC3339 expiry with timezone")
    args = parser.parse_args()
    try:
        expires = datetime.datetime.fromisoformat(args.expires.replace("Z", "+00:00"))
        if expires.utcoffset() is None or expires <= datetime.datetime.now(datetime.timezone.utc):
            raise ValueError
    except ValueError:
        parser.error("--expires must be a future RFC3339 timestamp with timezone")
    if not 0 < args.sequence < 2**63:
        parser.error("--sequence must be a positive int64")
    if not valid_version(args.version):
        parser.error("--version must be a semantic version without a v prefix")
    if not re.fullmatch(r"[A-Za-z0-9._-]{1,80}", args.commit):
        parser.error("--commit must be a short identifier without spaces")
    args.signing_key = args.signing_key or configured_signing_key()
    if args.signing_key is None:
        parser.error("provide --signing-key, NEWPRESS_CORE_SIGNING_KEY or local Git newpress.coreSigningKey; no key is generated automatically")
    output = args.output.resolve()
    key = args.signing_key.absolute()
    if key.is_symlink() or not key.is_file() or key.resolve().is_relative_to(ROOT) or key.resolve().is_relative_to(output):
        parser.error("--signing-key must be a regular file outside the checkout and release directory")
    if output.exists():
        parser.error("--output must be a new directory; existing releases are never overwritten")
    if args.withdraw and args.platform:
        parser.error("--withdraw has no platform packages")
    notes = args.notes.read_text(encoding="utf-8") if args.notes else ""
    if len(notes.encode("utf-8")) > 16000:
        parser.error("release notes exceed 16000 bytes")
    output.mkdir(parents=True)
    artifacts = [] if args.withdraw else build_packages(args, output)
    manifest = {"schema": 2, "product": "newpress-core", "sequence": args.sequence,
                "expires": expires.astimezone(datetime.timezone.utc).isoformat(timespec="seconds"), "version": args.version, "notes": notes, "data_compatible": args.data_compatible, "artifacts": artifacts}
    (output / "updates").mkdir()
    # Keep the private key off the site. Publish the envelope only after signing succeeds.
    with tempfile.TemporaryDirectory(prefix="newpress-sign-") as signing:
        payload = Path(signing) / "manifest.json"
        payload.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        signed = subprocess.check_output(["go", "run", "./cmd/updatesign", "-key", str(key), "-manifest", str(payload)], cwd=ROOT)
        (output / "updates" / (args.channel + ".json")).write_bytes(signed)
    if args.withdraw:
        print(f"Withdrawal ready in {output}. Commit updates/{args.channel}.json to the release channel.")
    else:
        print(f"Ready: {output}. Publish packages/, updates/, LICENSE and THIRD_PARTY_LICENSES.txt in ONE release-branch commit; push identical files and commits to both official repositories.")
        if not args.data_compatible:
            print("Online installation is disabled for this manifest: data compatibility was not confirmed.")


def build_packages(args, output):
    subprocess.run([sys.executable, str(ROOT / "scripts" / "collect_licenses.py"), "--check"], cwd=ROOT, check=True)
    platforms = list(dict.fromkeys(args.platform or ["linux/amd64", "linux/arm64", "windows/amd64"]))
    # Texts are also embedded in each executable (newpress licenses). Sidecars
    # can be read without executing a foreign-platform binary.
    (output / "LICENSE").write_bytes((ROOT / "LICENSE").read_bytes())
    (output / "THIRD_PARTY_LICENSES.txt").write_bytes((ROOT / "internal/licensing/THIRD_PARTY_LICENSES.txt").read_bytes())
    artifacts = []
    timestamp = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")
    with tempfile.TemporaryDirectory(prefix="newpress-build-") as build:
        for platform in platforms:
            target_os, arch = platform.split("/")
            name = "newpress.exe" if target_os == "windows" else "newpress"
            binary = Path(build) / name
            environment = dict(os.environ, CGO_ENABLED="0", GOOS=target_os, GOARCH=arch)
            flags = f"-s -w -X newpress.local/newpress/internal/version.Version={args.version} -X newpress.local/newpress/internal/version.Commit={args.commit} -X newpress.local/newpress/internal/version.BuildDate={timestamp}"
            print(f"Building {platform} / {args.version}", flush=True)
            subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", f"-ldflags={flags}", "-o", str(binary), "./cmd/newpress"], cwd=ROOT, env=environment, check=True)
            relative = f"packages/{args.version}/newpress-{target_os}-{arch}.zip"
            package = output / relative
            package.parent.mkdir(parents=True, exist_ok=True)
            with zipfile.ZipFile(package, "x", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
                archive.write(binary, f"bin/{name}")
            with zipfile.ZipFile(package) as archive:
                if archive.namelist() != [f"bin/{name}"] or archive.testzip() is not None:
                    raise ValueError("release ZIP verification failed")
            size = package.stat().st_size
            if size > 96 * 1024 * 1024:
                raise RuntimeError(f"{relative} exceeds the 96 MiB update package limit")
            with package.open("rb") as stream:
                checksum = hashlib.file_digest(stream, "sha256").hexdigest()
            artifacts.append({"os": target_os, "arch": arch, "path": relative, "size": size, "sha256": checksum})
    return artifacts


if __name__ == "__main__":
    main()
