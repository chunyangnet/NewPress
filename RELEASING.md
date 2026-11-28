# 开源发布准备

当前发布版本为 **1.0.0-beta.2**，标签为 **v1.0.0-beta.2**。作者署名 ChunYang，公开联系邮箱 Chunnel@linux.do。主程序 GitHub： https://github.com/chunyangnet/NewPress ，Gitee： https://gitee.com/chunyangnet_0/newpress 。主题、插件保留现有独立 GitHub 账号和仓库。

## 提交隐私与仓库身份

只配置当前仓库，不修改维护者全局 Git 设置。首次提交前执行：

```sh
git init -b main
git config --local user.name "ChunYang"
git config --local user.email "76999815+chunyangnet@users.noreply.github.com"
git remote add origin https://github.com/chunyangnet/NewPress.git
git remote add gitee https://gitee.com/chunyangnet_0/newpress.git
```

已有同名远程时先检查 git remote -v，不盲目覆盖。两个平台推送同一份提交与标签，保留同一套作者身份；Gitee 的账号贡献归属显示需单独核验。隐私提交邮箱不用于收件；对外联系统一使用 Chunnel@linux.do。修改 Git 配置不会清除已有历史中的真实邮箱、凭据或私有文件。


## 核心发布签名密钥

核心更新使用独立 Ed25519 签名密钥。官方公钥随程序内置于 `internal/update/official_ed25519.pub`；仅公钥可进入源码。私钥保存在维护者机器的仓库外，不能放进 main、releases、Docker 上下文或构建日志。首次建立后持续使用同一密钥，不要每次发布重新生成。

当前维护机的固定位置通过本机 Git 配置登记，发布前查询：

```sh
git config --local --get newpress.coreSigningKey
```

推荐存放约定为用户目录下 `.newpress/release-keys/core-ed25519.key`，对应公钥为 `core-ed25519.key.pub`。本机的实际绝对路径仅记录在本地 Git 配置和维护记录中，不写入公共仓库。密钥目录只允许维护者和系统访问；另行保存受控的离线备份。

`scripts/build_update.py` 依次选择命令行 `--signing-key`、发布进程环境变量 `NEWPRESS_CORE_SIGNING_KEY`、本机 Git 配置 `newpress.coreSigningKey`。配置只保存路径；脚本缺少密钥时停止，不自动生成替代密钥。新机器或新检出应恢复原密钥并重新登记路径：

```sh
git config --local newpress.coreSigningKey /absolute/private/path/core-ed25519.key
```

`NEWPRESS_CORE_SIGNING_KEY` 仅供发布端使用，不要与部署端验证更新用的 `NEWPRESS_UPDATE_PUBLIC_KEY` 混淆。官方安装默认使用内置公钥；显式部署公钥可覆盖默认值，私有源仍要求配置自己的验证公钥。更换官方密钥需要单独制定旧安装的信任迁移方案，不能只替换发布仓库中的公钥文件。

## 公开范围

公开源码包含运行、开发和测试所需的代码、生成资源与许可声明。维护者本地的 `docs/` 为私有资料，不提交、不放入源码发布包，也不发送到 Docker 构建上下文。

- `.gitignore` 排除私有文档、本地工具、运行数据、凭据和产物。
- `.gitattributes` 用 `export-ignore` 排除私有文档，作为 `git archive` 的额外防线。
- `.dockerignore` 排除私有文档和本地凭据。
- CI 的 `scripts/check_public_source.py` 检查已跟踪文件中的私有路径、公开 Markdown 对私有文档的引用及常见凭据格式，发现时失败；该检查不是完整的秘密扫描审计。
- `scripts/build_ai_pro_max_release.py` 不再从私有文档目录读取或打包文件。

不要直接压缩整个本地工作目录作为公开源码。初次发布使用经过检查的 Git 提交生成源码归档：

```sh
git init -b main
python scripts/check_public_source.py --include-untracked
git add .
python scripts/check_public_source.py
git diff --cached --stat
# 审阅暂存内容并完成下方检查后再提交
git commit -m "Prepare initial public source"
git archive --format=zip --output=newpress-source.zip HEAD
```

忽略规则不移除已有 Git 历史中的文件。如果从其他仓库导入历史，需要单独确认历史中没有私有资料和凭据。

## 发布验证

在不含维护者私有资料的干净检出目录执行 CI 同等检查：前端生成一致性和测试、主题契约、部署脚本测试、许可清单、Go 测试（含 PostgreSQL 与 race）、vet、漏洞扫描及构建。命令见 `.github/workflows/ci.yml`。

