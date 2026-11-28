NewPress 1.0.0-beta.2

本次以新版首次安装为基准。完成 TypeScript 前端迁移，统计页保留轻量报告并提供 React 交互图表；文章和评论选择表情后自动关闭面板。

一键入口直接下载预编译部署包。原生模式使用包内程序，Docker 模式下载并导入发布端构建好的镜像，无需在安装主机编译 Go、前端或 Docker 镜像。支持 Linux amd64、Linux arm64 和 Windows amd64。部署包、Docker 镜像及后台核心包会在服务器上测速 GitHub 与多个加速站，优先最快可用源，失败自动切换。校验值和签名清单仅从官方仓库获取。

后台官方更新通过 releases 分支的签名清单下载已编译核心包并自动重启，无需 Go、Node.js、npm、Git 或 Docker 构建命令。使用 newpress run 托管模式。更新前确认备份；PostgreSQL 站点需有效恢复回执。首次安装 beta.2 后，后台会显示当前版本，后续版本继续采用此更新机制。

完整运行包包含 Chunnel 0.48.21；市场主题沿用独立版本。签名私钥不随包分发。

Source commit: `e345bcb7f46955fc37ded8e1ff6f2e81978ccc2b`
