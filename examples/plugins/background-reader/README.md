# Background reader 示例

这是后台任务与事件 hook 的最小只读源码示例，不是可安装的官方发行包。不提供签名、不伪造官方收据，也不改变宿主安装校验。

每次处理调用 `settings.get`，仅在宿主响应成功后返回业务 JSON；没有写入、网络访问或二次入队。其用途是展示 `wasm/core-v1` ABI 和两个后台入口，不展示内容写入功能。

此最小示例依赖当前 v1 宿主 JSON 编码中末尾的顶层 `,"ok":true}` 标记，按完整后缀检查，不搜索设置值中的同名文本。宿主拒绝、输出溢出或编码变化均触发 trap，由宿主记录执行失败；生产插件应使用完整 JSON 解析器读取顶层 `ok`。

## 构建

需安装支持 wasm32 的 Zig 工具链。在本目录运行：

```powershell
zig cc -target wasm32-freestanding -O2 -nostdlib -Wl,--no-entry -Wl,--export-memory -Wl,-z,stack-size=65536 -Wl,--initial-memory=262144 -Wl,--max-memory=33554432 -o plugin.wasm plugin.c
```

2026-11-24 已在 Windows 使用 Zig 0.16.0 按上述参数编译本示例。`internal/plugin/testdata/background-reader.wasm` 是由该 C 源码生成的测试夹具，`background-reader.build.json` 记录源码/产物 SHA-256 和构建命令。常规 Go 测试直接读取夹具并核对摘要，不自动调用 C 编译器；修改源码后须重编夹具并更新摘要。

在项目根目录运行：

```powershell
go test ./internal/plugin -run '^TestBackgroundReaderExample' -count=1
```

测试在临时目录使用测试安装凭据，实际执行两个 hook，验证只读宿主成功响应、文件 worker 完成、队列重开后的去重与新任务执行，以及宿主拒绝/撤权/旧代际后的重试或取消。它不生成官方签名包，不能代替正式发行与部署验收。

## 宿主约束

- 通过正式发行校验后，管理员需保存 `settings.read` 授权并启用插件。
- `task.execute.v1` 接收管理员通过任务 API 提交的 payload。
- `event.deliver.v1` 接收标识符事件；在设置页显式勾选订阅并保存后才接收新事件。
- 执行是至少一次；插件不得假设仅调用一次。
- 后台 v1 只开放 `settings.get`、`cache.get`、公开内容 `content.get`，且仍受 manifest 和已保存授权限制。
- 任务输出不作为公开文章渲染内容；成功状态记录在宿主任务列表。
- 保存配置、停用再启用或升级会改变授权代际。过期任务不能借新配置重新获得权限。
- 手动恢复文件或 PostgreSQL 备份后，先停止所有站点实例及 worker，再对恢复目标运行 `newpress plugin cancel-restored-tasks --confirm-all-workers-stopped`。旧活动任务和冻结事件回执会取消，历史幂等键保留；需要重做的业务应由管理员核对后用新键提交。命令不撤销已经发生的副作用，也不释放保留配额。
