# Core UI

本包拥有后台模板、后台浏览器资源与公共系统页面的默认实现。文件通过 go:embed 随核心发布；不从活动主题读取模板、样式或默认设置。

- pages.json：核心页面入口。admin 和 admin-* 永远由核心渲染，公共入口仅在主题未提供时使用。
- templates/admin-base.html：独立后台基础模板。templates/public-base.html：公共默认页面基础模板。
- defaults.json 和 assets/css/core-defaults.css：核心外观快照，与活动主题 schema 和设置无关。修改两者应保持一致。
- assets/：本包资源。scripts/build_core_ui.mjs 读取本包源码、仓库级 frontend/ 的 TS 入口及声明的第三方构建输入，不读取安装主题；npm run build:frontend 汇总核心与 Chunnel 的构建，npm run check:frontend 检查产物。
- assets/js/newpress-client.js：公开可选请求/会话客户端，由 frontend/shared/newpress-client.ts 及全局适配器生成，不依赖本包 DOM 或 Chunnel UI；保留 NewPressClient v1 和既有资源 URL。
- assets/js/chunnel-selects.js 与 chunnel-choice-menu.js：由 frontend/shared/selects.ts、choice-menu.ts 生成，与 Chunnel 共用单份 TS 源码；choice-types.ts 定义按需菜单协议。修改选择逻辑时不要重建旧 .source.js 副本，配套 CSS 仍按现有源码构建。
- assets/js/chunnel-color-mode.js 与 chunnel-color-reveal.js：由 frontend/shared/color-mode.ts、color-reveal.ts 生成，配套类型位于 color-types.ts。前者保留样式前同步应用偏好，后者按用户交互加载；两端共用 TS 源码，保留全局 API、存储键和外观事件。
- assets/js/chunnel-admin-workbench.js：由 frontend/core/admin-workbench.ts 按顺序组合时间工具、admin-ui.ts、admin-navigation.ts 和 admin-feature-status.ts。非登录后台只加载此入口；登录继续使用独立时间工具。业务生效状态读取后端投影，保存后重读，并随列表局部更新重新挂载；状态判断与页面覆盖见 `../httpserver/feature_status.go` 及同包 `feature_status_*.go`。data-choice-entry 保留在合包 script 上，延迟出现的 select 仍可加载增强器。
- assets/js/chunnel-admin-media.js：按原顺序编译组合 frontend/core/media-picker.ts 与 media-fields.ts；media-data.ts 验证媒体 API 数据，media-types.ts 定义选择器协议。只在需要媒体选择器的后台页加载。独立 admin-ui、admin-navigation、media-fields 资源仍由 TS 清单生成，不能手改；旧 chunnel-editor-media.js 及 admin-ui/media-fields.source.js 已移除。
- THIRD_PARTY_NOTICES.md：原项目已授权素材与图标声明。抽离不改变第三方素材授权。
- 资源编辑/加载器、SEO、内容复用、文章卡片及角标由 frontend/core/ 下的六个 TS 入口生成；对应旧 .source.js 已移除。resource-data.ts 校验配置和金额，editor-resource-selection.ts 为连续编辑器提供隐藏选区事务。验收脚本 scripts/verify_editor_tools_browser.mjs 使用隔离 Go 页面，覆盖三引擎并拦截写请求。
- 文章组件库由 frontend/core/component-loader.ts 在显式插入/编辑时加载 JS 和 chunnel-editor-components.css；样式插到 data-component-style-anchor 前保留层叠顺序。连续编辑器保持默认，商城仍使用原有组件库入口。首页编辑样式只进入外观/布局专用 chunnel-admin-layout-workspace.css，其他后台使用不含该部分的 chunnel-admin-workspace.css；资源映射以 `../../frontend/entries.json` 与 `../../scripts/build_core_ui.mjs` 为准。
- chunnel-composer.js 现由 frontend/core/composer.ts 生成，仅商城加载；composer-core.ts / composer-parser.ts 是与连续文档编辑器共用的严格 TS 支持模块。文章编辑器独立启动且拒绝重复初始化，模板不再加载旧块编辑界面。验证入口 scripts/verify_composer_browser.mjs。
- 首页布局、模块/历史和外观预览由 frontend/core/home-editor.ts、design.ts、appearance-preview.ts 生成，保持原 URL；仅编辑 TS 和支持模块，旧 chunnel-design.source.js 已移除。布局采用统一指针拖动和校验后的配置恢复；预览独立管理请求超时/取消。验收脚本 scripts/verify_layout_browser.mjs。
- 导航编辑和角标由 frontend/core/admin-menu.ts、navigation-badges.ts 生成；icon-picker.ts 按首次打开创建本地 SVG 图标库，navigation-drag.ts 统一指针拖动与取消，保留按钮/键盘操作。两个旧 .source.js 已移除，生成资源 URL 保持原值；验收入口 scripts/verify_navigation_browser.mjs。

