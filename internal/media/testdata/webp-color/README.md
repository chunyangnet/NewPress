# WebP color references

Synthetic images generated locally by `generate.py`. These are original test
assets, not downloaded photographs. The paired PNGs are decoded by Pillow /
libwebp, with EXIF orientation applied and metadata removed. `manifest.json`
records library versions and file hashes. Go tests do not invoke Python.

The lossy images exercise saturated colors, black/white/gray, odd dimensions,
partial and full transparency, gradients, a one-pixel width and EXIF rotation.
The lossless pair guards against applying video-range conversion twice.
Color comparison permits resampling and JPEG compression differences; libwebp
also interpolates chroma differently from Go's nearest chroma sample.
