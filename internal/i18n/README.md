# 站点语言包

语言包提供界面词条加载、查询、回退和命名参数替换。核心和 Starter 均内置简体中文、繁体中文、英文、日文；主题可在自己的 `locales/` 目录提供补充或覆盖。每次页面渲染读取站点设置中的 `identity.locale`，与 HTML 的站点语言保持一致。请求 Cookie、账号语言和 `Accept-Language` 不自动切换站点语言。

## 覆盖范围

语言包应覆盖程序提供的界面固定文案，按实际呈现位置接入：

| 范围 | 应包含的文字 |
| --- | --- |
| 公共页面与主题 | 默认导航、搜索、分页、评论表单、空状态、404/维护提示、系统生成的页面标题和说明 |
| 账号与业务模块 | 登录、注册、密码找回、账号设置，以及圈子、通知、商城等模块的固定标签、状态和操作提示 |
| 管理后台 | 菜单、编辑器、设置字段名称与说明、权限与校验提示、保存/删除/安装等操作反馈 |
| 浏览器交互与无障碍 | 弹窗确认、加载/成功/失败状态、表单校验、按钮文字、占位文字、title、aria-label、读屏提示 |
| 系统通知 | 程序生成的邮件、短信和站内通知模板；收件人语言选择需由发送流程显式决定 |

文章标题与正文、评论内容、用户昵称、自定义菜单名称、站点标题、运营者填写的维护/隐私提示等属于内容数据，保留原文。需要多语言内容时应单独管理内容版本。API 字段名、错误码、路由、权限标识和语言包键名保持稳定，只翻译给人看的说明。

**当前接入范围：** 核心通用词条和 Starter 模板中的固定文案已经提供四语；Chunnel、核心后台、账号/业务页面、浏览器脚本和通知模板仍有硬编码文案，需逐处迁移。Starter 接收的服务端页面标题、业务错误和站点自定义提示也不会因添加语言包而自动翻译。只有显式调用 `t`、Go 查询接口或读取字典的文案参与翻译，不能把四语语言包视为全站文案已迁移完成。

现有主题可继续不提供语言包。当前接口不包含上传管理、在线自动翻译、复数规则引擎或按语言格式化日期/数字的能力；时间显示仍遵循现有站点时区设置。

## 内置语言与设置

在后台「站点设置 → 站点身份 → 站点语言」可通过站点统一下拉框选择以下语言。通过配置导入的其他合法 BCP 47 代码也会保留为当前选项，供扩展主题使用。保存后，已接入的文案在后续请求使用所选语言。

| 语言 | 建议值 / 包名 | 已验证的地区代码回退 |
| --- | --- | --- |
| 简体中文 | `zh-CN` | 默认语言 |
| 繁體中文 | `zh-Hant` | `zh-TW`、`zh-HK`、`zh-MO` → `zh-Hant` |
| English | `en` | `en-US`、`en-GB` → `en` |
| 日本語 | `ja` | `ja-JP` → `ja` |

繁体使用独立译文，保留与简体不同的用词；不在渲染时机械转换。同一组件的四种语言包具有相同的键与参数占位符，测试直接检查原始字典，避免回退掩盖漏译。第三方主题可提供部分词条并使用正常回退机制。

## 主题语言包

目录示例：

```text
my-theme/
  theme.json
  locales/
    zh-CN.json
    zh-Hant.json
    en.json
    ja.json
  templates/
    base.html
    home.html
```

在 `theme.json` 中可声明 `"default_locale": "zh-CN"`；省略时默认 `zh-CN`。提供 `locales/` 或显式声明默认语言后，必须包含对应的默认语言包。目录只接受 JSON 文件，不接受子目录或链接。

`locales/en.json`：

```json
{
  "schema": 1,
  "locale": "en",
  "messages": {
    "theme.welcome": "Welcome to {name}",
    "navigation.home": "Home"
  }
}
```

文件名语言标识与 `locale` 规范化后必须相同。语言标识沿用站点的受限 BCP 47 形式（最长 35 字节，如 `en-US`、`zh-Hant`），并由 `golang.org/x/text/language` 校验与规范化；不接受下划线、路径、空白、未知语言或 `und`。规范化后重复的语言包会被拒绝。

词条键匹配 `[A-Za-z][A-Za-z0-9_.-]{0,127}`，值必须是字符串；空字符串是有效译文。每个目录最多 32 个语言包、总计 8 MiB；单个 JSON 最多 512 KiB、4096 个词条，单条译文最多 8192 字节。未知字段、重复 JSON 字段、无效 Unicode、未配对代理项、`null` 词条、尾随 JSON、控制字符（换行、回车、制表符除外）和超限输入均拒绝。