资源地址为 /core-assets/<内容摘要>/<路径>；稳定 /core-assets/<路径> 只用于兼容和公共客户端，按 ETag 重新验证。后台缓存版本独立于活动主题。

部分资源和全局对象保留 chunnel-* 命名，维持已存在的 DOM/脚本协议；它们是核心内部实现，不要求主题遵守。公共默认流程与 Chunnel 覆盖具有各自的视觉源码，修改共享业务呈现时检查两份实现；共同的网络和会话协议放在 NewPressClient 中。

历史浏览器资源的可读源码使用 `.source.js` / `.source.css` 后缀；同名无 `source` 的文件由 `npm run build:frontend` 生成，不应手动编辑。另有无同名 source 的手写 JS，以文件头和源码盘点判断。已迁移入口由 `frontend/entries.json` 管理：共享时间工具是 `frontend/shared/site-time*.ts`，共享请求/会话客户端是 `frontend/shared/newpress-client*.ts`，统计试用入口是 `frontend/analytics/loader.ts` 和 `main.tsx`。先运行 `npm run typecheck:frontend`，再构建和检查。后台通用、列表、评论、设置、上传、登录、导航编辑脚本现位于 `frontend/core/admin*.ts`，按 `admin-base.html` 的页面条件加载；对应旧 `.source.js` 已删除。旧文章编辑器协调仍为 `chunnel-admin-editor.source.js`，只由文章编辑页面加载。

统计页默认是 Go 原生报告，初始仅增加轻量试用加载器。点击“试用交互图表”或带 `#interactive` 进入时才加载 React/ECharts 与专属 CSS。Go 本次授权报告以安全转义属性交给空 React root，首次挂载不增加报告查询。原生报告保留为无脚本、加载失败和初始化失败的回退；React 成功后独占报告 DOM/周期导航，原生区域替换前卸载。实现见 `../../frontend/analytics/`，资源回归见 `../../tests/frontend/` 和 `../httpserver/` 的相关测试。

非登录后台按原层叠顺序加载 `chunnel-admin-shared.css`、可选页面分区和 `chunnel-admin-workflows.css`。分类与用户页共同加载 `chunnel-admin-directory.css`，会话页加载 `chunnel-admin-sessions.css`；其余后台页省略这两个分区。对应 `.source.css` 入口从 `assets/css/admin/` 导入，四个入口的完整顺序与旧 `chunnel-admin.source.css` 一致。`media.css` 和 `navigation.css` 含全局布局、响应式或共享控件，继续进入共享加载范围；不可按文件名直接删减。

相邻的输入框与 foundation（含 motion）规则合为 `chunnel-admin-controls.css`；原 `chunnel-claude`、`chunnel-claude-admin`、`chunnel-admin-ui` 依次合为 `chunnel-admin-skin.css`。登录用 `chunnel-admin-login-skin.css`，只含前两层。旧完整 `chunnel-admin.css` 和独立样式仍生成/嵌入作为兼容产物，后台模板使用上述新入口；构建统计含兼容产物，不是每页请求数。资源预算以 `../httpserver/prelaunch_resources_test.go` 和同包资源测试为准。

`assets_embed.go` 同样由构建脚本生成，仅列出运行时资源和模板。可读脚本、样式源码及 CSS 分区不进入 Go 可执行文件，也不通过 `/core-assets/` 提供。增加资源后应运行 `npm run build:frontend` 并提交产物与清单，再执行 `npm run check:frontend`。`chunnel-interactions` 和 `chunnel-experience` 的核心及主题源码由构建脚本校验一致。

资源摘要在初始化时计算；请求直接读取嵌入文件，保留条件请求、HEAD 和 Range 支持。主题磁盘资源仍使用主题包的完整性校验，不共享核心资源的信任边界。

HTTP 层完成授权、投影和页面选择，本包只负责展示。禁止从这里获取数据库、账户服务或主题设置仓库，也不要把后台 pageView 传入第三方主题。公开边界见 [主题开发规范](../../themes/README.md)。
