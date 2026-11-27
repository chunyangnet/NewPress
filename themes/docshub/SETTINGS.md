# DocsHub 2.0.1 主题设置

DocsHub 提供 22 组、236 个设置，覆盖外观、导航、首页、文档列表、阅读、系列、讨论提示和页脚。进入后台「外观与主题」(`/admin/appearance/`)，调整后点击「保存更改」。预览不会保存设置；导出、导入和恢复默认值使用 NewPress 的统一设置流程。

默认值和校验范围以 [settings.schema.json](settings.schema.json) 为准。配置使用当前声明的字段，未知字段会拒绝。配置由核心保存到数据目录下的 `theme-settings/docshub.json`；日常修改通过后台完成。

## 先理解设置的作用范围

- **菜单优先级**：在菜单管理中配置的 `primary`、`utility`、`footer`、`mobile`、`mobile_action` 菜单，分别替代主题默认主导航、更多资源、页脚、手机底栏与底栏快捷入口。显式配置的空菜单保持为空。`show_series_navigation`、默认 RSS 和主题名称开关只影响默认菜单；`show_utility_navigation`、`show_footer_navigation` 可以隐藏整个对应区域。修改 `listing_title`、`series_title` 会同步默认主导航名称，自定义菜单的标签仍由菜单管理维护。
- **站点身份与页脚**：站点名、站点图标、语言、账号身份、版权、备案和自定义页脚内容归核心管理。`footer_description` 只在核心未设置自定义版权时显示；主题的页脚导航开关不隐藏版权、备案或核心页脚内容。核心隐藏署名设置优先于 `show_theme_credit`，RSS 还要求公开 API 开启。
- **外观优先级**：允许切换时，读者浏览器中保存的浅色/深色/跟随系统偏好优先于 `default_color_mode`。关闭 `allow_color_switch` 后统一使用默认外观并忽略旧偏好。预览页从当前预览配置开始，不读取或写入读者偏好；预览中的临时切换不影响公开页面。
- **首页数据数量**：`tutorials_count`、`latest_count` 各允许 1–8 篇，默认 4 篇，只截取核心已经提供的数据。需要显示 8 篇时，还要在站点阅读设置中提供至少 8 篇首页文章，并确保有足够公开内容。教程优先使用核心的精选集合（有数据时），否则使用首页文章；主题不会为了补足数量生成示例内容或额外查询。
- **入口复用**：快捷入口、首屏按钮和学习步骤复用「导航与学习入口」中的地址。`start_link` 对应主按钮/入门步骤，`advanced_link` 对应进阶，`practice_link` 对应实战，`community_link` 也用于阅读帮助。关闭教程推荐时，默认 `demo_link="/#tutorials"` 自动转向 `/explore/`；自定义链接由站长维护。更多链接需要文字和地址同时非空；公告文字为空时整条公告隐藏。
- **列表视图**：`list_default_view` 用于文档中心、作者页和归档页。启用 JavaScript 时，地址里的有效 `view=grid` 或 `view=list` 优先，非法值回退到默认设置。隐藏排序、视图或分类控件只隐藏入口；已有 URL 条件仍有效，已选关键词/分类仍可清除。默认排序由核心决定。
- **阅读与系列**：目录只收录正文存在的标题；`h2`、`h3`、`h4` 分别表示截至二、三、四级。关闭全部阅读侧栏内容时自动收为单栏。系列入口、上下篇和推荐依赖核心系列模块及真实系列关系，同系列推荐最多三篇。隐藏入口不会停用模块。
- **讨论与恢复**：主题只调整评论区标题、说明和输入提示；评论开关、登录要求、审核与验证由核心控制。404 文案和搜索可以配置；维护说明始终使用核心维护信息，维护页不显示搜索入口。

## 尺寸、空值与无脚本行为

