"""Prepare NewPress's AI-native product imagery from generated PNG originals (requires Pillow)."""

import argparse
import base64
import io
import json
from pathlib import Path

from PIL import Image, ImageOps


ROOT = Path(__file__).resolve().parents[1]
IMAGES = ROOT / "themes/chunnel/assets/images"
DESTINATION = IMAGES / "editorial"
COLLECTION = "暗房纪事 · Darkroom Editorial"
GENERATED = "2026-11-13"
MODEL = "gpt-image-2.6-sunburst"

# Each source is a square or wide render. The editorial slots keep their
# historical file names so stored posts, settings and templates stay valid.
ASSETS = {
    "cover-library": {"source": "hero-platform", "title": "混凝土模块建筑", "crop": "landscape", "small": 768},
    "cover-notes": {"source": "creator-studio", "title": "陶艺工作台", "crop": "landscape", "small": 768},
    "cover-print": {"source": "resource-library", "title": "翡翠釉面砖阵列", "crop": "landscape", "small": 768},
    "cover-community": {"source": "community-network", "title": "陶土织物经纬", "crop": "landscape", "small": 768},
    "cover-stream": {"source": "media-stream", "title": "百叶窗光影剧照", "crop": "landscape", "small": 768},
    "reading-room": {"source": "ai-operations", "title": "模拟示波器波形", "crop": "portrait", "small": 640},
}

# Stored cover URLs still point at these SVGs. An <img> loading an SVG cannot
# fetch its external raster image, so embed the small WebP in the file.
LEGACY = {
    "cover-core.svg": {"source": "resource-library", "title": "翡翠釉面砖阵列"},
    "cover-chunnel.svg": {"source": "community-network", "title": "陶土织物经纬"},
    "cover-plugin.svg": {"source": "media-stream", "title": "百叶窗光影剧照"},
}

RATIOS = {"landscape": 3 / 2, "portrait": 2 / 3}
FULL_WIDTHS = {"landscape": 1536, "portrait": 1024}


def load(input_dir, name):
    with Image.open(input_dir / f"{name}.png") as original:
        return ImageOps.exif_transpose(original).convert("RGB")


def crop_to_ratio(image, ratio):
    width, height = image.size
    if width / height > ratio:
        new_width = round(height * ratio)
        left = (width - new_width) // 2
        return image.crop((left, 0, left + new_width, height))
    new_height = round(width / ratio)
    top = (height - new_height) // 2
    return image.crop((0, top, width, top + new_height))


def resize(image, width):
    return image.resize((width, round(image.height * width / image.width)), Image.Resampling.LANCZOS)


def webp_bytes(image):
    buffer = io.BytesIO()
    image.save(buffer, "WEBP", quality=82, method=6)
    return buffer.getvalue()


def legacy_svg(title, image):
    payload = base64.b64encode(webp_bytes(image)).decode("ascii")
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 720" role="img" aria-labelledby="title">\n'
        f'  <title id="title">{title}</title>\n'
        f'  <image width="1200" height="720" preserveAspectRatio="xMidYMid slice" href="data:image/webp;base64,{payload}"/>\n'
        '</svg>\n'
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    args = parser.parse_args()
    DESTINATION.mkdir(parents=True, exist_ok=True)
    manifest = {
        "collection": COLLECTION,
        "model": MODEL,
        "generated": GENERATED,
        "source": "output/imagegen/product-20261113-photo",
        "assets": [],
        "legacy": [],
    }
    for name, spec in ASSETS.items():
        image = load(args.input, spec["source"])
        ratio = RATIOS[spec["crop"]]
        image = crop_to_ratio(image, ratio)
        variants = []
        for suffix, width in [("", FULL_WIDTHS[spec["crop"]]), ("-small", spec["small"])]:
            variant = resize(image, width)
            path = DESTINATION / f"{name}{suffix}.webp"
            path.write_bytes(webp_bytes(variant))
            variants.append({
                "file": path.relative_to(IMAGES).as_posix(),
                "width": variant.width,
                "height": variant.height,
                "bytes": path.stat().st_size,
            })
        manifest["assets"].append({
            "id": name,
            "title": spec["title"],
            "source": spec["source"],
            "variants": variants,
        })
    for legacy, spec in LEGACY.items():
        image = resize(crop_to_ratio(load(args.input, spec["source"]), 3 / 2), 768)
        (IMAGES / legacy).write_text(legacy_svg(spec["title"], image), encoding="utf-8")
        manifest["legacy"].append({
            "file": legacy,
            "title": spec["title"],
            "source": spec["source"],
            "bytes": (IMAGES / legacy).stat().st_size,
        })
    (ROOT / "docs/visual-assets/manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    print(json.dumps(manifest, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
