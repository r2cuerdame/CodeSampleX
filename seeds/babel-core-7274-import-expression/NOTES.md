# Babel 7.27.4 dynamic `import()` AST shape

In `@babel/core` 7.27.4, `parseSync` keeps the Babel 7 compatibility default:
dynamic `import()` is a `CallExpression` whose callee is an `Import` node.
Code that visits only `ImportExpression` will therefore miss it.

Set `parserOpts.createImportExpressions: true` to opt into the standards-shaped
`ImportExpression`. That node stores its module specifier in `source` and has
neither `callee` nor `arguments`. Babel 8 makes this shape the default, so the
explicit two-shape contract is useful when migrating AST visitors.
