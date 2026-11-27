// Exercise the compiled browser/module boundary without reading or slicing TS.
const path = require('node:path');
const vm = require('node:vm');
const { buildSync } = require('esbuild');
const root = path.resolve(__dirname, '../../..');
function compile(source, format = 'iife') {
  return buildSync({ absWorkingDir: root, entryPoints: [source], bundle: true, write: false,
    platform: 'browser', format, target: ['chrome100', 'firefox110', 'safari16'],
    tsconfig: path.join(root, 'tsconfig.frontend.json'), legalComments: 'none' }).outputFiles[0].text;
}
function loadModule(source, context = {}) {
  const module = { exports: {} };
  vm.runInNewContext(compile(source, 'cjs'), { module, exports: module.exports, ...context }, { filename: source });
  return module.exports;
}
function loadModules(sources, context = {}) {
  const code = buildSync({ absWorkingDir: root, stdin: { contents: sources.map((source, i) => `export * as module${i} from ${JSON.stringify('./' + source)};`).join('\n'), resolveDir: root },
    bundle: true, write: false, format: 'cjs', platform: 'browser', target: 'es2022' }).outputFiles[0].text;
  const module = { exports: {} };
  vm.runInNewContext(code, { module, exports: module.exports, ...context });
  return sources.map((_, i) => module.exports[`module${i}`]);
}
module.exports = { compile, loadModule, loadModules };
