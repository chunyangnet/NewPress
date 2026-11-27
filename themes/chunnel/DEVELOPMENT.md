# Chunnel 资源开发

主题运行和安装使用已生成的 JS/CSS，不需要 Node.js。

Chunnel 的组合资源在 NewPress 源码仓库中维护。运行 `npm ci --ignore-scripts`、`npm run typecheck:frontend`、`npm run build:frontend`、`npm run check:frontend` 和 `npm run test:frontend`。资源变化须提升 `theme.json` 版本，并由发行流程重新生成主题包完整性信息。

`assets/js/chunnel-time.js`、`chunnel-home-member.js` 和 `chunnel-home-features.js` 内的时间逻辑来自仓库级 `frontend/shared/site-time.ts` 与 `site-time-global.ts`；不要编辑这些生成文件，也不要在主题中重建 `chunnel-time.source.js` 副本。共享入口映射在 `frontend/entries.json`。只持有安装后的主题目录时，应取得对应 NewPress 源码版本进行开发；主题包继续携带可直接运行的资源。

首页 features 合包中的 `NewPressClient` 来自 `frontend/shared/newpress-client.ts` 与 `newpress-client-global.ts`，构建时直接编译该入口，不把核心生成的 JS 当作可编辑源码。其他页面继续按原 URL 加载核心客户端；主题内不维护另一份客户端。共享客户端保持无 DOM 依赖，也不引入 React。

`assets/js/chunnel-selects.js` 和按需加载的 `chunnel-choice-menu.js` 分别由 `frontend/shared/selects.ts`、`choice-menu.ts` 生成，公共契约位于 `choice-types.ts`。核心与主题脚本使用同一入口；不要编辑生成 JS 或恢复旧 `.source.js` 副本。配套 `chunnel-selects.source.css` 与 `chunnel-choice-menu.source.css` 继续保留核心/主题两份，构建会检查一致性。修改后运行 `node --test tests/frontend/native-selects.test.cjs tests/frontend/select-dropdowns.test.cjs`，同时验证 TS 源码和发布包。

`assets/js/chunnel-color-mode.js`、`chunnel-color-reveal.js` 分别来自 `frontend/shared/color-mode.ts`、`color-reveal.ts`，共享类型在 `color-types.ts`；首页 `chunnel-home.js` 和 `chunnel-home-glass.js` 的外观逻辑也直接编译同一 TS 入口。不要恢复旧 `.source.js` 副本。修改后运行 `node --test tests/frontend/color-mode.test.cjs tests/frontend/color-mode-browser.test.cjs`，并检查首页两套外观的 HTTP 资源预算；同步主题不能延迟到 DOMContentLoaded 后。

后台与 React 统计试用属于核心 UI，不由 Chunnel 提供。主题开发无需 React，不应复制后台入口或修改后台资源。其他独立主题可以采用自己的工具链；仓库的 `scripts/build_theme.mjs` 是独立主题的可选惯例构建器，不替代 Chunnel 的组合构建。
