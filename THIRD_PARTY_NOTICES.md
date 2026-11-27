# Third-Party Notices

NewPress's original code remains licensed under Apache-2.0. Third-party code and fonts retain their own licenses; the root license does not replace them. Project illustrations are AI-generated and do not carry individual source attributions.

Go modules are listed in `go.mod` and locked by `go.sum`. `scripts/collect_licenses.py` collects the full license and notice texts from those exact module sources, the Go toolchain, and the bundled UI/artwork notices. The generated `internal/licensing/THIRD_PARTY_LICENSES.txt` is embedded in the executable and available with `newpress licenses`; `internal/licensing/GO_MODULES.json` records the module versions and notice hashes. Run the collector after dependency or notice changes, then use `--check` before release. This inventory preserves supplied texts; it is not a legal opinion or a full asset-rights audit.

## In-tree source redistribution

### golang.org/x/crypto v0.58.0

NewPress redistributes a minimal, unmodified source subset of the Go `x/crypto` module required for Argon2id:

- `third_party/xcrypto/argon2`
- `third_party/xcrypto/blake2b`

This local copy allows authentication builds in restricted/offline environments and is selected by the `replace` directive in `go.mod`. It remains third-party code and is not relicensed under NewPress's Apache-2.0 license.

Copyright 2009 The Go Authors. All rights reserved.

License: BSD 3-Clause. The complete upstream license text is preserved at `third_party/xcrypto/LICENSE`:

> Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:
>
> * Redistributions of source code must retain the above copyright notice, this list of conditions and the following disclaimer.
> * Redistributions in binary form must reproduce the above copyright notice, this list of conditions and the following disclaimer in the documentation and/or other materials provided with the distribution.
> * Neither the name of Google LLC nor the names of its contributors may be used to endorse or promote products derived from this software without specific prior written permission.
>
> THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

## Image processing modules

`github.com/HugoSmits86/nativewebp v1.3.0` is used for pure Go lossless WebP encoding. Copyright (c) 2024 Hugo Smits; MIT license. The complete upstream license is retained in `third_party/licenses/nativewebp-MIT.txt`.

`golang.org/x/image v0.47.0` supplies resampling and WebP decoding. Copyright 2009 The Go Authors; BSD 3-Clause license. The complete upstream license is retained in `third_party/licenses/x-image-BSD.txt`. Both dependencies are pinned in `go.mod` and verified by `go.sum`; their source is not vendored.

## Theme asset notices

Chunnel carries separate font and visual-asset notices in `themes/chunnel/THIRD_PARTY_NOTICES.md`. A theme's manifest license applies to its code unless that notice explicitly says otherwise.

The embedded administration UI has its own copy at `internal/coreui/THIRD_PARTY_NOTICES.md`. Inter and Barlow Semi Condensed are distributed under SIL Open Font License 1.1, with complete license texts and pinned source/hash manifests in each `assets/fonts/` directory. The former B2 font files have been removed. Chinese system fonts are referenced by family name only and are not redistributed.

Nexora and DocsHub retain the SIL Open Font License 1.1 text with their Noto font subsets. See each theme's `THIRD_PARTY_NOTICES.md` and `assets/fonts/OFL.txt`.

## Comment-origin artwork

The host implementation bundles flag-icons artwork under the MIT license. Its complete upstream notice is preserved at `internal/commentorigin/flags/LICENSE`, with the source version recorded in that directory's `README.md`. This is host artwork, not an asset included in the small plugin descriptor ZIP.

## GPT-generated comment stickers

The former Bilibili emoji images were removed on 2026-11-23. The replacement
60 PNG stickers were supplied by the project owner, who confirmed GPT image
generation; the whale character references unofficial DeepSeek community fan
art. These images are separate from the Apache-2.0 code license. The owner
selected CC BY 4.0 for rights they may grant, with attribution to
“NewPress 项目贡献者”. Redistribution, adaptation and commercial use are allowed
subject to attribution, a license link and indication of changes under that license.
Third-party character/mark rights are not granted or cleared. See
`third_party/licenses/emoji-NOTICE.txt`, and the `LICENSE.txt` and `manifest.json`
files shipped in each `assets/images/emoji/` directory for provenance and scope.
No official affiliation is claimed.

## Browser build and analytics dependencies

TypeScript 7.0.2 (Apache-2.0) is a development-only compiler; deployment still uses
prebuilt assets. Terser 5.51.4 (BSD-2-Clause) is a development-only compressor for
the homepage bundles; its implementation is not included in browser assets.
React, React DOM and Scheduler (MIT), Apache ECharts and zrender
(Apache-2.0), and tslib (0BSD) are bundled only in the interactive analytics entry.
Exact versions are pinned in `package-lock.json`. The frontend build extracts the
complete license/NOTICE texts, including ECharts' additional license files, from
the actual esbuild dependency graph into `internal/licensing/FRONTEND_LICENSES.txt`.
`newpress licenses` includes these notices; `check:frontend` verifies their freshness.

## Distribution

Every independently built official theme/plugin includes its own full `LICENSE` and applicable third-party notices. Previously signed archives remain byte-for-byte unchanged; corrected archives require a new version and signed catalog. See [release preparation](RELEASING.md) and [contribution licensing](CONTRIBUTING.md#licenses) for the source and release requirements.
