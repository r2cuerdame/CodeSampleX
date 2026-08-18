import assert from "node:assert/strict";
import babel from "@babel/core";

const source = 'const pending = import("./feature.js");';
const options = {
  sourceType: "module",
  configFile: false,
  babelrc: false,
};

assert.equal(babel.version, "7.27.4");
assert.equal(typeof babel.parseSync, "function");
assert.equal(typeof babel.types.isImportExpression, "function");

const legacyFile = babel.parseSync(source, options);
const legacyExpression = legacyFile.program.body[0].declarations[0].init;
assert.equal(legacyExpression.type, "CallExpression");
assert.ok(babel.types.isCallExpression(legacyExpression));
assert.ok(babel.types.isImport(legacyExpression.callee));
assert.equal(babel.types.isImportExpression(legacyExpression), false);
assert.equal(legacyExpression.arguments[0].value, "./feature.js");

const expressionFile = babel.parseSync(source, {
  ...options,
  parserOpts: { createImportExpressions: true },
});
const importExpression = expressionFile.program.body[0].declarations[0].init;
assert.equal(importExpression.type, "ImportExpression");
assert.ok(babel.types.isImportExpression(importExpression));
assert.equal("callee" in importExpression, false);
assert.equal("arguments" in importExpression, false);
assert.equal(importExpression.source.value, "./feature.js");

console.log("CONTRACT PASS: Babel 7.27.4 dynamic import AST opt-in boundary");