`content_width` 为主内容区域上限（960–1800px，不含左侧导航），`sidebar_width` 为宽屏侧栏宽度（190–280px，只在 1180px 以上生效）。`reading_width` 为文章区域上限（960–1440px，包含右侧目录）；它仍受外层内容宽度约束。`reading_font_size` 范围 14–20px，手机正文自动减小 1px；`reading_line_height` 范围 1.5–2.4。小屏幕始终随窗口收缩。

可选说明留空后不生成空白段落。首屏第一行标题留空时显示站点名；教程区、社区标题和搜索提示留空时使用默认文字。带必填校验的标题、按钮或阅读帮助文字不能保存为空。颜色接受六位十六进制值，入口接受安全的站内路径或网址；范围、枚举和类型由核心校验。

禁用 JavaScript 时，站内链接、搜索表单、正文和后台配置的默认列表视图仍可使用。默认深色由服务端直接输出；跟随系统回退浅色。外观切换、动态目录、阅读进度/时长、复制按钮功能、相对日期和悬浮返回顶部需要 JavaScript。首页日期会保留完整日期。减少动态效果同时尊重主题开关和系统偏好。

## 三种配置示例

以下只列需要调整的字段，可在后台对应分组中设置；它们是配置对照，不是完整的导入文档。未列字段保留当前值，建议从默认配置开始。

**产品文档门户**：保留首页入口与分类检索，推荐内容更丰富，外观跟随读者设备。

```json
{
  "default_color_mode": "system",
  "allow_color_switch": true,
  "hero_eyebrow": "产品文档",
  "hero_title": "从这里开始",
  "hero_title_second": "找到每一步的答案",
  "tutorials_count": 6,
  "latest_count": 6,
  "card_cover": "all",
  "list_default_view": "grid",
  "show_category_filters": true,
  "toc_depth": "h3"
}
```

首页文章数据至少提供 6 篇；将快速开始、API 和常见问题地址替换为本站对应内容。

**精简知识库**：减少插画和首页模块，用紧凑列表呈现更新，保留顶部搜索。

```json
{
  "navigation_density": "compact",
  "show_hero_artwork": false,
  "show_hero_secondary": false,
  "show_quick_links": false,
  "show_tutorials": false,
  "show_community": false,
  "show_learning_path": false,
  "show_latest": true,
  "latest_count": 8,
  "list_default_view": "list",
  "card_cover": "none",
  "card_meta": "date",
  "show_card_excerpt": true,
  "reduce_motion": true
}
```

列表无封面时自动收回图片列。需要统一浅色展示时另设 `default_color_mode="light"`、`allow_color_switch=false`。

**课程阅读站**：从系列索引进入课程，使用更详细的目录与连续阅读工具。

```json
{
  "series_title": "课程目录",
  "start_link": "/series/",
  "hero_primary_label": "选择课程",
  "show_series_navigation": true,
  "show_learning_path": true,
  "show_learning_series_link": true,
  "toc_depth": "h4",
  "reading_font_size": 17,
  "reading_line_height": 2,
  "reading_width": 1160,
  "show_reading_series": true,
  "show_series_related": true,
  "show_chapter_excerpt": true,
  "show_chapter_meta": false
}
```

先在核心启用系列模块，创建公开系列并关联章节。需要把课程放到自定义主菜单时，在菜单管理中添加 `/series/` 入口。

## 2.0.1 服务与高级首页

`show_service_navigation` 控制侧栏的学习与协作入口；问答、圈子、快讯、反馈、链接和系列还受核心模块开关约束。`show_shop_navigation` 单独控制资源商城入口。宣传截图由核心后台提供，仅管理员可用。页面权限、支付、评论、内容审核、邮箱验证均由核心决定；隐藏入口不会关闭服务。

`use_home_layout` 默认关闭，保留原有文档首页。在「外观与主题 → 可视化布局」开启后，使用核心 `home-layout/v1`，调整顶部、主栏、侧栏的模块顺序、设备可见性和间距。模块包括导读、分类、轮播、搜索横幅、文档流、系列、小组、签到、公告、作者、标签与统计。配置为「隐藏」的模块不输出；缺少实际内容或核心服务未开放时，相应模块不会出现。

