# Chunnel third-party font notice

Original UI code is Apache-2.0. Bundled Inter and Barlow Semi Condensed fonts
are licensed separately under SIL Open Font License 1.1.

- Inter: `assets/fonts/inter-OFL.txt`.
- Barlow Semi Condensed: `assets/fonts/barlowsemicondensed-OFL.txt`.
- Pinned upstream URLs and source/output SHA-256 hashes: `assets/fonts/fonts-manifest.json`.

`scripts/build_fonts.py` converts the upstream fonts to WOFF2 without subsetting
or changing glyph metrics. Chinese text uses installed system fonts, which are
not redistributed. The former B2 font files have been removed.

## Local icon system

The UI bundles SVG geometry from lucide-static 1.75.0. The pinned subset and
legacy-ID aliases are stored in third_party/lucide/icons.json in the source
repository; scripts/build_icons.mjs generates the local sprite. No icon font,
CDN, runtime package or network request is needed. Heart and star filled states
reuse the corresponding outlines with a solid fill. All other paths are kept
from upstream. The complete upstream license follows, including the Feather
attribution for derived icons.

```text
ISC License

Copyright (c) 2026 Lucide Icons and Contributors

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

---

The following Lucide icons are derived from the Feather project:

airplay, alert-circle, alert-octagon, alert-triangle, aperture, arrow-down-circle, arrow-down-left, arrow-down-right, arrow-down, arrow-left-circle, arrow-left, arrow-right-circle, arrow-right, arrow-up-circle, arrow-up-left, arrow-up-right, arrow-up, at-sign, calendar, cast, check, chevron-down, chevron-left, chevron-right, chevron-up, chevrons-down, chevrons-left, chevrons-right, chevrons-up, circle, clipboard, clock, code, columns, command, compass, corner-down-left, corner-down-right, corner-left-down, corner-left-up, corner-right-down, corner-right-up, corner-up-left, corner-up-right, crosshair, database, divide-circle, divide-square, dollar-sign, download, external-link, feather, frown, hash, headphones, help-circle, info, italic, key, layout, life-buoy, link-2, link, loader, lock, log-in, log-out, maximize, meh, minimize, minimize-2, minus-circle, minus-square, minus, monitor, moon, more-horizontal, more-vertical, move, music, navigation-2, navigation, octagon, pause-circle, percent, plus-circle, plus-square, plus, power, radio, rss, search, server, share, shopping-bag, sidebar, smartphone, smile, square, table-2, tablet, target, terminal, trash-2, trash, triangle, tv, type, upload, x-circle, x-octagon, x-square, x, zoom-in, zoom-out

The MIT License (MIT) (for the icons listed above)

Copyright (c) 2013-present Cole Bemis

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## 中文授权范围说明

主题代码继续采用 Apache-2.0，B2 字体及其 WOFF2 转换文件不属于该代码许可。上面的日期记录项目所有者曾确认本项目使用权，当前说明未附原始授权条款，不能据此确认该授权包含向公众重新分发字体或允许接收者在其他项目中使用。

公开分发含这些字体的主题包前，发布者应核实原始授权是否覆盖随软件分发、格式转换及预期的使用范围；缺少依据时应替换为具有相应权限的字体并更新本表。授权凭据留在仓库外妥善保存，公开说明可记录可核验的许可或凭据编号。本说明保留现有来源与哈希，不新增、扩大或撤回原有授权；随包保留 `LICENSE`、本说明与适用的第三方许可。

## GPT 生成评论表情 / GPT-generated comment stickers

`assets/images/emoji/` 内的 60 张 PNG 由项目所有者提供，并确认使用 GPT 图像模型生成。日常组为黄色表情；鲸伴组参考 DeepSeek 民间社区二创形象，属于非官方素材，不表示 DeepSeek、OpenAI 或参考作者的授权与背书。

这些图片采用 CC BY 4.0，署名为“NewPress 项目贡献者”，不适用代码的 Apache-2.0。许可允许复制、修改、商业使用和再分发，须按条款署名、提供许可链接并注明修改；仅覆盖许可方有权授予的权利，不授予或确认第三方角色、标志或参考作品的权利。

完整许可见 `assets/images/emoji/LICENSE.txt`，原图及成品哈希见同目录 `manifest.json`。旧 B 站表情图片已于 2026-11-23 移除。保留现有 AI 来源标识与说明。