公开 README 的运行、管理员引导、部署和备份步骤必须可独立使用。主题开发规范位于 `themes/README.md`；新安装的 Docker 镜像和原生主题目录仅包含 Chunnel，示例与市场主题按需安装，已有站点的主题和活动选择不被清理或重置。维护者素材生成与样机工具依赖的私有输入不随源码提供。

## 当前待确认事项

1. **字体已替换**：前后台统一使用 SIL OFL 1.1 的 Inter 与 Barlow Semi Condensed；B2 字体已移除。分发时保留各 `assets/fonts/` 中的许可及来源哈希清单。中文字体使用系统回退，不打包商业字体文件。
2. **表情素材许可已明确**：所有者选择 CC BY 4.0，署名为“NewPress 项目贡献者”，许可覆盖其有权授予的素材权利。各 `assets/images/emoji/` 随包附带许可、署名及来源说明；鲸伴组涉及非官方社区同人参考，第三方角色或商标权利仍需单独确认，不能由本项目的许可替代。
3. **私密漏洞报告入口**：SECURITY.md 已提供 Chunnel@linux.do；仓库公开后可另外启用 GitHub 私密漏洞报告。不要把尚未启用的平台功能写成唯一报告入口。

以上是现有声明记录的待办；不因主程序采用 Apache-2.0 而自动解决。确认并更新声明后，运行 `python scripts/collect_licenses.py` 重新生成内置清单，再运行 `--check`。

## 双源发布顺序

1. 完成上述检查，审阅公开文件清单，在 main 分支提交源码；版本默认值、README 徽章、Docker / Compose 和部署构建均为 1.0.0-beta.2。
2. 将同一份 main 提交推送到 origin 与 gitee，再在该提交建立 v1.0.0-beta.2 标签并推送两边。不得让同名标签指向不同源码，也不单独升级主题或插件版本。
3. 在 GitHub 与 Gitee 两种网络环境分别执行 README 的安装入口。确认 Gitee 入口只向 Gitee 获取安装器、部署包和镜像分片；验证原生 / Docker 初始化、管理员登录、重启与健康检查。仓库和标签尚不可访问时，README 的公网安装命令不代表已验证可用。
4. 使用已有 scripts/build_update.py 在发布机器生成并签名一次，--version 1.0.0-beta.2 --channel beta，并指定递增 --sequence、未来 --expires 和源码 --commit；签名密钥按上述本机配置读取，也可显式指定仓库外的 --signing-key。只有确认数据向后兼容时才添加 --data-compatible。不要在两个平台分别重新构建同一版本。
5. 发布产物包含 packages/、updates/beta.json、LICENSE 和 THIRD_PARTY_LICENSES.txt。在单独的 releases 发布分支维护这些文件（保留历史包），用同一提交同步推送两个主程序仓库。发布分支中的 ZIP 需要 git add -f 精确添加指定文件，不能强制添加整个工作目录。不要覆盖或强推 main；源码镜像同步不代替发布文件同步。
6. 核对生产签名密钥与内置官方公钥匹配。默认官方安装无需填写 NEWPRESS_UPDATE_PUBLIC_KEY；自定义发布者或私有源才需按受信渠道配置覆盖值。私钥始终保留在发布端，不能放进源码、发布分支或 Docker 上下文。未公开发布签名清单前，不宣称后台公网更新已经上线。
7. 验证双源检查、单源不可达、镜像落后、下载失败切换、升级、撤回与回退、备份恢复，再对外宣布 beta。GitHub Release / Gitee 发行版可附加相同包供人工下载，但后台当前读取的是 releases 分支，不能仅上传 Release 附件。

测试阶段使用 1.0.0-beta.N；功能冻结后使用 1.0.0-rc.N。正式验收通过后才发布 1.0.0，不以首次开源自动视为稳定版。

## 预编译部署包

发布端通过 `.github/workflows/release-build.yml` 在 Linux amd64 / arm64 主机构建应用及已修补的 PostgreSQL、NGINX、Redis 镜像，实测部署后保存镜像归档和二进制。`scripts/build_deployment.py` 将这些产物打包，剔除 Compose 的构建配置，生成平台清单、校验值和 48 MiB 镜像分片。所有分片、运行包及部署包在 GitHub Release 和 Gitee 的 `releases` 分支 `distributions/<版本>/` 保持相同内容。

一键安装器按架构获取 `*-deploy.tar.gz` 及其 `.sha256`。运行包不得包含私钥、环境密钥、已初始化的站点状态或私有文档。部署侧不会下载 Go 工具链或编译程序；完整源码检出仍可用于开发者自行构建。