轮播支持四张图片、标题、说明和跳转地址；没有图片的条目不输出。支持按钮、方向键、触摸切换与暂停；系统减少动画或省流量模式会停止自动播放。视频背景须配置可访问的视频地址，播放失败时保留静态背景与重试入口。图片、视频请优先使用站内媒体地址。

`home_custom_blocks` 通过核心布局编辑器管理多个自定义模块。HTML 经核心安全清理后进入展示层；主题不执行任意模板代码。

首页签到读取核心奖励配置，领取和重复请求处理沿用真实账户服务；未登录时引导登录。文章收藏、点赞、关注和私信也使用核心接口。最近阅读记录仅保存在当前浏览器，不跨设备同步；最多保留 12 条、首页展示最近 3 条，可清除。专注阅读、分享与打印是本地工具。

`cookie_consent_enabled` 默认关闭。开启后可选择是否允许访问统计，在页脚重新打开偏好面板。只控制支持授权标记的站点统计脚本；不控制登录等必要功能，也不能替代站点自己的隐私政策。

## 分组字段索引

以下索引根据随包 schema 生成；默认值、允许范围与选项以 schema 为准。

### 品牌与外观

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `accent` | 强调色 | #4c82ff | 最长 160 字符 |
| `default_color_mode` | 默认外观 | light | light, dark, system |
| `allow_color_switch` | 允许访客切换外观 | true | 空 |
| `content_width` | 内容区域最大宽度 | 1600 | 960–1800 |
| `sidebar_width` | 桌面侧栏宽度 | 218 | 190–280 |
| `navigation_density` | 桌面导航密度 | comfortable | comfortable, compact |
| `reduce_motion` | 减少动态效果 | false | 空 |

### 导航与搜索

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `sticky_header` | 固定顶部工具栏 | true | 空 |
| `show_search` | 显示顶部搜索 | true | 空 |
| `search_placeholder` | 搜索框提示语 | 搜索文档、教程或关键词… | 最长 80 字符 |
| `show_search_shortcut` | 显示搜索快捷键提示 | true | 空 |
| `show_help` | 显示桌面帮助菜单 | true | 空 |
| `show_language` | 显示桌面语言信息 | true | 空 |
| `show_account` | 显示账号入口 | true | 空 |
| `show_utility_navigation` | 显示更多资源导航 | true | 空 |
| `show_series_navigation` | 默认导航显示学习系列 | true | 空 |
| `show_mobile_navigation` | 移动端底部导航 | true | 空 |

### 站点公告

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `announcement_text` | 公告内容 | 空 | 最长 240 字符 |
| `announcement_link` | 公告详情链接 | 空 | 最长 2048 字符 |
| `announcement_link_label` | 公告链接文字 | 查看详情 | 最长 40 字符 |

### 首页首屏

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `hero_eyebrow` | 首屏眉题 | 空 | 最长 60 字符 |
| `hero_title` | 首屏标题 · 第一行 | 开发文档 | 最长 160 字符 |
| `hero_title_second` | 首屏标题 · 第二行 | 与使用教程 | 最长 160 字符 |
| `hero_description` | 首屏介绍 · 第一行 | 查阅安装步骤、功能说明和接口用法， | 最长 160 字符 |
| `hero_description_second` | 首屏介绍 · 第二行 | 也可以按章节阅读教程和示例。 | 最长 160 字符 |
| `show_hero_artwork` | 显示玻璃丝带插画 | true | 空 |
| `hero_primary_label` | 首屏主按钮文字 | 开始阅读 | 最长 24 字符 |
| `show_hero_secondary` | 显示首屏次按钮 | true | 空 |
| `hero_secondary_label` | 首屏次按钮文字 | 浏览教程 | 最长 24 字符 |

