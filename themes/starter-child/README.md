# Starter 子主题示例

只维护差异，不复制 Starter 的列表、文章和基础布局。

- `theme.json`：schema 2 单层父依赖。
- `settings.schema.json`：只调整默认强调色，保存值与父主题隔离。
- `templates/partials/footer.html`：覆盖页脚，保留站点备案和版权配置。
- `templates/partials/child-head.html`：通过父主题扩展槽追加 CSS。
- `assets/css/child.css`：子主题样式。

父主题须是包含 child-head 扩展槽及 footer partial 的 Starter；旧安装包应先更新。离线检查：

```powershell
go run ./cmd/themecheck --source themes/starter-child --themes-dir themes
```

包内无需携带父主题。先在目标站点安装兼容父包，再安装子包；不要直接修改运行站点的资源文件。源码仓库中的完整规则见 [主题开发规范](../README.md)。代码采用随包 [LICENSE](LICENSE) 中的 Apache-2.0。

## 修改和分发

复制本示例后修改目录名、slug、name、author 与 version。保留 schema 2 和明确的 parent.slug/version；父主题必须已安装、没有自己的父主题，且 engine/view 与子主题一致。省略 capabilities 时继承，显式填写时必须与父主题一致。

模板、资源按相同相对路径覆盖。新增 CSS/JS 放入本包，通过父主题已调用的扩展槽和 `asset` 帮助函数引用；不要复制整份基础模板或手写父资源地址。继承的设置字段只能覆盖 default、title、description，新增字段需要完整定义；保存值按子 slug 隔离。

从源码仓库根目录构建本示例：

```powershell
python scripts/build_extension.py --kind theme --source themes/starter-child --output output/starter-child-1.0.0.zip
```

子包只包含差异和许可证，不包含父包或站点私有状态。安装后由管理员显式选择；本示例不会在新站点默认安装。父主题升级和回滚须兼容所有已安装子主题；依赖存在时不能卸载父主题。离线检查通过后仍需验证实际页面与浏览器交互。
