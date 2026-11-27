"""Prepare Chunnel's generated brand mark for the web (requires Pillow).

The generated original is a full-bleed square. This script finds the emblem,
centres it with even margins, and writes the rounded, alpha-aware sizes used
by the header, footer, administration pages and browsers.
"""

import argparse
import json
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageStat


ROOT = Path(__file__).resolve().parents[1]
DESTINATION = ROOT / "themes/chunnel/assets/images/brand"
SOURCE_DIR = ROOT / "output/imagegen/brand-20261112"
MANIFEST = ROOT / "docs/visual-assets/brand-manifest.json"


def background(image: Image.Image) -> tuple[int, int, int]:
    width, height = image.size
    samples = []
    for box in (
        (0, 0, 24, 24),
        (width - 24, 0, width, 24),
        (0, height - 24, 24, height),
        (width - 24, height - 24, width, height),
    ):
        samples.append(ImageStat.Stat(image.crop(box)).median)
    return tuple(round(sum(channel) / len(channel)) for channel in zip(*samples))


def emblem_box(image: Image.Image, bg: tuple[int, int, int]) -> tuple[int, int, int, int]:
    difference = ImageChops.difference(image, Image.new("RGB", image.size, bg)).convert("L")
    mask = difference.point(lambda value: 255 if value > 35 else 0)
    box = mask.getbbox()
    if box is None:
        raise SystemExit("no emblem found against the sampled background")
    return box


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, default=ROOT / "output/imagegen/brand-20261112/logo-mark.png")
    args = parser.parse_args()
    DESTINATION.mkdir(parents=True, exist_ok=True)

    with Image.open(args.input) as original:
        image = original.convert("RGB")
    bg = background(image)
    left, top, right, bottom = emblem_box(image, bg)
    width, height = right - left, bottom - top
    # Keep the emblem at roughly 70 percent of the square so the rounded tile
    # keeps a calm margin at 32 px while the lit arch stays legible.
    side = round(max(width, height) / 0.70)
    centre_x, centre_y = (left + right) // 2, (top + bottom) // 2
    crop_left, crop_top = centre_x - side // 2, centre_y - side // 2
    canvas = Image.new("RGB", (side, side), bg)
    canvas.paste(image, (-crop_left, -crop_top))
    master = canvas.resize((1024, 1024), Image.Resampling.LANCZOS)
    master_path = SOURCE_DIR / "logo-mark-1024.png"
    master.save(master_path, "PNG", optimize=True)

    variants = []
    rounded_master = master.convert("RGBA")
    mask = Image.new("L", master.size, 0)
    ImageDraw.Draw(mask).rounded_rectangle(
        [0, 0, master.width - 1, master.height - 1], radius=round(master.width * 0.22), fill=255
    )
    rounded_master.putalpha(mask)
    for size in (168, 84, 64):
        resized = rounded_master.resize((size, size), Image.Resampling.LANCZOS)
        path = DESTINATION / f"logo-mark-{size}.webp"
        resized.save(path, "WEBP", quality=92, method=6, exact=True)
        variants.append({"file": path.relative_to(ROOT / "themes/chunnel/assets").as_posix(), "width": size, "height": size, "bytes": path.stat().st_size})

    favicon = rounded_master.resize((64, 64), Image.Resampling.LANCZOS)
    favicon.save(DESTINATION / "favicon.png", "PNG", optimize=True)
    variants.append({"file": "images/brand/favicon.png", "width": 64, "height": 64, "bytes": (DESTINATION / "favicon.png").stat().st_size})

    touch = master.resize((180, 180), Image.Resampling.LANCZOS)
    touch.save(DESTINATION / "apple-touch-icon.png", "PNG", optimize=True)
    variants.append({"file": "images/brand/apple-touch-icon.png", "width": 180, "height": 180, "bytes": (DESTINATION / "apple-touch-icon.png").stat().st_size})

    favicon_vector = ROOT / "themes/chunnel/assets/images/favicon.svg"
    variants.append({"file": "images/favicon.svg", "width": 64, "height": 64, "vector": True, "bytes": favicon_vector.stat().st_size})

    manifest = {
        "collection": "Chunnel 品牌标记 · Tunnel of Light",
        "model": "gpt-image-2.6-sunburst",
        "generated": "2026-11-12",
        "source": args.input.relative_to(ROOT).as_posix(),
        "background": "#%02x%02x%02x" % bg,
        "variants": variants,
    }
    MANIFEST.parent.mkdir(parents=True, exist_ok=True)
    MANIFEST.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(manifest, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