### 首页模块

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `show_quick_links` | 显示快捷入口 | true | 空 |
| `show_tutorials` | 显示教程推荐 | true | 空 |
| `show_latest` | 显示最新文档 | true | 空 |
| `show_community` | 显示社区卡片 | true | 空 |
| `show_learning_path` | 显示学习路径 | true | 空 |

### 首页快捷入口

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `quick_start_title` | 快捷入口 1 · 标题 | 快速上手 | 最长 24 字符 |
| `quick_start_description` | 快捷入口 1 · 说明 | 环境准备与基础操作 | 最长 80 字符 |
| `quick_advanced_title` | 快捷入口 2 · 标题 | 进阶教程 | 最长 24 字符 |
| `quick_advanced_description` | 快捷入口 2 · 说明 | 功能用法与实现示例 | 最长 80 字符 |
| `quick_api_title` | 快捷入口 3 · 标题 | API 参考 | 最长 24 字符 |
| `quick_api_description` | 快捷入口 3 · 说明 | 接口参数与调用示例 | 最长 80 字符 |
| `quick_tools_title` | 快捷入口 4 · 标题 | 开发工具 | 最长 24 字符 |
| `quick_tools_description` | 快捷入口 4 · 说明 | 工具安装与配置说明 | 最长 80 字符 |

### 首页文档内容

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `tutorials_title` | 教程区标题 | 教程推荐 | 最长 160 字符 |
| `tutorials_description` | 教程区说明 | 空 | 最长 200 字符 |
| `tutorials_count` | 推荐教程数量 | 4 | 1–8 |
| `tutorials_more_label` | 教程区更多链接文字 | 查看全部 | 最长 24 字符 |
| `tutorials_more_link` | 教程区更多链接 | /explore/ | 最长 2048 字符 |
| `latest_title` | 最新文档标题 | 最新文档 | 最长 80 字符 |
| `latest_description` | 最新文档说明 | 空 | 最长 200 字符 |
| `latest_count` | 最新文档数量 | 4 | 1–8 |
| `latest_more_label` | 最新文档更多链接文字 | 查看全部 | 最长 24 字符 |
| `latest_more_link` | 最新文档更多链接 | /explore/?sort=published | 最长 2048 字符 |
| `show_latest_excerpt` | 显示最新文档摘要 | true | 空 |
| `show_latest_date` | 显示最新文档日期 | true | 空 |
| `relative_dates` | 最新文档使用相对时间 | true | 空 |

### 社区与学习路径

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `community_title` | 社区卡片标题 | 社区交流 | 最长 160 字符 |
| `community_description` | 社区卡片说明 | 提交问题，交流使用经验。 | 最长 200 字符 |
| `community_button_label` | 社区按钮文字 | 前往社区 | 最长 24 字符 |
| `show_community_artwork` | 显示社区山景插画 | true | 空 |
| `learning_path_title` | 学习路径标题 | 学习路径 | 最长 48 字符 |
| `show_learning_series_link` | 显示“全部系列”入口 | true | 空 |
| `learning_start_title` | 学习步骤 1 · 标题 | 快速入门 | 最长 32 字符 |
| `learning_start_description` | 学习步骤 1 · 说明 | 了解基本概念与环境搭建 | 最长 120 字符 |
| `learning_advanced_title` | 学习步骤 2 · 标题 | 进阶提升 | 最长 32 字符 |
| `learning_advanced_description` | 学习步骤 2 · 说明 | 了解功能用法与配置方法 | 最长 120 字符 |
| `learning_practice_title` | 学习步骤 3 · 标题 | 实战应用 | 最长 32 字符 |
| `learning_practice_description` | 学习步骤 3 · 说明 | 参考项目示例与操作步骤 | 最长 120 字符 |

