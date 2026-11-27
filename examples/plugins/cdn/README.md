# CDN 加速 1.1.0

NewPress 官方 CDN 管理插件，运行时 `builtin/cdn-v2`。

支持 Cloudflare、腾讯云 CDN、腾讯云 EdgeOne、阿里云 CDN、七牛 Fusion 和 Amazon CloudFront 的缓存操作；另提供主题资源加速、内容 / 对象存储刷新联动、持久化任务、HTTPS 诊断及 Cloudflare IP 优选。

Cloudflare 优选提供官方网段校验、IPv4/IPv6、成功率、TCP/TLS/TTFB、有限下载测试、CF-Ray 机房识别及 CSV/JSON/hosts 导出。测量来自服务器网络，不能固定 Anycast 全球机房。

1.1 新增 Cloudflare SaaS 回源与自定义主机名、实时验证记录、阿里云云解析 / DNSPod 的地区和运营商 CNAME 分流。DNS 变更需先预览再确认执行，支持逐步回执和受保护的 DNS 回滚。可显式开启健康监测、连续失败回退及冷却后恢复，仅修改本方案管理的记录。证书和主机名必须 active，且 NS 与实际 HTTPS 检查通过后才能切流。

此 ZIP 是受审核的宿主插件描述包，需要包含 `builtin/cdn-v2` 运行时和内置 `admin-cdn` 管理页的 NewPress 主程序。后台页面由主程序提供，独立于前台主题；旧主程序仅安装此包无法提供这些功能。新主程序兼容已安装的原版 1.0.0 描述包和配置，无需卸载。市场使用 v4 目录，旧目录和旧 ZIP 保留不变。

安装默认停用。管理入口 `/admin/plugins/cdn/`。先保存服务商配置并测试连接，确认域名、回源 HTTPS 和证书后再启用加速。缓存刷新只提交明确选择的对象或地址；DNS 分流先预览，再确认执行，失败时按回执核对已完成步骤，不能盲目重复变更。

SaaS 开通、计费、DNS 区域添加、NS 委派及 DNSSEC 由管理员在服务商控制台完成。健康监测与自动回退须显式启用，回滚仅作用于本方案管理的记录。未使用真实账号逐家生产验收。

许可证：[Apache-2.0](LICENSE)。第三方依赖与服务的许可边界见 [第三方声明](THIRD_PARTY_NOTICES.md)。

打包时保留 `LICENSE` 与 `THIRD_PARTY_NOTICES.md`；许可补全发行须协调宿主版本后使用新版本号、新 ZIP 路径与新签名目录，不能覆盖已发布的同版本包。
