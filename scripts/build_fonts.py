"""Build pinned OFL fonts as WOFF2; requires fonttools[woff].

Only needed when rebuilding font assets, not for normal application builds.
Upstream TTF files and licenses are downloaded from immutable Google Fonts commits.
"""
from io import BytesIO
from pathlib import Path
import hashlib
import json
import base64
from urllib.request import urlopen
from fontTools.ttLib import TTFont

ROOT = Path(__file__).resolve().parents[1]
DESTINATIONS = [ROOT / 'themes/chunnel/assets/fonts', ROOT / 'internal/coreui/assets/fonts']
FAMILIES = [
    ('inter', '0b58fb370093f9a9f4ff785d94405710b79de67c', [
        ('Inter%5Bopsz,wght%5D.ttf', 'inter-variable.woff2'),
        ('Inter-Italic%5Bopsz,wght%5D.ttf', 'inter-italic-variable.woff2'),
    ]),
    ('barlowsemicondensed', '3218ae2acc5bce7ccd97c477965b5b4094241fb0', [
        (f'BarlowSemiCondensed-{style}.ttf', f'barlow-semi-condensed-{style.lower()}.woff2')
        for style in ['Regular', 'Medium', 'SemiBold', 'Bold', 'ExtraBold', 'Black']
    ]),
]


def main():
    records = []
    for family, commit, fonts in FAMILIES:
        base = f'https://raw.githubusercontent.com/google/fonts/{commit}/ofl/{family}/'
        def download(name):
            url = f'https://api.github.com/repos/google/fonts/contents/ofl/{family}/{name}?ref={commit}'
            return base64.b64decode(json.load(urlopen(url, timeout=60))['content'])
        license_text = download('OFL.txt')
        for destination in DESTINATIONS:
            destination.mkdir(parents=True, exist_ok=True)
            (destination / f'{family}-OFL.txt').write_bytes(license_text)
        for source, target in fonts:
            original = download(source)
            font = TTFont(BytesIO(original), recalcTimestamp=False)
            font.flavor = 'woff2'
            output = BytesIO()
            font.save(output)
            data = output.getvalue()
            encoded = TTFont(BytesIO(data))
            assert font.getBestCmap() == encoded.getBestCmap()
            assert font['hmtx'].metrics == encoded['hmtx'].metrics
            for destination in DESTINATIONS:
                (destination / target).write_bytes(data)
            records.append({'file': target, 'source': base + source,
                            'source_sha256': hashlib.sha256(original).hexdigest(),
                            'sha256': hashlib.sha256(data).hexdigest(),
                            'license': f'{family}-OFL.txt'})
            print(f'{target}: {len(data)} bytes')
    for destination in DESTINATIONS:
        (destination / 'fonts-manifest.json').write_text(
            json.dumps({'fonts': records}, indent=2) + '\n', encoding='utf-8')


if __name__ == '__main__':
    main()
