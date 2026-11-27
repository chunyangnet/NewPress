#!/usr/bin/env python3
"""Collect cached, pinned Go-module licenses and in-tree notices; --check verifies freshness.

No dependencies are downloaded or upgraded. Populate the exact module graph first
with `go mod download all`. Text and SHA-256 values use UTF-8 with LF line endings.
The inventory includes dependency test fixtures; it is not a legal compatibility audit.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "internal/licensing"
NAMES = re.compile(r"^(?:licen[cs]e|copying|copyright|notice|ofl)(?:$|[._-])", re.I)
CODE = {".go", ".c", ".h", ".js", ".py", ".sh"}


def go(*args, cwd=ROOT):
    env = dict(os.environ, GOPROXY="off", GOWORK="off", GOFLAGS="")
    return subprocess.check_output(["go", *args], cwd=cwd, env=env, encoding="utf-8")


def objects(text):
    decoder = json.JSONDecoder()
    while text.strip():
        value, end = decoder.raw_decode(text.lstrip())
        yield value
        text = text.lstrip()[end:]


def license_files(directory):
    return sorted((p for p in directory.rglob("*") if p.is_file()
                   and NAMES.match(p.name) and p.suffix.lower() not in CODE),
                  key=lambda p: p.relative_to(directory).as_posix())


def collect():
    sections, records = [], []

    def add(name, version, directory, files):
        if not files:
            raise ValueError(f"no license files found for {name}@{version}")
        sources = []
        for path in files:
            if path.is_symlink():
                raise ValueError(f"linked license file in {name}")
            relative = path.relative_to(directory).as_posix()
            text = path.read_bytes().decode("utf-8").replace("\r\n", "\n").replace("\r", "\n")
            if not text.strip():
                raise ValueError(f"empty license file: {name}/{relative}")
            digest = hashlib.sha256(text.encode("utf-8")).hexdigest()
            sources.append({"path": relative, "sha256_lf": digest})
            sections.append(f"\n{'=' * 72}\nComponent: {name}\nVersion: {version}\n"
                            f"Source: {relative}\nSHA-256 (UTF-8/LF): {digest}\n{'-' * 72}\n"
                            + text + ("" if text.endswith("\n") else "\n"))
        return sources

    modules = sorted((m for m in objects(go("list", "-mod=readonly", "-m", "-json", "all"))
                      if not m.get("Main")), key=lambda m: m["Path"])
    for module in modules:
        resolved = module.get("Replace", module)
        if not resolved.get("Dir"):
            # Query the exact cached archive outside the project so go.sum is not changed.
            with tempfile.TemporaryDirectory(prefix="newpress-license-") as temporary:
                cached = json.loads(go("mod", "download", "-json",
                                       f"{resolved['Path']}@{resolved['Version']}", cwd=temporary))
            if not cached.get("Dir"):
                raise ValueError(f"module cache missing: {module['Path']}@{module['Version']}; run go mod download all")
            resolved = dict(resolved, Dir=cached["Dir"])
        directory = Path(resolved["Dir"])
        record = {"path": module["Path"], "version": module["Version"]}
        if "Replace" in module:
            replacement = resolved["Path"]
            if not resolved.get("Version"):
                replacement = "./" + directory.resolve().relative_to(ROOT).as_posix()
            record["replace"] = {"path": replacement, "version": resolved.get("Version", "")}
        record["licenses"] = add(module["Path"], module["Version"], directory, license_files(directory))
        records.append(record)

    info = json.loads(go("env", "-json", "GOROOT", "GOVERSION"))
    goroot = Path(info["GOROOT"])
    extras = [{"name": "Go standard library", "version": info["GOVERSION"],
               "licenses": add("Go standard library", info["GOVERSION"], goroot,
                               [goroot / "LICENSE"] + ([goroot / "PATENTS"] if (goroot / "PATENTS").is_file() else []))}]
    for name, version, paths in (
        ("NewPress core UI third-party notices", "in-tree", ["internal/coreui/THIRD_PARTY_NOTICES.md"]),
        ("Inter and Barlow Semi Condensed", "pinned; see fonts-manifest.json", [
            "internal/coreui/assets/fonts/inter-OFL.txt",
            "internal/coreui/assets/fonts/barlowsemicondensed-OFL.txt",
        ]),
        ("flag-icons", "v7.5.0", ["internal/commentorigin/flags/LICENSE", "internal/commentorigin/flags/README.md"]),
        ("NewPress GPT stickers", "CC-BY-4.0", [
            "third_party/licenses/emoji-NOTICE.txt", "third_party/licenses/CC-BY-4.0.txt",
        ]),
    ):
        extras.append({"name": name, "version": version,
                       "licenses": add(name, version, ROOT, [ROOT / p for p in paths])})
    inventory = {"schema": 1, "generator": "scripts/collect_licenses.py",
                 "normalization": "UTF-8/LF; sha256_lf hashes normalized source text",
                 "scope": "Full go list -m all graph, including dependency test fixtures; no license classification inferred",
                 "modules": records, "additional_components": extras}
    header = ("NewPress third-party license texts\nGenerated by scripts/collect_licenses.py.\n"
              "Sources are retained in full with LF line endings; hashes cover that normalized text.\n"
              "The full selected Go module graph includes test dependencies and fixtures, whether or not linked.\n"
              "Component labels record provenance, not a new license grant or compatibility determination.\n")
    return {"GO_MODULES.json": json.dumps(inventory, ensure_ascii=False, indent=2) + "\n",
            "THIRD_PARTY_LICENSES.txt": header + "".join(sections)}, len(records)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="fail if the committed inventory differs")
    args = parser.parse_args()
    try:
        outputs, count = collect()
        for name, text in outputs.items():
            path, data = OUTPUT / name, text.encode("utf-8")
            if args.check:
                if not path.is_file() or path.read_bytes().replace(b"\r\n", b"\n") != data:
                    raise ValueError(f"outdated {path.relative_to(ROOT)}; run scripts/collect_licenses.py")
            else:
                OUTPUT.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
        print(f"{'Checked' if args.check else 'Collected'} licenses for {count} Go modules and 3 additional components.")
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"License collection failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
