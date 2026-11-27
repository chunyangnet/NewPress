# NewPress 官方插件

后台插件目录支持功能分类、标签搜索和安装状态筛选。新插件的元数据、开发与发布约定如下。

本站只提供官方插件。后台 `/admin/plugins/` 从部署者指定的官方 GitHub／Gitee 公共仓库加载已签名目录，按需安装、启用、停用和卸载。插件默认不安装；安装后默认停用。

随程序提供的「评论来源」插件可以离线安装，不会在启动时自动重装。插件工作目录为 `<slug>/data/`，卸载移除整个安装目录，包括设置、密钥、来源记录和缓存，保留站点评论与账号。

## 分类与发现

`plugin.json` 可声明主分类 `category` 和最多 8 个 `tags`。分类为 `content`、`comments`、`appearance`、`seo`、`analytics`、`integration`、`tools` 或 `other`。分类 ID 最长 40 个 ASCII 字符，以小写字母开头，其余为小写字母、数字或单个连字符，不能以连字符结尾；未知但有效的 ID 显示在“其他插件”。每个标签为 1–24 个 Unicode 字符，无首尾空白、换行或制表符，忽略大小写后不能重复。分类和标签不授予权限。

分类注册与校验见源码 `internal/plugin/categories.go`。发布包和签名目录中的 manifest 必须一致；不要修改已签名包的元数据。

## 开发边界

普通插件使用 `wasm/core-v1`，参考源码 `examples/plugins/content-age/` 与 `examples/plugins/background-reader/` 中的完整示例和构建说明。宿主运行时、清单及权限校验位于 `internal/plugin/`。

WASM 不提供 WASI、系统环境、文件、进程或直接数据库访问。只能使用宿主明确开放并经管理员授权的接口；不要假定任意网络、内容写入或后台任务能力可用。`content.render` 提供公开文章摘要，展示片段需要 `render.fragment` 权限，由主题转义成文本。管理操作和任务仍受真实登录、权限、配置版本与存储模式约束。

设置与授权由核心生成管理页，插件不能注入后台 HTML、脚本、模板或自定义保存地址。内置 `builtin/*` 包仅为宿主已注册功能的分发描述符，安装 ZIP 不能为旧主程序增加缺失的运行时。

## 打包与发布

从源码根目录运行，例如：

```sh
python scripts/build_extension.py --kind plugin --source examples/plugins/content-age --output output/content-age-1.0.0.zip
```

该工具只构建本地 ZIP，不签名、不发布。保留根目录 `plugin.json`、需要的 `plugin.wasm`、完整 `LICENSE` 与第三方许可；不要打包 `data/`、`.official.json`、`.integrity.json`、`.disabled`、`.review-required`、`.versions.json`、`.rollback/` 或 `.rollback-configuration.json`。

官方目录需要发行者签名、真实核心兼容范围及包摘要。正式发布使用新版本和新 ZIP 路径，不覆盖历史签名包；权限或配置变化需要管理员审阅。发布工具入口为源码 `cmd/pluginmarket/`；更新和回滚保留兼容配置，但不能据此跳过重新授权。
