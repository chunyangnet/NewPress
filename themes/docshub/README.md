# DocsHub · 文档与教程

DocsHub 2.0.1 是面向产品文档、开发教程、技术知识库和课程站点的 NewPress `public/v1` 主题。保留玻璃丝带首屏、桌面侧栏、文档搜索和阅读目录，将学习、讨论、资源获取与个人资料连成完整流程。

本版覆盖当前默认主题 Chunnel 0.48.1 的全部 24 个公开页面映射。后台、业务数据、权限和审核继续由 NewPress 核心管理；所有公共页面使用 DocsHub 的配色、字体、侧栏、移动导航及深色外观。

## 功能

| 领域 | 界面与流程 |
|---|---|
| 文档与学习 | 经典首页、可编排首页、搜索与筛选、卡片/列表、分类/标签、作者、学习系列、上下篇、同系列推荐 |
| 阅读工具 | 分级目录、进度、预计时长、代码复制、收藏、点赞、分享、打印、专注阅读、当前设备最近阅读与清除 |
| 账户 | 登录、注册/邮箱验证码/找回入口、资料与头像、收藏夹、历史互动、关注、私信、通知偏好、导入本机记录 |
| 积分与成长 | 多币钱包、收支、转账核对、签到、等级、会员、勋章、认证与赞赏；按核心设置显示开放状态 |
| 技术协作 | 问答检索、提问/回答/编辑/采纳；圈子申请、加入、话题、回应、举报；反馈投票、回复、状态与路线图 |
| 内容与资源 | 技术快讯、投稿与审核状态、评论；链接目录多视图；公告目录、搜索、详情与上下篇 |
| 资源商城 | 分类、搜索、排序、购买选项、结算核对、付款、订单交付、评价与售后入口；支持核心提供的商品和购买方案 |
| 工具与服务 | 工具目录、四端截图工具、通知退订、404、维护提示、统计授权偏好 |
| 全局体验 | 深浅色/系统外观、手机抽屉与底栏、嵌套菜单、菜单徽章、快捷操作、键盘焦点、减少动态效果 |

后台开关决定问答、圈子、系列、快讯、反馈与链接的可用性。商城、邮箱登录、支付、会员、认证等功能还需要站点实际配置对应服务，未开放时显示真实状态与恢复入口。

## 安装

```powershell
newpress theme install <docshub-2.0.1.zip 的绝对路径>
```

安装后在后台主题市场选择 DocsHub，必要时重启实例以重新加载主题。

**2.0.1 使用当前 NewPress 核心的完整展示契约**：`public/v1` 的 Questions、Flash、AnnouncementPage、ShopProductKey、ShopDescriptionHTML 展示字段，主题设置上限 256 项，以及布局编辑器的 `use_home_layout` 开关。当前开发核心版本为 `0.1.0-dev`。无需安装或启用 Chunnel；共享服务脚本由核心内置的 `/core-assets/` 提供。

安装包携带当前设置定义和完整许可材料，不创建演示用户、文章或支付配置。正式发行以主题签名目录及发行元数据为准。

## 设置与首页

本版提供 **22 组、236 个设置**。完整字段、优先级和配置示例见 [SETTINGS.md](SETTINGS.md)。设置的导入、导出、预览和恢复使用核心流程；旧单例 HTML 字段不再接纳，自定义内容统一使用 home_custom_blocks。

`use_home_layout=false` 保留经典文档首页。需要更多组件时，在「外观与主题 → 可视化布局」开启「使用可编排首页」，调整顶部、主栏与侧栏。全部核心首页模块均可使用，包含轮播、导读、搜索横幅、文档流、学习系列、小组、签到、公告、标签和作者。设备可见性、模块顺序、自定义 HTML 和模块间距由核心布局配置管理。

`primary`、`utility`、`footer`、`mobile`、`mobile_action` 菜单支持自定义、嵌套和徽章；显式配置的空菜单保持为空。站点身份、版权、备案、模块开关和业务权限由核心设置维护。默认学习入口使用站内搜索，可替换为本站文档地址。

最近阅读只保存在当前浏览器，最多 12 条，首页展示最近 3 条。收藏与关注存入账户，可跨设备使用。预览不写入阅读记录或执行账户操作。隐私偏好默认关闭，开启后可在页脚重新调整访问统计授权。

## 开发与验证

在主题仓库运行：

```powershell
node scripts/build_theme.mjs --source sources/docshub
node scripts/build_theme.mjs --source sources/docshub --check
node --test tests/frontend/docshub-*.test.cjs
python scripts/check_core.py --core ../newpress --theme docshub
npm run package:docshub
```

检查包含 144 次展示夹具渲染、设置表单与预览、35 条服务路由的游客/登录状态、公共资源请求、业务流程与数据隔离。实际运行记录和覆盖边界见 [PARITY.md](PARITY.md)。

隔离浏览器预览使用 `python scripts/check_core.py --core ../newpress --theme docshub --ui`，地址为 `http://127.0.0.1:19107/`。夹具使用临时配置和内存服务；`NEWPRESS_DOCSHUB_UI_STOP` 可指定停止文件路径。源码热载入仅在设置 `NEWPRESS_DOCSHUB_UI_LIVE=1` 时开启，Go 服务变更需重启。

模板按阅读、导航、首页与服务拆分，样式位于 `assets/css/modules/`，交互源码位于仓库级 `frontend/themes/docshub/`，入口由 `frontend/entries.json` 管理。编辑源文件后运行 `npm run build:frontend` 或在仓库中运行 `npm run build:theme -- --source themes/docshub`；仅持有安装包时需取得对应仓库源码进行开发。发布包包含生成资源，运行时无需 Node 或 Python。

## 许可证与素材

主题采用 [Apache-2.0](LICENSE)。公共组件与轮播改编来源、Lucide/Feather 图标及完整许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。项目配图为 AI 生成素材，记录见 [ASSETS.md](ASSETS.md)。使用设备本地字体；保留的 Noto Sans SC 字体文件继续附带 OFL-1.1 许可。
