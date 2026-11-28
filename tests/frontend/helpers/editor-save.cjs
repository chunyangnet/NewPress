const fs = require('node:fs');
const { stripTypeScriptTypes } = require('node:module');
const { loadNativeModule } = require('./typescript.cjs');

// Keep the pre-existing focused VM fixtures for the save state machine while
// exercising the same typed response parsers as the bundled browser entry.
const source = () => stripTypeScriptTypes(fs.readFileSync('frontend/core/admin-editor.ts', 'utf8'))
  .replace(/(\.querySelector(?:All)?)\s+\(/g, '$1(');
const helpers = loadNativeModule('frontend/core/editor-save-data.ts');
class SelectControl { static [Symbol.hasInstance](element) { return element?.tagName === 'SELECT'; } }
const context = { ...helpers, Error, HTMLSelectElement: SelectControl };
module.exports = { source, context };
