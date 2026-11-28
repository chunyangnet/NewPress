# 预编译包与后台更新

`1.0.0-beta.2` 提供 Linux amd64、Linux arm64 和 Windows amd64 二进制。Go 程序与前端资源在发布端构建完成，使用方无需安装 Go、Node.js 或 npm。

下载入口：[GitHub Releases](https://github.com/chunyangnet/NewPress/releases/tag/v1.0.0-beta.2)。GitHub Release 附件使用 `DOWNLOAD_SHA256SUMS` 核对下载文件。Gitee 保留源码、签名清单与校验值镜像，大文件匿名下载受限。

## 一键部署

Linux 使用 README 中的 `install.sh` 入口，自动选择 amd64 或 arm64，下载 `newpress-1.0.0-beta.2-linux-<架构>-deploy.tar.gz` 并核验 SHA-256，再启动原有配置向导。无需 Git、Go、Node.js 或 Buildx。

- 原生模式直接安装包内二进制并创建 systemd 服务。
- Docker 模式按需下载镜像分片，逐片及整体校验后 `docker load`，再以 `docker compose up --no-build` 启动。应用、数据库、代理和可选 Redis 均在发布端构建。
- 默认 `--source auto` 并行采样 GitHub、gh-proxy.com、ghfast.top、ghproxy.net，按实际文件传输速度选择最快源，失败自动切换。`--source github` 仅直连下载。
- 官方 GitHub / Gitee 提供校验值；加速站只传输包，不提供信任依据。每个部署包、镜像分片及完整镜像均校验 SHA-256。

预构建镜像体积大于原生程序，只在选择 Docker 且缺少匹配的本地镜像时下载。系统尚未安装 Docker 或 Python 时，向导仍会按需安装这些运行与部署组件。

## 手动运行

选择 `newpress-1.0.0-beta.2-<系统>-<架构>-runtime.zip`，解压到新目录。运行包包含程序、Chunnel 主题、默认插件及许可证；源码与开发工具无需部署。不要将完整运行包覆盖到已有站点目录。

在解压后的根目录运行。Linux 先赋予执行权限，生成管理员密码哈希，再启动：

```sh
chmod +x bin/newpress
./bin/newpress admin hash-password
export NEWPRESS_ADMIN_PASSWORD_HASH='<上一步输出的哈希>'
export NEWPRESS_PUBLIC_URL='http://127.0.0.1:8080'
export NEWPRESS_ADDR='127.0.0.1:8080'
./bin/newpress run
```

Windows PowerShell：

```powershell
.\bin\newpress.exe admin hash-password
$env:NEWPRESS_ADMIN_PASSWORD_HASH='<上一步输出的哈希>'
$env:NEWPRESS_PUBLIC_URL='http://127.0.0.1:8080'
$env:NEWPRESS_ADDR='127.0.0.1:8080'
.\bin\newpress.exe run
```

打开 `/admin/login/`，默认管理员用户名为 `admin`。上线到自己的域名时，设置实际 `NEWPRESS_PUBLIC_URL`，并配置 HTTPS 反向代理和服务管理。所有配置项见 [.env.example](.env.example)；直接运行程序时需显式传入环境变量，示例文件不会自动加载。默认站点数据写入 `var/`，文件存储支持一个写入进程。

## 已有站点更新

本次以新版首次安装为基准，不承诺旧开发快照的数据兼容。通过 `newpress run` 启动的新站点，在后台“核心更新”使用官方公开双源检查更新，确认备份后安装。程序下载 `packages/1.0.0-beta.2/newpress-<系统>-<架构>.zip`，验证内置 Ed25519 公钥、发布序号、SHA-256 和平台信息，再切换程序并重启。更新主机不执行 Go、npm、Git 或 Docker 构建。

核心更新包只含 `bin/newpress` 或 `bin/newpress.exe`。站点配置、数据、插件和已安装主题保留；完整运行包中的 Chunnel 仅用于新安装。需要升级现有 Chunnel 时，可在主题管理中安装本次发布的 `chunnel-0.48.21.zip`。

仅用 `serve` 启动的进程需先改用 `run`。标准部署向导已使用托管模式；Docker 的更新目录必须持久化。初始托管程序、基础镜像或部署协议的变更仍通过对应部署流程处理。

PostgreSQL 站点需按 [README 的恢复回执说明](README.md#后台核心更新) 先完成备份和恢复演练。升级失败的程序回退不等于数据库恢复。

核心包同样自动测速 GitHub 加速站，下载失败或 SHA-256 不符时切换；只有签名清单确认的提交、长度和摘要可用于安装。

后台更新清单位于两仓库同一 `releases` 分支提交的 `updates/beta.json`，与 GitHub Release 附件使用相同核心包。安装来源无须访问令牌；不要下载或配置任何发布私钥。
