# NewPress 主题开发规范

新安装仅预装并默认使用 Chunnel。`starter`、`starter-child` 是开发示例，其他主题按需从主题市场或 ZIP 安装；源码目录中存在主题不表示部署时应自动安装。原生部署和 Docker 均保留已有站点安装的主题及其活动选择。

## 前台与核心边界

主题只提供前台模板、设置、翻译和浏览器资源。后台、编辑器、路由、权限、数据库及业务接口由核心提供；独立主题无需安装 Chunnel。主题不能注入管理模板、服务端代码、SQL 或安装脚本。

展示字段以 [public/v1 数据结构](../internal/presentation/page.go)及同包组件为准；加载与校验见 [主题包](../internal/theme/)。模板使用 Go `html/template` 的上下文转义，`safeHTML` 仅用于核心已净化的 HTML，不把整个页面对象直接序列化给脚本。未提供的公共系统页面由核心回退；后台始终由核心渲染。

## 独立主题

复制 [Starter](starter/README.md)，同时修改目录名、slug、名称、作者、版本和资源。安装包根目录包含 `theme.json`、完整 `LICENSE`、`templates/` 和所需资源；可选 `settings.schema.json`、`locales/` 与第三方许可说明。

```json
{
  "schema": 1,
  "slug": "my-theme",
  "name": "My Theme",
  "version": "1.0.0",
  "description": "A NewPress public theme",
  "author": "Example",
  "license": "Apache-2.0",
  "engine": "go-template/v1",
  "newpress": ">=0.1.0",
  "view": "public/v1",
  "templates": {"home": "home.html", "single": "single.html"},
  "settings_schema": "settings.schema.json"
}
```

`home` 和 `single` 必填，并提供它们使用的基础布局。模板路径相对于 `templates/`，设置 schema 相对于主题根目录。独立主题声明 `public/v1`；`chunnel/v1` 仅供 Chunnel 及其直接子主题使用。需要共享首页布局时显式声明 `"capabilities": ["home-layout/v1"]` 并实现对应展示；不需要时省略。

清单使用严格 JSON，不能包含未知字段、重复键或尾随文档。包内路径使用规范的正斜杠相对路径，不能包含反斜杠、点段、符号链接、目录逃逸或大小写冲突。不要将运行目录的私有状态打包。

## 单层子主题

[Starter 子主题示例](starter-child/README.md)只维护差异。使用 schema 2，并声明自己的身份、版本、engine、view 及父版本范围：

```json
{
  "schema": 2,
  "slug": "my-child",
  "name": "My Child Theme",
  "version": "1.0.0",
  "engine": "go-template/v1",
  "view": "public/v1",
  "newpress": ">=0.1.0",
  "parent": {"slug": "starter", "version": ">=1.0.0 <2.0.0"}
}
```

- 父主题须已安装且自身没有父主题；不自动下载依赖，不支持多级继承。engine/view 必须一致；省略 capabilities 时继承，显式声明时必须与父主题集合相同。
- 页面映射按键覆盖；模板与资源按相同相对路径覆盖。无效的子文件会使加载失败，不会静默使用父文件。不能在不同文件中定义同名非空模板来任意覆盖父模板。
- 父主题已调用 `partials/child-head.html` 或 `partials/child-body-end.html` 扩展槽时，可覆盖它们并用 `asset` 加载追加 CSS/JS，无需复制基础布局。Starter 页脚可通过 `partials/footer.html` 覆盖。旧父包没有调用扩展槽时应先更新父包。
- 资源 URL 使用子 slug 与有效内容摘要。翻译按语言和键合并后执行语言回退；不要手写父资源 URL。
- 设置按子 slug 独立保存，不共享父主题已保存值。schema 增量补丁对继承字段仅能改 default、title、description；新增字段须完整定义，不能改变父字段类型、约束、CSS 映射或控件语义。required 取并集，新增 UI 分组不能与父分组 ID 冲突。
- 父升级、回滚会校验所有已安装子主题，包括未激活者；不兼容时拒绝切换。有子主题依赖时不能卸载父主题。SemVer 范围不保证 DOM 或视觉兼容，仍须测试实际页面。

## 构建、检查和打包

从源码仓库根目录运行，Node 仅用于开发构建，站点运行不需要 Node：

```powershell
npm ci --ignore-scripts
npm run build:theme -- --source themes/starter
npm run check:theme -- --source themes/starter
go run ./cmd/themecheck --source themes/starter-child --themes-dir themes
python scripts/build_extension.py --kind theme --source themes/starter --output output/starter-1.0.0.zip
python scripts/build_extension.py --kind theme --source themes/starter-child --output output/starter-child-1.0.0.zip
```

可选构建器将 `assets/**/*.source.css/js` 构建到同目录的 CSS/JS；用 `--check` 检查产物一致性。主题可使用自己的工具链。Chunnel 使用仓库组合构建，见 [Chunnel 资源开发](chunnel/DEVELOPMENT.md)。

`themecheck` 的 public/v1 夹具包含空内容、文章、预览、模块停用、完整组件和需要登录的评论。Chunnel 及其子主题使用 `--structure-only` 检查结构，再运行 HTTP 与浏览器测试；结构通过不表示页面已实际渲染。

打包输出须位于源码目录外，且不能覆盖已有文件。子包只包含自己的差异，不携带父包。包内保留完整许可证及第三方素材各自许可，不包含 `data/`、`.official.json`、`.integrity.json`、`.disabled`、回滚目录或其他站点状态。正式发行使用新版本及新 ZIP 路径，不覆盖旧签名包。

## 安装与验收

先安装兼容父包，再安装子包，例如 `newpress theme install ./output/starter-1.0.0.zip` 和 `newpress theme install ./output/starter-child-1.0.0.zip`。安装后通过后台主题市场选择；安装不会自动激活。已有保存的活动选择优先于 `NEWPRESS_ACTIVE_THEME`，不要靠修改环境变量覆盖用户选择。

开发和测试使用独立的数据、主题、插件目录；运行站点与 CLI 不能同时占用这些目录。修改模板或资源后重新构建并重载，避免绕过资源摘要和包完整性检查。发布前检查桌面与手机、键盘操作、空状态、模块关闭、未登录、评论限制、设置保存/重置、父包升级及回滚，并运行相关 Go、前端与 HTTP 测试。

公开使用和开发说明应放在随源码提供的 README 或本规范中，安装包的必要说明写在包内。维护者私有目录不作为公开开发的前置依赖。
