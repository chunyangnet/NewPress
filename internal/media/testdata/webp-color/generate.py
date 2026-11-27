"""Regenerate independent WebP/PNG fixtures with Pillow's libwebp decoder.

Go tests read the checked-in files and do not need Python or Pillow.
"""
from pathlib import Path
import hashlib
import json

from PIL import Image, ImageOps, features, __version__

ROOT = Path(__file__).resolve().parent
COLORS = [(210, 38, 40), (40, 155, 52), (40, 64, 210),
          (208, 170, 38), (186, 42, 175), (38, 165, 190),
          (0, 0, 0), (128, 128, 128), (255, 255, 255)]


def palette(alpha=False):
    img = Image.new('RGBA' if alpha else 'RGB', (301, 201))
    for y in range(img.height):
        for x in range(img.width):
            rgb = COLORS[min(2, y // 67) * 3 + min(2, x // 101)]
            img.putpixel((x, y), rgb + (([0, 64, 128, 255][min(3, x // 75)],) if alpha else ()))
    return img


def gradient():
    img = Image.new('RGB', (257, 129))
    for y in range(img.height):
        for x in range(img.width):
            img.putpixel((x, y), (min(x, 255), y * 255 // 128, (x + y) * 255 // 384))
    return img


fixtures = [('palette', palette(), False, 1),
            ('alpha', palette(True), False, 1),
            ('alpha-rotated', palette(True), False, 6),
            ('gradient', gradient(), False, 1),
            ('lossless', palette(True), True, 1),
            ('narrow', Image.linear_gradient('L').resize((1, 17)).convert('RGB'), False, 1)]
records = []
for name, source, lossless, orientation in fixtures:
    exif = Image.Exif()
    exif[274] = orientation
    path = ROOT / (name + '.webp')
    source.save(path, format='WEBP', quality=90, method=6, lossless=lossless, exif=exif)
    with Image.open(path) as decoded:
        reference = ImageOps.exif_transpose(decoded).convert('RGBA')
        # Clear metadata; expected orientation is baked into the PNG pixels.
        Image.frombytes('RGBA', reference.size, reference.tobytes()).save(ROOT / (name + '.png'))
    records.append({'name': name, 'lossless': lossless, 'orientation': orientation,
                    'width': reference.width, 'height': reference.height})
# Compressed large inputs exercise the real decoder and bounded conversion in
# benchmarks; their decoded PNGs are unnecessary in the repository.
for name, alpha, orientation in [('large', False, 1), ('large-rotated', False, 6), ('large-alpha', True, 1)]:
    source = gradient().resize((3201, 1801), Image.Resampling.BICUBIC)
    if alpha:
        source.putalpha(Image.linear_gradient('L').resize(source.size))
    exif = Image.Exif()
    exif[274] = orientation
    source.save(ROOT / (name + '.webp'), format='WEBP', quality=85, method=6, exif=exif)
manifest = {'pillow': __version__, 'libwebp': features.version('webp'), 'fixtures': records,
            'sha256': {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                       for p in sorted(ROOT.glob('*')) if p.suffix in ('.png', '.webp')}}
(ROOT / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n', encoding='utf-8')
print('Generated', len(records), 'independent WebP / PNG pairs with Pillow', __version__)
