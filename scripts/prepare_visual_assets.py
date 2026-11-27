"""Prepare Chunnel's editorial images from generated PNG originals (requires Pillow)."""

import argparse
import base64
import json
from pathlib import Path

from PIL import Image, ImageOps


ROOT = Path(__file__).resolve().parents[1]
IMAGES = ROOT / "themes/chunnel/assets/images"
ASSETS = {
    "reading-room": ("窗边的写作桌", None),
    "cover-library": ("午后的阅读角", "cover-chunnel.svg"),
    "cover-notes": ("翻开的笔记本", "cover-core.svg"),
    "cover-print": ("纸上的靛蓝印迹", "cover-plugin.svg"),
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    args = parser.parse_args()
    destination = IMAGES / "editorial"
    destination.mkdir(parents=True, exist_ok=True)
    manifest = {"collection": "纸间 · Paper & Light", "model": "gpt-image-2.6-sunburst", "generated": "2026-11-10", "assets": []}
    for name, (title, legacy) in ASSETS.items():
        with Image.open(args.input / f"{name}.png") as original:
            image = ImageOps.exif_transpose(original).convert("RGB")
        variants = []
        for suffix, width in [("", image.width), ("-small", 640 if legacy is None else 768)]:
            resized = image.resize((width, round(image.height * width / image.width)), Image.Resampling.LANCZOS)
            path = destination / f"{name}{suffix}.webp"
            resized.save(path, "WEBP", quality=82, method=6)
            variants.append({"file": path.relative_to(IMAGES).as_posix(), "width": resized.width, "height": resized.height, "bytes": path.stat().st_size})
        if legacy:
            # An SVG loaded through <img> cannot fetch an external raster image.
            # Embed the small WebP so existing stored cover URLs remain self-contained.
            payload = base64.b64encode((destination / f"{name}-small.webp").read_bytes()).decode("ascii")
            (IMAGES / legacy).write_text(
                '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 720" role="img" aria-labelledby="title">\n'
                f'  <title id="title">{title}</title>\n'
                f'  <image width="1200" height="720" preserveAspectRatio="xMidYMid slice" href="data:image/webp;base64,{payload}"/>\n'
                '</svg>\n', encoding="utf-8",
            )
        manifest["assets"].append({"id": name, "title": title, "legacy": legacy, "variants": variants})
    (ROOT / "docs/visual-assets/manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(manifest, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
