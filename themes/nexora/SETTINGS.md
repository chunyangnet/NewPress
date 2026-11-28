# Nexora 主题设置

使用 NewPress 原生设置面板、预览、保存、导入、导出与重置功能。字段定义以 [settings.schema.json](settings.schema.json) 为准；没有独立的主题设置存储或后台脚本。企业内容配置流程见 [企业上线指南](ENTERPRISE.md)。

## 使用

- 建议先预览，再保存。预览使用隔离候选值，不改变当前设置或持久化文档；保存使用版本校验，避免旧页面覆盖新设置。
- 首屏、关于我们、服务优势、技术研发、核心业务、新闻、人才招聘和联系合作默认启用；企业事实、合作伙伴、常见问题默认关闭。
- 空内容不会生成占位卡片：企业事实需同时填写数值与口径，伙伴需填写名称，FAQ 需同时填写问答；即使开启模块，全空时仍不渲染。
- 主题内容设置均作为纯文本转义，不支持在字段中粘贴 HTML。图片、地图、社交、伙伴和 CTA 链接接受以 `/` 开头的站内绝对路径或完整 `http(s)` 地址。首页锚点使用 `/#about` 等完整站内路径，不接受 `#about`、脚本 URI、协议相对地址、空白或反斜杠。
- 默认按钮指向的首页模块关闭时，链接会隐藏；联系区关闭时使用有效的直接联系入口，再依次回退到邮箱、电话或已启用的在线留言。没有可用入口时隐藏按钮。
- 内容、菜单、站点名称、SEO、社交分享图、评论权限、隐私政策、审核、验证码和登记信息继续使用核心数据。主题设置不复制这些管理入口。

浏览器交互实现已迁至仓库 `frontend/themes/nexora/`。下表旧模块位置对应同名 `.ts` 文件：`reading.ts`、`carousel.ts`、`navigation-state.ts`；安装包仅保留打包后的 `assets/js/nexora.js`。

## 设置之间的关系

| 设置 | 生效范围与限制 |
| --- | --- |
| 全站配色 | 页面、卡片、表单、目录、选中态和焦点使用同一组变量；图片首屏与研发图片保留原有图片色彩，错误提示保留可识别的错误色。自定义颜色后应检查文字对比。 |
| 标题字体、按钮、密度 | 标题可选衬线或无衬线；按钮可选胶囊、微圆角或直角；紧凑密度减少留白，移动端保持操作区域。 |
| 界面动效 | 关闭后取消过渡、首屏进场、图片缩放与平滑滚动；自动轮播也会暂停。系统减少动态效果偏好优先。 |
| 页眉 | 透明仅作用于已开启首屏的首页；实体页眉使用设置颜色。固定关闭后随页滚动，移动菜单打开时仍固定在视口内。 |
| 自定义标志 | 最大宽度 48–180 px，默认 80 px；高度限制在页眉空间内，保持原图比例。手机端额外限制标志宽度，过长名称省略显示，保留完整可访问名称和菜单空间。空图片使用内置品牌标志。 |
| 页脚 | 品牌说明留空使用站点标语；版权留空使用年份和站点名。关闭品牌介绍仍保留页脚导航、版权、登记信息和已启用的联系信息；返回顶部可独立隐藏。 |
| 轮播 | 单屏模式只显示第一张；自动轮播默认关闭。启用后间隔为 5–15 秒，悬停、键盘焦点、页面隐藏与减少动效均暂停。第二、三张空图片沿用关于我们、研发图片，再回退到内置图。 |
| 首屏图片 | 移动图片仅替换第一张；额外暗色遮罩范围 0–0.7。替代文字默认空，纯装饰图不重复朗读；提供信息的图片可填写描述。 |
| 首页顺序 | 企业介绍优先、业务优先、新闻优先三种编排；已关闭或内容不完整的模块会跳过。 |
| 联系信息 | 默认不展示；可放首页联系区、页脚或两处。联系区还需开启首页联系模块。地图链接需同时填写联系地址，地址作为链接；社交入口需名称与链接同时存在。 |
| 新闻卡片 | 图片、摘要、日期、分类开关共享到首页与归档卡片；摘要行数为 1–4 行。列数仅作用于网格布局，窄屏自动折叠。 |
| 文章阅读 | 宽屏正文最大宽度 760、1000 或 1200 px，移动端自适应。阅读时间与更新时间需开启文章元信息，仅对文章生效。目录根据正文标题自动生成，折叠设置不改变正文。 |
| 评论与专题 | 评论标题和 300 字以内的介绍仅影响展示，不改变核心评论策略。专题标题和说明作用于专题列表，专题内容与顺序仍由核心管理。 |

