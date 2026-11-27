# 对象存储 1.0.0

本目录是可重建的宿主插件分发描述符，不包含独立 WASM 或可加载的 Go 动态库。需要运行时 `builtin/object-storage-v1` 已注册且包含对象存储管理页的 NewPress 主程序。后台由核心提供，与前台主题无关；旧版主程序不能通过此 ZIP 单独获得对象存储功能。

支持 S3、Cloudflare R2、腾讯 COS、阿里 OSS、七牛、Cloudreve 和 WebDAV；提供多配置、媒体镜像、摘要校验、失败重试、CDN/签名访问及显式清理。本地媒体保留。Cloudreve 使用独立 WebDAV 账号；未配置可公开下载地址时继续从本地读取。

离线安装：`newpress plugin install object-storage`。初次安装默认停用，在 `/admin/plugins/object-storage/` 保存配置并完成连接测试后启用。

重建 ZIP（从 NewPress 源码根目录运行）：

```sh
python scripts/build_extension.py --kind plugin --source examples/plugins/object-storage --output output/packages/object-storage-1.0.0.zip
```

构建工具只生成本地 ZIP，不签名或发布。官方目录发布需要配套新版主程序、真实发行兼容范围及发布者签名。不要修改现有签名目录伪造发布。

配置时先创建独立服务账号并填写对应服务的 endpoint、存储桶或目录及凭据；先测试连接，再启用镜像。公网/CDN 地址需能真实读取目标对象；私有对象按对应服务配置签名访问，不把密钥放进公开 URL。上传、校验、失败重试和显式清理均从管理页执行，本地媒体继续保留；更换后端或删除远端对象前先备份并检查站点引用。

代码、描述文件与文档采用 [Apache-2.0](LICENSE)。第三方依赖与服务的许可边界见 [第三方声明](THIRD_PARTY_NOTICES.md)。

打包时保留 `LICENSE` 与 `THIRD_PARTY_NOTICES.md`；许可补全发行须协调宿主版本后使用新版本号、新 ZIP 路径与新签名目录，不能覆盖已发布的同版本包。
