# NewPress Starter

独立的 `public/v1` 主题：只包含前台模板、设置和样式。无需安装 Chunnel。后台始终由程序提供，缺少的账号、结算及业务模块页面采用核心默认界面。

本目录是开发示例，不在新站点默认安装。完整要求见源码仓库中的 [主题开发规范](../README.md)，差异覆盖参考 [子主题示例](../starter-child/README.md)。

从项目根目录运行：

```powershell
npm run build:theme -- --source themes/starter
npm run check:theme -- --source themes/starter
python scripts/build_extension.py --kind theme --source themes/starter --output output/starter-1.0.0.zip
```

复制目录后修改 manifest 的 slug、名称和版本；设置文件按 slug 隔离。可选模板参考本示例及 `internal/theme/` 的校验规则，公共字段见 [展示契约](../../internal/presentation/page.go)；该包不依赖 HTTP 处理器、身份或数据库。检查器提供空内容、文章、预览、模块停用、完整组件及需要登录的评论夹具。

此示例首页只获取文章列表。需要现有可视化布局时显式声明 `home-layout/v1`，并实现该协议的展示。浏览器增强可按需加载 `/core-assets/js/newpress-client.js`，通过 `NewPressClient.create()` 使用请求、会话与 CSRF 接口；模板无需依赖 Chunnel 的 DOM 或全局对象。

## 语言包

模板固定文案通过 `{{t "navigation.home"}}` 查询，带参数的词条使用 `{{t "comments.title" (dict "count" .Comments.Total)}}`。`locales/` 提供 `zh-CN`（简体中文）、`zh-Hant`（繁體中文）、`en`（English）、`ja`（日本語）四种主题词条，其余通用词条来自核心的对应语言包。后台站点语言字段提供四种建议值并允许输入其他代码；`zh-TW` / `zh-HK` 回退到繁体包，`en-US` 回退到英文包，`ja-JP` 回退到日文包。缺失词条继续回退到主题默认 `zh-CN` 和核心词条。

`theme.json` 的 `default_locale` 指定主题默认语言。复制主题后可添加同格式 JSON；更新包会同时校验并切换模板和词条。前端脚本可通过 `/api/v1/translations` 获取当前语言字典，或指定 `locale` 与 `key` 查询。文章、菜单自定义文字、服务端页面标题和业务错误提示保留其原文。Starter 的四语模板不代表核心账号页面、后台或其他主题均已迁移；覆盖边界、完整格式、回退顺序和接口约束见[语言包说明](../../internal/i18n/README.md)。

## 许可证

Starter 代码采用 [Apache-2.0](LICENSE)，独立打包时保留完整 `LICENSE` 与 [第三方说明](THIRD_PARTY_NOTICES.md)。此示例没有随包字体或默认摄影图片；复制后新增的第三方素材应保留各自来源和许可证，不能直接用主题的 Apache-2.0 覆盖其原许可。