### 导航与学习入口

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `start_link` | 快速开始链接 | /explore/?q=入门 | 最长 2048 字符 |
| `advanced_link` | 进阶教程链接 | /explore/?q=进阶 | 最长 2048 字符 |
| `api_link` | API 参考链接 | /explore/?q=API | 最长 2048 字符 |
| `practice_link` | 最佳实践链接 | /explore/?q=实战 | 最长 2048 字符 |
| `tools_link` | 开发工具链接 | /explore/ | 最长 2048 字符 |
| `faq_link` | 常见问题链接 | /explore/?q=常见问题 | 最长 2048 字符 |
| `changelog_link` | 更新日志链接 | /explore/?q=更新 | 最长 2048 字符 |
| `community_link` | 社区入口链接 | /community/ | 最长 2048 字符 |
| `projects_link` | 开源项目链接 | /explore/?q=开源 | 最长 2048 字符 |
| `about_link` | 关于我们链接 | /explore/?q=关于 | 最长 2048 字符 |
| `demo_link` | 演示教程链接 | /#tutorials | 最长 2048 字符 |

### 文档列表与卡片

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `listing_title` | 文档中心标题 | 文档与教程 | 最长 160 字符 |
| `listing_description` | 文档中心介绍 | 按分类浏览文档，或搜索你需要的操作步骤与示例。 | 最长 300 字符 |
| `list_default_view` | 默认展示方式 | grid | grid, list |
| `show_category_filters` | 显示分类筛选 | true | 空 |
| `show_sort_control` | 显示排序控件 | true | 空 |
| `show_view_switch` | 显示视图切换 | true | 空 |
| `card_cover` | 文档卡片封面 | all | all, real, none |
| `show_card_category` | 显示卡片分类 | true | 空 |
| `show_card_excerpt` | 显示卡片摘要 | true | 空 |
| `card_meta` | 卡片底部信息 | full | full, date, none |

### 文章阅读

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `show_toc` | 显示文章目录 | true | 空 |
| `toc_depth` | 目录层级 | h3 | h2, h3, h4 |
| `show_reading_progress` | 显示阅读进度 | true | 空 |
| `show_reading_time` | 显示预计阅读时间 | true | 空 |
| `show_code_copy` | 显示代码复制按钮 | true | 空 |
| `show_copy_link` | 显示复制文章链接 | true | 空 |
| `show_article_author` | 显示文章作者 | true | 空 |
| `show_article_date` | 显示最后更新日期 | true | 空 |
| `show_article_excerpt` | 显示文章摘要 | true | 空 |
| `show_article_tags` | 显示文章标签 | true | 空 |
| `show_reading_help` | 显示阅读帮助入口 | true | 空 |
| `reading_help_label` | 阅读帮助文案 | 遇到问题？到社区聊聊 | 最长 80 字符 |
| `show_reading_back_to_top` | 显示阅读侧栏返回顶部 | true | 空 |
| `reading_font_size` | 正文字号 | 15 | 14–20 |
| `reading_line_height` | 正文行高 | 1.95 | 1.5–2.4 |
| `reading_width` | 阅读区域最大宽度 | 1240 | 960–1440 |
| `show_reading_history` | 本机继续阅读 | true | 空 |
| `show_reading_actions` | 文档收藏、点赞和关注 | true | 空 |
| `show_reading_feedback` | 文档纠错与提问入口 | true | 空 |
| `show_reading_focus` | 专注阅读开关 | true | 空 |
| `show_reading_print` | 打印文档 | true | 空 |

### 学习系列

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `series_title` | 系列索引标题 | 学习系列 | 最长 160 字符 |
| `series_description` | 系列默认介绍 | 按主题整理的文档，可按章节顺序阅读。 | 最长 300 字符 |
| `show_series_search` | 显示系列搜索 | true | 空 |
| `show_chapter_excerpt` | 显示章节摘要 | true | 空 |
| `show_chapter_meta` | 显示章节分类与日期 | true | 空 |
| `show_reading_series` | 显示系列上下篇与阅读位置 | true | 空 |
| `show_series_related` | 显示同系列推荐文档 | false | 空 |

### 学习与社区服务

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `show_shop_navigation` | 显示学习资源商城入口 | true | 空 |
| `show_service_navigation` | 显示服务导航 | true | 空 |
| `show_home_services` | 首页学习与服务入口 | true | 空 |

