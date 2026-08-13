import ts from 'typescript';

// Type-checking without touching disk means supplying a CompilerHost: the
// default one reads files from the filesystem, so an in-memory source is
// invisible to it and every symbol resolves to "cannot find name".
export function check(source, options = {}) {
  const fileName = 'input.ts';
  const opts = { strict: true, noEmit: true, target: ts.ScriptTarget.ES2022, ...options };
  const sourceFile = ts.createSourceFile(fileName, source, opts.target, true);
  const host = {
    getSourceFile: (name) => (name === fileName ? sourceFile : undefined),
    writeFile: () => {},
    getDefaultLibFileName: () => 'lib.d.ts',
    useCaseSensitiveFileNames: () => true,
    getCanonicalFileName: (n) => n,
    getCurrentDirectory: () => '',
    getNewLine: () => '\n',
    fileExists: (name) => name === fileName,
    readFile: (name) => (name === fileName ? source : undefined),
  };
  const program = ts.createProgram([fileName], opts, host);
  return ts.getPreEmitDiagnostics(program).map((d) => ({
    code: d.code,
    message: ts.flattenDiagnosticMessageText(d.messageText, ' '),
  }));
}