## 字段与实际消费位置

设置投影使用核心 `SettingsCSS()` 和 `SettingsBodyClasses()`；[base.html](templates/base.html) 加载设置样式并输出 body 类。下表列出全部字段的模板、样式或脚本消费者。CSS 源文件由 [nexora.source.css](assets/css/nexora.source.css) 汇总打包。

| 分组 | 字段 | 消费位置 |
| --- | --- | --- |
| 全站配色 | `accent`、`ink`、`paper`、`muted`、`surface`、`border` | [settings.css](assets/css/settings.css)、[foundation.css](assets/css/foundation.css)、[states.css](assets/css/states.css) |
| 排版与交互 | `heading_font`、`button_shape`、`content_density`、`motion_enabled` | [settings.css](assets/css/settings.css)、[reading.js](assets/js/modules/reading.js)、[carousel.js](assets/js/modules/carousel.js)、[home-hero.html](templates/partials/home-hero.html) |
| 页眉与导航 | `logo_image`、`logo_width`、`header_background`、`header_color`、`header_sticky`、`header_transparent`、`show_header_search`、`header_contact_label`、`contact_link` | [header.html](templates/partials/header.html)、[home-actions.html](templates/partials/home-actions.html)、[settings.css](assets/css/settings.css)、[navigation-state.js](assets/js/modules/navigation-state.js) |
| 页脚 | `footer_background`、`footer_color`、`show_footer_top`、`footer_description`、`footer_copyright`、`show_back_to_top` | [footer.html](templates/partials/footer.html)、[settings.css](assets/css/settings.css) |
| 首页模块与顺序 | `show_hero`、`home_sequence`、`show_about`、`show_strengths`、`show_innovation`、`show_facts`、`show_business`、`show_news`、`show_partners`、`show_careers`、`show_faq`、`show_contact` | [home.html](templates/home.html)、[home-actions.html](templates/partials/home-actions.html)、[header.html](templates/partials/header.html)、[footer.html](templates/partials/footer.html)、[states.css](assets/css/states.css) |
| 首屏与轮播 | `hero_mode`、`hero_autoplay`、`hero_interval`、`hero_height`、`hero_alignment`、`hero_shade`、`hero_title`、`hero_title_second`、`hero_subtitle`、`hero_button`、`hero_link`、`hero_image`、`hero_mobile_image`、`hero_image_alt`、`second_title`、`second_title_second`、`second_subtitle`、`second_button`、`second_link`、`second_image`、`second_image_alt`、`third_title`、`third_title_second`、`third_subtitle`、`third_button`、`third_link`、`third_image`、`third_image_alt` | [home-hero.html](templates/partials/home-hero.html)、[carousel.js](assets/js/modules/carousel.js)、[chrome-options.css](assets/css/chrome-options.css) |
| 关于我们 | `about_title`、`about_description`、`about_image`、`about_button`、`about_link` | [home-about.html](templates/partials/home-about.html)、[home-hero.html](templates/partials/home-hero.html)、[home-actions.html](templates/partials/home-actions.html) |
| 企业事实 | `facts_title`、`facts_description`、`fact_1_value`、`fact_1_label`、`fact_2_value`、`fact_2_label`、`fact_3_value`、`fact_3_label`、`fact_4_value`、`fact_4_label` | [home-facts.html](templates/partials/home-facts.html)、[home-actions.html](templates/partials/home-actions.html)、[home-rich.css](assets/css/home-rich.css) |
| 服务优势 | `strengths_title`、`strengths_subtitle`、`strength_1_title`、`strength_1_text`、`strength_2_title`、`strength_2_text`、`strength_3_title`、`strength_3_text`、`strength_4_title`、`strength_4_text` | [home-strengths.html](templates/partials/home-strengths.html) |
| 技术研发 | `innovation_title`、`innovation_description`、`innovation_image`、`innovation_button`、`innovation_link` | [home-innovation.html](templates/partials/home-innovation.html)、[home-hero.html](templates/partials/home-hero.html)、[home-actions.html](templates/partials/home-actions.html) |
| 核心业务 | `business_title`、`business_description`、`business_1_title`、`business_1_text`、`business_1_link`、`business_2_title`、`business_2_text`、`business_2_link`、`business_3_title`、`business_3_text`、`business_3_link` | [home-business.html](templates/partials/home-business.html)、[home-actions.html](templates/partials/home-actions.html) |
| 首页新闻 | `news_title`、`news_description` | [home-news.html](templates/partials/home-news.html) |
| 合作伙伴 | `partners_title`、`partners_description`、`partner_1_name`、`partner_1_image`、`partner_1_url`、`partner_2_name`、`partner_2_image`、`partner_2_url`、`partner_3_name`、`partner_3_image`、`partner_3_url`、`partner_4_name`、`partner_4_image`、`partner_4_url`、`partner_5_name`、`partner_5_image`、`partner_5_url`、`partner_6_name`、`partner_6_image`、`partner_6_url` | [home-partners.html](templates/partials/home-partners.html)、[home-actions.html](templates/partials/home-actions.html)、[home-rich.css](assets/css/home-rich.css) |
| 常见问题 | `faq_title`、`faq_description`、`faq_1_question`、`faq_1_answer`、`faq_2_question`、`faq_2_answer`、`faq_3_question`、`faq_3_answer`、`faq_4_question`、`faq_4_answer` | [home-faq.html](templates/partials/home-faq.html)、[home-actions.html](templates/partials/home-actions.html)、[home-rich.css](assets/css/home-rich.css) |
| 人才招聘 | `careers_title`、`careers_description`、`careers_button`、`careers_link` | [home-careers.html](templates/partials/home-careers.html)、[home-actions.html](templates/partials/home-actions.html) |
| 首页联系合作 | `contact_title`、`contact_description`、`contact_action_label`、`contact_action_url` | [home-contact.html](templates/partials/home-contact.html)、[home-actions.html](templates/partials/home-actions.html) |
| 联系信息与社交 | `contact_details_location`、`contact_email`、`contact_phone`、`contact_address`、`contact_hours`、`contact_map_url`、`contact_qr_image`、`contact_qr_label`、`social_1_label`、`social_1_url`、`social_2_label`、`social_2_url`、`social_3_label`、`social_3_url` | [contact-details.html](templates/partials/contact-details.html)、[home-contact.html](templates/partials/home-contact.html)、[footer.html](templates/partials/footer.html)、[chrome-options.css](assets/css/chrome-options.css) |
| 新闻列表与卡片 | `listing_title`、`listing_description`、`listing_layout`、`listing_columns`、`card_image`、`card_excerpt`、`card_date`、`card_category`、`card_excerpt_lines`、`show_breadcrumbs` | [list.html](templates/list.html)、[content.html](templates/partials/content.html)、[home-news.html](templates/partials/home-news.html)、[single.html](templates/single.html)、[series.html](templates/series.html)、[inner-options.css](assets/css/inner-options.css) |
| 文章阅读 | `article_cover`、`article_summary`、`article_meta`、`article_tags`、`article_toc`、`article_reading_time`、`article_copy_link`、`article_series`、`article_toc_open`、`article_updated`、`article_width`、`article_title_align` | [single.html](templates/single.html)、[reading-series.html](templates/partials/reading-series.html)、[reading.js](assets/js/modules/reading.js)、[inner-options.css](assets/css/inner-options.css) |
| 评论区 | `comments_title`、`comments_intro` | [comments.html](templates/partials/comments.html)、[inner-options.css](assets/css/inner-options.css) |
| 专题与系列 | `series_title`、`series_description` | [series.html](templates/series.html) |
| 客户案例 | `show_cases`、`cases_title`、`cases_description`、`case_1_*` 至 `case_3_*` | `home-enterprise.html`、`header.html`、`home-actions.html`；名称必填，图片和详情链接可选 |
| 合作与交付流程 | `show_process`、`process_title`、`process_1_*` 至 `process_4_*` | `home-enterprise.html`、`home-actions.html`；阶段名称为空则隐藏 |
| 资料中心 | `show_resources`、`resources_title`、`resources_description`、`resource_1_*` 至 `resource_3_*` | `home-enterprise.html`、`header.html`、`home-actions.html`；名称与真实地址同时填写 |
| 招聘岗位 | `job_1_*` 至 `job_3_*` | `home-enterprise.html`、`home-careers.html`；原生折叠查看要求，详情按钮连接内容页或招聘系统 |
| 企业页转化 | `page_contact_cta` | `single.html`；仅正式企业页面显示咨询区，草稿预览不显示 |

## 验证

设置校验使用真实主题 Manager，覆盖表单类型转换、隔离预览、版本冲突、保存、导出、重置、导入和重载。核心的[当前设置契约回归](../../internal/theme/current_settings_contract_test.go) 检查全部 CTA 字段接受完整站内路径或 HTTP(S) 地址，拒绝旧格式的裸片段链接。

```sh
go test ./internal/theme
go run ./cmd/themecheck -source themes/nexora
```