### 讨论区

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `comments_title` | 讨论区标题 | 交流与讨论 | 最长 80 字符 |
| `comments_intro` | 讨论区说明 | 空 | 最长 400 字符 |
| `comments_placeholder` | 评论输入提示 | 空 | 最长 160 字符 |

### 错误与恢复

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `not_found_title` | 未找到页面的标题 | 文档不存在 | 最长 80 字符 |
| `not_found_description` | 未找到页面的说明 | 这篇文档可能已被移动。搜索关键词，或从文档中心继续浏览。 | 最长 300 字符 |
| `show_error_search` | 404 页面显示搜索 | true | 空 |

### 页脚与辅助入口

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `footer_description` | 页脚站点说明 | 文档与教程 | 最长 160 字符 |
| `show_footer_navigation` | 显示页脚导航 | true | 空 |
| `show_footer_rss` | 默认页脚显示 RSS 订阅 | true | 空 |
| `show_theme_credit` | 默认页脚显示主题名称 | true | 空 |
| `show_back_to_top` | 显示悬浮返回顶部 | true | 空 |

### 隐私偏好

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `cookie_privacy_url` | 隐私政策链接 | 空 | 最长 2048 字符 |
| `cookie_consent_enabled` | 启用可选 Cookie 授权 | false | 空 |

### 可编排首页与显示设备

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `use_home_layout` | 使用可编排首页 | false | 空 |
| `home_hero_visibility` | 封面精选显示设备 | both | 空 |
| `home_notice_visibility` | 社区公告显示设备 | both | 空 |
| `home_channels_visibility` | 快捷频道显示设备 | hidden | 空 |
| `home_categories_visibility` | 分类导航显示设备 | hidden | 空 |
| `home_feed_visibility` | 文章信息流显示设备 | both | 空 |
| `home_series_visibility` | 专题与系列显示设备 | both | 空 |
| `home_spotlight_visibility` | 推荐阅读显示设备 | hidden | 空 |
| `home_community_visibility` | 发现圈子显示设备 | both | 空 |
| `home_welcome_visibility` | 欢迎名片显示设备 | both | 空 |
| `home_recent_visibility` | 最近发布显示设备 | hidden | 空 |
| `home_authors_visibility` | 遇见创作者显示设备 | hidden | 空 |
| `home_tags_visibility` | 探索标签显示设备 | both | 空 |
| `home_stats_visibility` | 站点一览显示设备 | hidden | 空 |
| `home_subscribe_visibility` | 订阅更新显示设备 | both | 空 |
| `home_reading_guide_visibility` | 入门导读显示设备 | both | 空 |
| `home_updated_visibility` | 最近更新显示设备 | both | 空 |
| `home_focus_list_visibility` | 图文焦点与列表显示设备 | hidden | 空 |
| `home_topic_banner_visibility` | 专题横幅显示设备 | hidden | 空 |
| `home_entry_cards_visibility` | 卡片入口显示设备 | hidden | 空 |
| `home_image_grid_visibility` | 网格图文推荐显示设备 | hidden | 空 |
| `home_notice_list_visibility` | 公告列表显示设备 | hidden | 空 |
| `home_top_order` | 首屏与频道 | hero,reading_guide,notice,channels,categories | 最长 1024 字符 |
| `home_feed_title` | 文章流标题 | 最近更新的文档 | 最长 32 字符 |
| `home_main_order` | 内容区域 | feed,series,spotlight,community | 最长 1024 字符 |
| `home_feed_sort` | 首页默认排序 | updated | published, updated, oldest |
| `home_aside_order` | 侧栏小工具 | welcome,updated,checkin,notice_list,tags,subscribe,recent,authors,stats | 最长 1024 字符 |
| `feed_columns` | 内容列数 | grid | list, grid |
| `sidebar_enabled` | 首页侧栏 | true | 空 |
| `home_sidebar_width` | 首页侧栏宽度 | 292 | 260–360 |
| `home_checkin_visibility` | 学习签到显示设备 | both | both, desktop, mobile, hidden |
| `home_featured_ids` | 推荐文章 | 空 | 最长 240 字符 |
| `home_category_count` | 分类导航数量 | 4 | 1–8 |
| `home_series_count` | 专题展示数量 | 4 | 1–8 |
| `home_module_gap` | 首页模块间距 | 20 | 12–32 |
| `home_carousel_visibility` | 图片轮播显示设备 | hidden | 空 |
| `home_search_banner_visibility` | 背景搜索栏显示设备 | hidden | 空 |
| `home_updated_count` | 最近更新文章数量 | 4 | 3–5 |

