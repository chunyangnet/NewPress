import { api } from './newpress-client';

Object.assign(typeof window !== 'undefined' ? window : globalThis, { NewPressClient: api });

// Retain require(existingAssetPath) for integrations and the runtime regression tests.
// The alias and explicit ESM marker keep esbuild from creating a private CJS shim.
declare const module: { exports: unknown } | undefined;
const commonJS = typeof module === 'undefined' ? undefined : module;
if (commonJS?.exports) commonJS.exports = api;
export {};
