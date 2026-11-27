# DocsHub 素材与许可

主题模板、样式和浏览器交互代码：NewPress Project，[Apache-2.0](LICENSE)。验证展示适配器由 Nexora / NewPress 核心的公共验证适配器改编，保留 Apache-2.0 许可，使用核心验证接口，不含供应商私密凭据。

## Noto Sans SC

Copyright 2014-2021 Adobe. Noto Sans SC 可变字体采用 SIL Open Font License 1.1，完整许可位于 `assets/fonts/OFL.txt`。

本地子集包含 GB2312、ASCII 与常用标点，保留原可变字重轴并转换为 WOFF2。上游版本为 2.004，字体来源为 `notofonts/noto-cjk` 的 `Sans/Variable/TTF/Subset/NotoSansSC-VF.ttf`；原文件 SHA-256 为 `d68bafcb48a2707749396aa12bbbd833cb70401f3a9a689fd2902c7e0d295964`。

默认样式不再引用该字体；随包保留字体文件时，仍保留 `assets/fonts/OFL.txt`、上述版权及字体来源说明。字体的 OFL 许可不由主题代码的 Apache-2.0 替代。

## 图片与图形

项目配图为 AI 生成素材，不另列来源署名。文件用途见 [ASSETS.md](ASSETS.md)。品牌标记与基础图标为主题内联 SVG；服务页面图标包含下述 Lucide / Feather 图形，不加载外部 CDN 或浏览器框架。

独立安装包保留 `LICENSE`、本说明和字体许可文件。

## NewPress 公共组件适配

服务页面模板、首页签到和许可同为 Apache-2.0 的 NewPress 默认主题公共组件改编为 DocsHub 展示层；轮播逻辑改编自本仓库 BauMeet 主题，保留 Apache-2.0。核心脚本由 NewPress 的 `/core-assets/` 提供，安装包不包含用户状态或服务凭据。

## Lucide / Feather 服务图标

`templates/partials/services-icons.html` 的 SVG 图形来自 NewPress 已固定的 lucide-static 1.75.0 子集，包含 Feather 派生图标。完整许可如下。

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