### 首页轮播

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `home_carousel_animation` | 切换动画 | slide | slide, fade, rotate-3d, stack, accordion, page-turn |
| `home_carousel_autoplay` | 自动播放 | true | 空 |
| `home_carousel_interval` | 播放间隔 | 3 | 2–15 |
| `home_carousel_autohide` | 控件自动隐藏 | true | 空 |
| `home_slide_1_title` | 第 1 张 · 标题 | 空 | 最长 80 字符 |
| `home_slide_1_summary` | 第 1 张 · 简介 | 空 | 最长 160 字符 |
| `home_slide_1_image` | 第 1 张 · 图片 | 空 | 最长 2048 字符 |
| `home_slide_1_url` | 第 1 张 · 链接 | 空 | 最长 2048 字符 |
| `home_slide_2_title` | 第 2 张 · 标题 | 空 | 最长 80 字符 |
| `home_slide_2_summary` | 第 2 张 · 简介 | 空 | 最长 160 字符 |
| `home_slide_2_image` | 第 2 张 · 图片 | 空 | 最长 2048 字符 |
| `home_slide_2_url` | 第 2 张 · 链接 | 空 | 最长 2048 字符 |
| `home_slide_3_title` | 第 3 张 · 标题 | 空 | 最长 80 字符 |
| `home_slide_3_summary` | 第 3 张 · 简介 | 空 | 最长 160 字符 |
| `home_slide_3_image` | 第 3 张 · 图片 | 空 | 最长 2048 字符 |
| `home_slide_3_url` | 第 3 张 · 链接 | 空 | 最长 2048 字符 |
| `home_slide_4_title` | 第 4 张 · 标题 | 空 | 最长 80 字符 |
| `home_slide_4_summary` | 第 4 张 · 简介 | 空 | 最长 160 字符 |
| `home_slide_4_image` | 第 4 张 · 图片 | 空 | 最长 2048 字符 |
| `home_slide_4_url` | 第 4 张 · 链接 | 空 | 最长 2048 字符 |

### 首页背景搜索

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `home_search_title` | 搜索标题 | 查找你的下一个答案 | 最长 80 字符 |
| `home_search_summary` | 搜索说明 | 搜索文档、教程和实践记录。 | 最长 160 字符 |
| `home_search_placeholder` | 输入框提示 | 输入功能、接口或错误关键词 | 最长 48 字符 |
| `home_search_terms` | 热门关键词 | 入门,API,配置 | 最长 240 字符 |
| `home_search_background` | 背景样式 | gradient | gradient, image, video |
| `home_search_poster` | 静态封面 | 空 | 最长 2048 字符 |
| `home_search_image` | 图片 / GIF 地址 | 空 | 最长 2048 字符 |
| `home_search_video` | 视频地址（MP4 / WebM） | 空 | 最长 2048 字符 |
| `home_search_motion` | 允许背景动画 | true | 空 |

