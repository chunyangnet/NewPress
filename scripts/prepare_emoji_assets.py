"""Slice the owner's GPT sheets, keeping alpha and recording provenance.

Requires Pillow. Run from any directory; --check verifies the committed assets.
No image-generation service, model, or network is used by this preparation step.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import json
import re
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, PngImagePlugin

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "docs" / "emoji"
TARGETS = [ROOT / "internal/coreui/assets", ROOT / "themes/chunnel/assets"]
SIZE = 128


def separator(alpha: Image.Image, low: int, high: int) -> list[int]:
    """Follow the transparent gap where the last two whale rows interleave."""
    width = alpha.width
    pixels = alpha.load()
    count = high - low
    previous = [0.0] * count
    paths: list[list[int]] = []
    for x in range(width):
        current, parents = [], []
        for y in range(count):
            parent = min(range(max(0, y - 2), min(count, y + 3)),
                         key=lambda z: previous[z] + abs(z - y) * 0.3)
            cost = pixels[x, low + y] ** 2 + abs(y - count / 2) * 0.01
            current.append(previous[parent] + abs(parent - y) * 0.3 + cost)
            parents.append(parent)
        paths.append(parents)
        previous = current
    y = min(range(count), key=previous.__getitem__)
    seam = [0] * width
    for x in range(width - 1, -1, -1):
        seam[x] = low + y
        y = paths[x][y]
    return seam


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def products() -> dict[str, bytes]:
    catalog = json.loads((SOURCE / "catalog.json").read_text(encoding="utf-8"))
    result: dict[str, bytes] = {}
    manifest = {"schema": 1, "recorded": catalog["recorded"],
                "provenance": catalog["provenance"],
                "rights": "CC BY 4.0 for rights the licensor may grant; third-party character and trademark rights are not granted or cleared.",
                "license": "CC-BY-4.0",
                "license_url": "https://creativecommons.org/licenses/by/4.0/",
                "attribution": "NewPress 项目贡献者",
                "sources": [], "assets": []}
    for pack in catalog["packs"]:
        raw = (SOURCE / pack["source"]).read_bytes()
        sheet = Image.open(io.BytesIO(raw)).convert("RGBA")
        if sheet.size != (1536, 1024) or len(pack["names"]) != 30:
            raise ValueError(f"Unexpected sheet geometry: {pack['id']}")
        manifest["sources"].append({"pack": pack["id"], "file": pack["source"],
                                    "sha256": digest(raw), "notice": pack["notice"]})
        seam = None
        if "seam" in pack:
            seam = separator(sheet.getchannel("A"), pack["seam"]["minY"], pack["seam"]["maxY"])
        for index, name in enumerate(pack["names"]):
            row, col = divmod(index, 6)
            left, right = pack["columns"][row][col:col + 2]
            top, bottom = pack["rows"][row:row + 2]
            layer = sheet
            if seam and row in (3, 4):
                # A straight cut would remove the lower character's hair tuft.
                upper = Image.new("L", sheet.size)
                ImageDraw.Draw(upper).polygon([(0, 0), (sheet.width - 1, 0)] +
                                             [(x, seam[x]) for x in range(sheet.width - 1, -1, -1)], fill=255)
                mask = upper if row == 3 else ImageChops.invert(upper)
                layer = sheet.copy()
                layer.putalpha(ImageChops.multiply(sheet.getchannel("A"), mask))
                if row == 3:
                    bottom = max(seam) + 1
                else:
                    top = min(seam)
            tile = layer.crop((left, top, right, bottom))
            bounds = tile.getchannel("A").point(lambda value: 255 if value > 8 else 0).getbbox()
            if not bounds:
                raise ValueError(f"Empty sticker: {pack['id']}/{name}")
            x0, y0, x1, y1 = bounds
            tile = tile.crop((max(0, x0 - 2), max(0, y0 - 2), min(tile.width, x1 + 2), min(tile.height, y1 + 2)))
            tile.thumbnail((SIZE - 8, SIZE - 8), Image.Resampling.LANCZOS)
            canvas = Image.new("RGBA", (SIZE, SIZE))
            canvas.paste(tile, ((SIZE - tile.width) // 2, (SIZE - tile.height) // 2))
            token = f"[{pack['prefix']}{name}]"
            meta = PngImagePlugin.PngInfo()
            meta.add_itxt("Title", token)
            meta.add_itxt("Description", pack["notice"] + "；所有者提供的 GPT 生成表情，非官方素材。")
            meta.add_itxt("Source", f"{pack['source']}; SHA-256={digest(raw)}; cell={index + 1}")
            meta.add_itxt("Rights", manifest["rights"])
            meta.add_text("Software", "NewPress prepare_emoji_assets.py (crop and resize only)")
            # This is a factual provenance note, not a fabricated provider signature.
            meta.add_itxt("AI-Generated", "true; declared by the project owner; model version unspecified")
            output = io.BytesIO()
            canvas.save(output, "PNG", pnginfo=meta, optimize=True)
            filename = f"{pack['id']}-{index + 1:02d}.png"
            result[f"images/emoji/{filename}"] = output.getvalue()
            manifest["assets"].append({"pack": pack["id"], "name": name, "token": token,
                                       "file": f"images/emoji/{filename}", "source_cell": index + 1,
                                       "width": SIZE, "height": SIZE, "sha256": digest(output.getvalue())})
    result["images/emoji/manifest.json"] = (json.dumps(manifest, ensure_ascii=False, indent=2) + "\n").encode()
    result["images/emoji/LICENSE.txt"] = (ROOT / "third_party/licenses/CC-BY-4.0.txt").read_bytes()
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    output = products()
    config = json.loads((SOURCE / "catalog.json").read_text(encoding="utf-8"))
    packs = [{key: pack[key] for key in ("id", "name", "prefix", "names")} for pack in config["packs"]]
    block = "  // BEGIN GENERATED EMOJI CATALOG\n  const packs = " + json.dumps(packs, ensure_ascii=False, indent=2).replace("\n", "\n  ") + ";\n  // END GENERATED EMOJI CATALOG"
    script = ROOT / "frontend/shared/emoji.ts"
    source = script.read_text(encoding="utf-8")
    prepared, count = re.subn(r"  // BEGIN GENERATED EMOJI CATALOG.*?  // END GENERATED EMOJI CATALOG", lambda _: block, source, flags=re.S)
    if count != 1:
        raise SystemExit(f"Missing catalog markers: {script}")
    if args.check:
        if source != prepared:
            raise SystemExit(f"Stale emoji catalog: {script}")
    else:
        script.write_text(prepared, encoding="utf-8", newline="\n")
    for target in TARGETS:
        for name, data in output.items():
            path = target / name
            if args.check:
                if not path.is_file() or path.read_bytes() != data:
                    raise SystemExit(f"Stale or missing: {path}")
            else:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
        expected = {Path(name).name for name in output}
        extras = {path.name for path in (target / "images/emoji").iterdir()} - expected
        if extras:
            raise SystemExit(f"Unexpected emoji files (remove explicitly): {target}: {sorted(extras)}")
    print(f"60 transparent stickers and provenance {'verified' if args.check else 'prepared'} in core UI and Chunnel")


if __name__ == "__main__":
    main()
