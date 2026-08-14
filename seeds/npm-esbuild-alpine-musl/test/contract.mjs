import assert from "node:assert/strict";
import { createRequire } from "node:module";

import {
  elfInterpreter,
  platformPackage,
  transformTypeScript,
} from "../src/bundler.mjs";

const require = createRequire(import.meta.url);

// The question itself: esbuild runs, on musl, with lifecycle scripts off.
const code = await transformTypeScript("const x: number = 1; export default x");
assert.equal(code.trim(), "const x = 1;\nexport default x;");

const { optionalDependencies, declaresPostinstall, installed } = platformPackage();

// There is no musl build to go looking for. Every linux entry is a plain
// architecture, so the advice to install a "-musl" variant has nothing to
// install.
assert.ok(optionalDependencies.length > 0);
assert.equal(
  optionalDependencies.filter((name) => name.includes("musl")).length,
  0,
);
assert.ok(optionalDependencies.includes("@esbuild/linux-x64"));

// And the package npm chose does not constrain libc, which is why npm
// installs it on Alpine in the first place.
assert.deepEqual(installed.os, ["linux"]);
assert.deepEqual(installed.cpu, ["x64"]);
assert.equal(installed.libc, undefined);

// The postinstall script exists — it just is not how the binary arrives.
// This contract ran with --ignore-scripts, so the transform above already
// proved that; asserting the script is present keeps the two facts from
// being confused for each other later.
assert.equal(declaresPostinstall, true);

// Why any of this works: the binary has no ELF interpreter, so there is no
// dynamic loader to disagree about. Checked by reading the program headers
// rather than by trusting the claim.
const binary = require.resolve("@esbuild/linux-x64/bin/esbuild");
assert.equal(elfInterpreter(binary), null);

// The check is real: node itself is dynamically linked, and on this image
// the interpreter it names is musl's.
const nodeInterp = elfInterpreter(process.execPath);
assert.ok(nodeInterp && nodeInterp.includes("musl"), `node interpreter: ${nodeInterp}`);

console.log("contract ok");