语言包在主题加载、安装、激活、更新和回退时校验，并与对应模板作为同一快照生效。校验失败保留运行中的旧快照；查询和渲染不读磁盘。修改已安装文件后需重新加载主题或重启；发布更新请使用正常主题包流程。

## 模板调用

```gotemplate
<a href="/">{{t "navigation.home"}}</a>
<p>{{t "theme.welcome" (dict "name" .Site.Title)}}</p>
<h2>{{t "comments.title" (dict "count" .Comments.Total)}}</h2>
```

`t` 可接收一个可选参数字典，以字面的 `{name}` 替换参数；名称匹配 `[A-Za-z][A-Za-z0-9_]{0,63}`。缺少参数保留占位符，替换结果不再次解析。单条参数替换结果最多 64 KiB，超限返回渲染错误。译文和参数返回普通字符串，继续经过 `html/template` 的上下文转义；不要将译文传入 `safeHTML`。词条不是 Go 模板，不执行 `{{...}}`，也不按 `printf` 解释百分号。

模板函数对 `public/v1`、旧公共主题及核心模板都可用。在 `range`、`with` 和 partial 内也使用同一次渲染捕获的语言，不依赖当前的点对象。核心模板仅使用核心语言包，活动主题的覆盖不会进入核心后台或回退页面。

每个键独立按以下顺序查找：

1. 请求语言及其上级语言，在每个语言层级先查主题，再查核心。
2. 主题默认语言及其上级语言。
3. 核心默认 `zh-CN` 及其上级语言。
4. 仍找不到时，模板返回键名。

上级语言使用 `language.Tag.Parent()`，例如 `en-US → en`、`zh-TW → zh-Hant`、`ja-JP → ja`，保留文字系统差异，不做任意语言的模糊匹配。英文通用词条优先于主题的中文默认词条；主题缺少某一词条不影响其余词条的翻译。

## 只读 HTTP 接口

```http
GET /api/v1/translations
GET /api/v1/translations?locale=en-US
GET /api/v1/translations?locale=zh-TW
GET /api/v1/translations?locale=ja-JP
GET /api/v1/translations?locale=en-US&key=comments.title
HEAD /api/v1/translations?locale=en-US
```

省略 `locale` 使用本次请求的站点语言；省略 `key` 返回完整的有效字典，指定 `key` 只返回一个词条。`locales` 是主题与核心实际提供的语言包标识合集，按名称排序；`locale` 是请求语言，可能通过回退得到译文。接口不做参数替换，保留 `{count}` 等占位符。

```json
{
  "schema": 1,
  "locale": "en-US",
  "default_locale": "zh-CN",
  "theme": "starter",
  "locales": ["en", "ja", "zh-CN", "zh-Hant"],
  "messages": {"comments.title": "Comments ({count})"}
}
```

成功响应采用 `Cache-Control: public, no-cache` 与内容摘要 ETag，支持强/弱 `If-None-Match` 和 `304`。切换站点语言、主题或有效词条后响应摘要随内容变化。`HEAD` 返回与 `GET` 相同的表示头，无正文。未知、重复或非法参数返回 `400 request.invalid_query`，不存在的单个词条返回 `404 translations.not_found`，不支持的方法返回 `405` 和 `Allow: GET, HEAD`。错误使用现有 `error` 格式且 `no-store`；站点维护模式下返回 `503 site.maintenance`。接口匿名只读，不提供磁盘路径和主题设置。

浏览器使用普通 JSON 获取并按纯文本展示，例如：

```js
const response = await fetch('/api/v1/translations?locale=en-US');
if (!response.ok) throw new Error('Language pack unavailable');
const pack = await response.json();
homeLink.textContent = pack.messages['navigation.home'] ?? 'Home';
```

## Go 接入

`LoadDirectory` 用于本地受校验目录，`LoadFS` 用于可信的 `embed.FS`。加载返回不可变 `Catalog`；通过 `NewTranslator(locale, themeCatalog, Core())` 绑定请求语言。`Lookup` 返回文本和是否找到，`Text` 进行可选参数替换，`Messages` 和 `Locales` 返回独立副本。运行时遇到缺失或无效的站点语言时使用 `zh-CN`；显式 HTTP 查询参数和包内语言标识则严格报错。

回归测试：

```sh
go test ./internal/i18n ./internal/theme ./internal/httpserver
go run ./cmd/themecheck --source themes/starter
```
