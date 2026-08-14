import assert from "node:assert/strict";
import { createRequire } from "node:module";

import {
  binarySize,
  browserVersion,
  bundledNpmVersion,
  compile,
  elfNeededLibraries,
  installedTree,
  libcFamily,
  loadedNativeModules,
  loaderSource,
  lockedPlatformEntries,
  nativeBinaryPath,
  platformManifest,
  platformPackageName,
  requireFailure,
  transformThrows,
} from "../src/compile.mjs";

const require = createRequire(import.meta.url);

// The question itself: lightningcss compiles CSS on a musl image, installed
// with lifecycle scripts disabled. Shorthand merging is the cheapest proof
// that the native minifier ran and not some JS fallback.
assert.equal(
  compile(".foo { margin-left: 10px; margin-right: 10px; margin-top: 20px; margin-bottom: 20px; }"),
  ".foo{margin:20px 10px}",
);

// `code` comes back as a Buffer, and a Buffer coerces to its own utf8 text
// under == and in template strings while never being === the same string. So
// logging the result looks right and asserting on it strictly fails, which is
// the first thing people hit after getting the install itself sorted out.
const raw = require("lightningcss").transform({
  filename: "input.css",
  code: Buffer.from(".a{color:red}"),
  minify: true,
});
assert.equal(Buffer.isBuffer(raw.code), true);
assert.notStrictEqual(raw.code, ".a{color:red}");
assert.ok(raw.code == ".a{color:red}");
assert.equal(`${raw.code}`, ".a{color:red}");
assert.equal(raw.code.toString(), ".a{color:red}");

// Real lowering, driven by targets: Chrome 90 has no native nesting, so the
// nested rule is flattened. `blue` also minifies to #00f.
const nested = ".card { color: red; & .title { color: blue; } }";
assert.equal(
  compile(nested, { targets: { chrome: browserVersion(90) } }),
  ".card{color:red}.card .title{color:#00f}",
);
assert.equal(
  compile(nested, { targets: { chrome: browserVersion(130) } }),
  ".card{color:red;& .title{color:#00f}}",
);

// targets are packed as major << 16 | minor << 8 | patch. `{ chrome: 130 }`
// means Chrome 0.0.130, so it lowers everything and looks like targets were
// ignored. Same number, opposite result.
assert.equal(
  compile(nested, { targets: { chrome: 130 } }),
  ".card{color:red}.card .title{color:#00f}",
);

// @custom-media needs BOTH the draft flag and a targets object: the flag only
// makes it parse, targets are what make lightningcss lower it. With the flag
// alone the at-rule survives into the output, which reads like a silent
// failure. The lowered form joins the queries with `or`, not with a comma.
const customMedia = "@custom-media --modern (color), (hover);\n@media (--modern) { .a { color: red } }";
assert.equal(
  compile(customMedia, { drafts: { customMedia: true } }),
  "@custom-media --modern (color),(hover);@media (--modern){.a{color:red}}",
);
assert.equal(
  compile(customMedia, { drafts: { customMedia: true }, targets: { chrome: browserVersion(90) } }),
  "@media (color) or (hover){.a{color:red}}",
);

// Referencing an undefined custom media is a hard error with a machine
// readable shape, not a warning.
const undefinedMedia = transformThrows("@media (--nope) { .a { color: red } }", {
  drafts: { customMedia: true },
  targets: { chrome: browserVersion(90) },
});
assert.equal(undefinedMedia?.constructor.name, "SyntaxError");
assert.deepEqual(undefinedMedia.data, { type: "CustomMediaNotDefined", name: "--nope" });
assert.deepEqual(undefinedMedia.loc, { line: 1, column: 1 });

// Which binary is running, measured from the CJS cache rather than guessed.
// Exactly one .node was dlopen'd and it is the musl one.
const loaded = loadedNativeModules();
assert.equal(loaded.length, 1);
assert.equal(
  loaded[0].endsWith("/lightningcss-linux-x64-musl/lightningcss.linux-x64-musl.node"),
  true,
  loaded[0],
);

// It is lightningcss's own loader that made that choice, from detect-libc, at
// require time. npm never enters into it, and detect-libc is the only runtime
// dependency lightningcss has.
assert.deepEqual(libcFamily(), { family: "musl", isMusl: true });
assert.equal(platformPackageName(), "lightningcss-linux-x64-musl");
assert.deepEqual(installedTree().runtimeDependencies, ["detect-libc"]);

// That mechanism is quoted from the installed loader, not reconstructed from
// the fact that the right binary happened to load: familySync() decides the
// suffix, the platform package is required by name, and a .node in the package
// root is only the fallback. The fallback is what makes the classic error
// message misleading — see the end of this file.
const loaderSrc = loaderSource();
assert.ok(
  loaderSrc.includes("const { MUSL, familySync } = require('detect-libc');"),
  "loader no longer asks detect-libc for the family",
);
assert.ok(
  loaderSrc.includes("native = require(`lightningcss-${parts.join('-')}`);"),
  "loader no longer requires the platform package by name",
);
assert.ok(
  loaderSrc.includes("native = require(`../lightningcss.${parts.join('-')}.node`);"),
  "loader no longer falls back to a .node in the package root",
);

