import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);

// TypeScript 7 is the native port, and its npm package no longer ships the
// JavaScript compiler API: `typescript@7` exposes only `version` and
// `versionMajorMinor`, from BOTH the ESM and CommonJS entry points. Every
// tool built on ts.createProgram, ts.transpileModule or ts.createSourceFile
// stops working the moment the dependency resolves to 7 — and it fails as
// "Cannot read properties of undefined", not as a missing module, because
// the package is still there.
//
// The API also has no ESM named exports on 5.x: `import ts from
// 'typescript'` gives the namespace as a default export, and destructuring
// named bindings from it is what breaks under bundlers.
export function loadCompiler(specifier = 'typescript') {
  return require(specifier);
}

export function hasCompilerAPI(ts) {
  return typeof ts.createProgram === 'function' &&
    typeof ts.createSourceFile === 'function' &&
    ts.ScriptTarget !== undefined;
}

// Type-check a source string without touching disk.
//
// The CompilerHost is the whole trick, and hand-rolling one is where this
// usually goes wrong. The default library is not a single file: lib.*.d.ts
// pulls in more lib files through /// <reference lib="..." /> directives,
// so a host that serves only the one file it was told about type checks
// against no global types and reports TS2318 "Cannot find global type
// 'Array'" — which reads like a bug in the checked code rather than in the
// host. Delegating to ts.createCompilerHost and intercepting ONLY the
// in-memory file keeps that resolution intact.
export function check(source, options = {}) {
  const ts = loadCompiler('typescript');
  const fileName = 'input.ts';
  const opts = { strict: true, noEmit: true, target: ts.ScriptTarget.ES2022, ...options };

  const inMemory = ts.createSourceFile(fileName, source, opts.target, true);
  const base = ts.createCompilerHost(opts, true);
  const host = {
    ...base,
    getSourceFile: (name, languageVersion, onError) =>
      (name === fileName ? inMemory : base.getSourceFile(name, languageVersion, onError)),
    fileExists: (name) => name === fileName || base.fileExists(name),
    readFile: (name) => (name === fileName ? source : base.readFile(name)),
    writeFile: () => {},
  };

  const program = ts.createProgram([fileName], opts, host);
  return ts.getPreEmitDiagnostics(program).map((diag) => ({
    code: diag.code,
    message: ts.flattenDiagnosticMessageText(diag.messageText, ' '),
  }));
}
