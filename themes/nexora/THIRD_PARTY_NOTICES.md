# Nexora 素材与许可

模板、样式与交互代码保持 Apache-2.0，完整正文见 [LICENSE](LICENSE)。

## Noto Serif SC / Nexora Serif

`assets/fonts/nexora-serif.woff2` 是 Noto Serif SC 的 400 字重字符子集，内部名称改为 Nexora Serif。由 Google Fonts 源码生成，保留 SIL Open Font License 1.1，完整许可见 `assets/fonts/OFL.txt`。

Copyright 2012 Google Inc. All Rights Reserved.

原始字体来源：

```text
https://github.com/google/fonts/tree/main/ofl/notoserifsc
NotoSerifSC[wght].ttf
```

字体遵循其 OFL 许可，不属于主题代码的 Apache-2.0 许可。不包含 Chunnel 的 B2 字体。分发字体时保留 `assets/fonts/OFL.txt` 和现有字体版权声明。

## 图片

项目配图为 AI 生成素材，不另列来源署名。文件用途及维护说明见 [ASSETS.md](ASSETS.md)。

## 代码与图形

主题代码遵循 Apache-2.0。N 形标志和线性图标以 SVG 路径实现。`nexora-verification.js` 由仓库 `frontend/themes/nexora/verification.ts` 和共享 `verification-controller.ts` 生成，保留 NewPress 核心的 Apache-2.0 来源；使用 Nexora 自有全局命名和表单错误提示，不依赖安装后的 Chunnel 主题。

独立安装包保留 `LICENSE`、本说明和字体许可文件。

## Lucide / Feather 服务图标

`templates/partials/services-icons.html` 来自 NewPress 的 lucide-static 1.75.0 子集，包含 Feather 派生图标。

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

The MIT License (MIT) — Feather-derived icons
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