// Unlike esbuild, lightningcss really does publish per-libc platform packages,
// and each declares the libc field npm filters on.
const tree = installedTree();
assert.equal(tree.declaredPlatformPackages.length, 11);
assert.ok(tree.declaredPlatformPackages.includes("lightningcss-linux-x64-gnu"));
assert.ok(tree.declaredPlatformPackages.includes("lightningcss-linux-x64-musl"));
assert.deepEqual(platformManifest("lightningcss-linux-x64-musl").libc, ["musl"]);
assert.deepEqual(platformManifest("lightningcss-linux-x64-gnu").libc, ["glibc"]);
assert.deepEqual(platformManifest("lightningcss-linux-x64-musl").os, ["linux"]);

// And here is the part that contradicts the usual explanation. This tree was
// installed with `npm ci`, and BOTH linux-x64 libc variants are on disk. npm
// does honour libc when it resolves from the registry — a fresh `npm install`
// on this image installs the musl package alone — but the lock this image's
// npm writes records os and cpu per entry and no libc, so a later install from
// that lock has nothing to narrow linux-x64 with. os and cpu are recorded,
// which is why every darwin, win32, freebsd, android and arm64 package was
// still correctly skipped.
//
// What decides this is the npm that wrote the lock, not the lockfile format.
// This was npm/cli#8514, fixed by npm/cli#9025, which added `libc` to
// pkgMetaKeys in shrinkwrap.js and shipped in npm 11.11.0 on 2026-02-24:
// from 11.11.0 onward the writer records libc into a still-lockfileVersion-3
// lock and `npm ci` from it installs only the musl package.
//
// The fix is in the WRITER alone, which is what makes it easy to miss.
// Bisected on this image: npm 10.9.8 through 11.10.1 omit libc, 11.11.0
// onward record it. An npm 12 reading a lock written by npm 10 still
// installs both, and an npm 10 reading a lock written by npm 11.11.0
// installs only musl. So upgrading npm is not the fix — regenerating the
// lockfile is.
assert.deepEqual(tree.installedPlatformPackages, [
  "lightningcss-linux-x64-gnu",
  "lightningcss-linux-x64-musl",
]);
assert.match(
  bundledNpmVersion(),
  /^10\./,
  `image npm is ${bundledNpmVersion()}: npm 11.11.0 and later record libc in the lockfile, and a lock written by one of those narrows to the musl package alone`,
);
const { lockfileVersion, entries } = lockedPlatformEntries();
assert.equal(lockfileVersion, 3);
assert.equal(entries.size, 11);
for (const [name, entry] of entries) {
  assert.ok(Array.isArray(entry.os), `${name} has no os in the lockfile`);
  assert.ok(Array.isArray(entry.cpu), `${name} has no cpu in the lockfile`);
  assert.equal(entry.libc, undefined, `${name} unexpectedly records libc in the lockfile`);
  assert.equal(entry.optional, true);
}

// The cost of that is not theoretical: the glibc binary is a full copy of the
// same 10 MB addon, shipped in the image, unloadable, never opened.
const muslBytes = binarySize("lightningcss-linux-x64-musl");
const gnuBytes = binarySize("lightningcss-linux-x64-gnu");
assert.ok(muslBytes > 9_000_000, `musl binary is ${muslBytes} bytes`);
assert.ok(Math.abs(gnuBytes - muslBytes) / muslBytes < 0.05, `${muslBytes} vs ${gnuBytes}`);

// Why only one of them can ever work: these are dynamically linked shared
// objects, read here from their ELF dynamic sections. The musl build asks for
// musl's libc, the glibc build asks for glibc's loader and libc.
const muslNeeded = elfNeededLibraries(nativeBinaryPath("lightningcss-linux-x64-musl"));
assert.ok(muslNeeded.includes("libc.musl-x86_64.so.1"), muslNeeded.join(","));
assert.equal(muslNeeded.some((lib) => lib.startsWith("libc.so.")), false, muslNeeded.join(","));
const gnuNeeded = elfNeededLibraries(nativeBinaryPath("lightningcss-linux-x64-gnu"));
assert.ok(gnuNeeded.includes("libc.so.6"), gnuNeeded.join(","));
assert.ok(gnuNeeded.includes("ld-linux-x86-64.so.2"), gnuNeeded.join(","));

// The two failures people conflate, separated. Loading a wrong-libc binary
// that IS installed fails at dlopen, naming the missing loader.
const dlopenErr = requireFailure(nativeBinaryPath("lightningcss-linux-x64-gnu"));
assert.equal(dlopenErr?.code, "ERR_DLOPEN_FAILED");
assert.match(dlopenErr.message, /ld-linux-x86-64\.so\.2/);

// A platform package that is absent fails at resolution instead.
const missingPkg = requireFailure("lightningcss-darwin-arm64");
assert.equal(missingPkg?.code, "MODULE_NOT_FOUND");

// So "Cannot find module '../lightningcss.linux-x64-musl.node'" is not a
// broken package and not a wrong-libc binary. It is the fallback quoted above:
// a .node in the package root, which only exists after building lightningcss
// from source. When the platform package is missing — pruned, --no-optional,
// --omit=optional, or a node_modules copied in from a glibc machine — the
// fallback is what throws, so the error names a file nobody ever installed and
// hides the real cause. Same specifier as the loader's, required from the same
// directory.
assert.equal(tree.localBuildPresent, false);
const fallbackErr = requireFailure("../lightningcss.linux-x64-musl.node");
assert.equal(fallbackErr?.code, "MODULE_NOT_FOUND");
assert.match(fallbackErr.message, /Cannot find module '\.\.\/lightningcss\.linux-x64-musl\.node'/);

console.log("contract ok");