### 首页导读与小工具

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `home_focus_title` | 图文焦点标题 | 推荐学习 | 最长 120 字符 |
| `home_image_grid_title` | 网格推荐标题 | 动手实践 | 最长 120 字符 |
| `home_topic_title` | 专题横幅标题 | 循序渐进，掌握新技能 | 最长 80 字符 |
| `home_topic_summary` | 专题横幅说明 | 从基础概念到实践示例，按章节建立完整知识。 | 最长 160 字符 |
| `home_topic_image` | 专题横幅图片 | 空 | 最长 2048 字符 |
| `home_topic_url` | 专题横幅链接 | /series/ | 最长 2048 字符 |
| `home_entry_1_title` | 入口 1 · 名称 | 文档中心 | 最长 20 字符 |
| `home_entry_1_url` | 入口 1 · 链接 | /explore/ | 最长 2048 字符 |
| `home_entry_1_icon` | 入口 1 · 图标 | compass | compass, library, bookmark, rss, newspaper, users, message-circle, layout-grid, pencil, chart-no-axes-column |
| `home_entry_2_title` | 入口 2 · 名称 | 学习系列 | 最长 20 字符 |
| `home_entry_2_url` | 入口 2 · 链接 | /series/ | 最长 2048 字符 |
| `home_entry_2_icon` | 入口 2 · 图标 | library | compass, library, bookmark, rss, newspaper, users, message-circle, layout-grid, pencil, chart-no-axes-column |
| `home_entry_3_title` | 入口 3 · 名称 | 我的收藏 | 最长 20 字符 |
| `home_entry_3_url` | 入口 3 · 链接 | /account/ | 最长 2048 字符 |
| `home_entry_3_icon` | 入口 3 · 图标 | bookmark | compass, library, bookmark, rss, newspaper, users, message-circle, layout-grid, pencil, chart-no-axes-column |
| `home_entry_4_title` | 入口 4 · 名称 | 订阅更新 | 最长 20 字符 |
| `home_entry_4_url` | 入口 4 · 链接 | /feed.xml | 最长 2048 字符 |
| `home_entry_4_icon` | 入口 4 · 图标 | rss | compass, library, bookmark, rss, newspaper, users, message-circle, layout-grid, pencil, chart-no-axes-column |
| `home_notice_list_title` | 公告列表标题 | 文档公告 | 最长 80 字符 |
| `home_notice_list_count` | 首页公告显示条数 | 4 | 1–8 |
| `home_notice_title` | 公告标题 | 文档与社区 | 最长 24 字符 |
| `home_notice_text` | 公告内容 | 遇到问题时，带上复现步骤和配置示例，一起找到答案。 | 最长 160 字符 |
| `home_notice_url` | 公告链接 | /questions/ | 最长 2048 字符 |
| `home_notice_link_label` | 公告链接文字 | 前往问答 | 最长 16 字符 |
| `home_guide_title` | 导读标题 | 从这里开始 | 最长 60 字符 |
| `home_guide_description` | 导读简介 | 按你的目标选择阅读入口，逐步完成实践。 | 最长 160 字符 |
| `home_guide_1_title` | 导读入口 1标题 | 快速开始 | 最长 120 字符 |
| `home_guide_1_description` | 导读入口 1说明 | 找到安装与配置步骤 | 最长 120 字符 |
| `home_guide_1_url` | 导读入口 1链接 | /explore/?q=入门 | 最长 500 字符 |
| `home_guide_2_title` | 导读入口 2标题 | 系统学习 | 最长 120 字符 |
| `home_guide_2_description` | 导读入口 2说明 | 按系列逐章学习 | 最长 120 字符 |
| `home_guide_2_url` | 导读入口 2链接 | /series/ | 最长 500 字符 |
| `home_guide_3_title` | 导读入口 3标题 | 解决问题 | 最长 120 字符 |
| `home_guide_3_description` | 导读入口 3说明 | 查找解答与提问 | 最长 120 字符 |
| `home_guide_3_url` | 导读入口 3链接 | /questions/ | 最长 500 字符 |

### 首页自定义内容

| 字段 | 用途 | 默认值 | 允许值 / 范围 |
|---|---|---|---|
| `home_custom_blocks` | 自定义 HTML 组件 | 空 | 最长 65536 字符 |
