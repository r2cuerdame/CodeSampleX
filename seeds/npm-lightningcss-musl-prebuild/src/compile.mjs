import { createRequire } from "node:module";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const require = createRequire(import.meta.url);
const seedRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

/**
 * "Run lightningcss on Alpine" is the opposite problem from running esbuild
 * there, and the two get the same advice, which is wrong for one of them.
 *
 * esbuild ships one static Go binary per platform with no libc variants at
 * all. lightningcss ships a real napi shared object per platform AND per
 * libc: lightningcss-linux-x64-gnu links against ld-linux-x86-64.so.2 and
 * libc.so.6, lightningcss-linux-x64-musl links against libc.musl-x86_64.so.1.
 * Only one of the two can be dlopen'd on a given image, so something has to
 * choose, and the interesting question is what.
 *
 * The folklore answer is "npm picks it, because the platform packages declare
 * a libc field". That is only half right, and the half that is wrong is the
 * half that matters in a Dockerfile. See lockedPlatformEntries below.
 *
 * What actually loads the right binary is lightningcss's own loader: it calls
 * detect-libc's familySync() at require time and requires
 * `lightningcss-${platform}-${arch}-${musl|gnu}` by name. npm's filtering
 * only decides which packages are on disk; the loader decides which one runs.
 */

/**
 * lightningcss targets are packed version numbers: major << 16 | minor << 8 |
 * patch. Passing a bare `{ chrome: 130 }` is not Chrome 130, it is Chrome
 * 0.0.130, so every lowering fires and you conclude that targets are ignored
 * or that lightningcss is broken. The shift is not optional.
 */
export function browserVersion(major, minor = 0, patch = 0) {
  return (major << 16) | (minor << 8) | patch;
}

/**
 * transform() takes a Buffer and returns `code` as a Buffer, not a string.
 * Comparing the result to a string silently fails for the same reason
 * Buffer.from("a") !== "a".
 */
export function compile(css, options = {}) {
  const lightningcss = require("lightningcss");
  const { code } = lightningcss.transform({
    filename: "input.css",
    minify: true,
    ...options,
    code: Buffer.from(css),
  });
  return code.toString();
}

export function transformThrows(css, options = {}) {
  try {
    compile(css, options);
    return null;
  } catch (err) {
    return err;
  }
}

/** The specifier lightningcss's loader builds for this machine. */
export function platformPackageName() {
  const { MUSL, familySync } = require("detect-libc");
  const parts = [process.platform, process.arch];
  if (process.platform === "linux") {
    if (familySync() === MUSL) parts.push("musl");
    else if (process.arch === "arm") parts.push("gnueabihf");
    else parts.push("gnu");
  } else if (process.platform === "win32") {
    parts.push("msvc");
  }
  return `lightningcss-${parts.join("-")}`;
}

export function libcFamily() {
  const { familySync, MUSL } = require("detect-libc");
  return { family: familySync(), isMusl: familySync() === MUSL };
}

function loaderPath() {
  // lightningcss has an exports map with no "./package.json" entry, so
  // require.resolve("lightningcss/package.json") throws
  // ERR_PACKAGE_PATH_NOT_EXPORTED. Walk up from the loader instead.
  return require.resolve("lightningcss");
}

/**
 * The loader's own source. Every claim about how the platform package gets
 * chosen is checkable there, so read it instead of inferring the mechanism
 * from the fact that the right binary happened to load.
 */
export function loaderSource() {
  return readFileSync(loaderPath(), "utf8");
}

export function installedTree() {
  const loader = loaderPath();
  const packageDir = path.resolve(path.dirname(loader), "..");
  const nodeModules = path.resolve(packageDir, "..");
  const manifest = JSON.parse(readFileSync(path.join(packageDir, "package.json"), "utf8"));
  return {
    loader,
    packageDir,
    nodeModules,
    declaredPlatformPackages: Object.keys(manifest.optionalDependencies ?? {}),
    runtimeDependencies: Object.keys(manifest.dependencies ?? {}),
    installedPlatformPackages: readdirSync(nodeModules)
      .filter((name) => name.startsWith("lightningcss-"))
      .sort(),
    // The loader's fallback target, in the package root one level above
    // node/index.js. The published tarball's files list is node/*.js plus the
    // type files, so this path exists only after a local build from source.
    localBuildPresent: existsSync(
      path.join(packageDir, `${platformPackageName().replace("lightningcss-", "lightningcss.")}.node`),
    ),
  };
}

export function platformManifest(name) {
  return require(`${name}/package.json`);
}

export function nativeBinaryPath(name) {
  const { nodeModules } = installedTree();
  return path.join(nodeModules, name, platformManifest(name).main);
}

export function binarySize(name) {
  return statSync(nativeBinaryPath(name)).size;
}

/**
 * Which .node files this process actually dlopen'd. Native addons land in the
 * CJS module cache keyed by their resolved filename, so this is a receipt
 * rather than an inference about what the loader chose.
 */
export function loadedNativeModules() {
  return Object.keys(require.cache)
    .filter((file) => file.endsWith(".node"))
    .sort();
}

/**
 * The lockfile's own view of the platform packages. The npm that ships in
 * node:22-alpine records `os` and `cpu` per entry and drops `libc`, even
 * though the registry manifests carry it, which is why `npm ci` on Alpine
 * cannot narrow linux-x64 down to one libc the way `npm install` can. What
 * decides this is the npm that wrote the lock, not the format: npm 12 writes
 * `libc` into a lockfileVersion 3 lock and then installs only the musl one.
 */
export function lockedPlatformEntries() {
  const lock = JSON.parse(readFileSync(path.join(seedRoot, "package-lock.json"), "utf8"));
  const entries = new Map();
  for (const [key, value] of Object.entries(lock.packages)) {
    const name = key.replace(/^.*node_modules\//, "");
    if (!name.startsWith("lightningcss-")) continue;
    entries.set(name, { os: value.os, cpu: value.cpu, libc: value.libc, optional: value.optional });
  }
  return { lockfileVersion: lock.lockfileVersion, entries };
}

/**
 * The npm bundled with the image, read beside the running node. The lockfile
 * above is whatever this npm wrote, so the version is part of the finding.
 */
export function bundledNpmVersion() {
  const manifest = path.join(path.dirname(process.execPath), "..", "lib", "node_modules", "npm", "package.json");
  return JSON.parse(readFileSync(manifest, "utf8")).version;
}

export function requireFailure(specifier, from = loaderPath()) {
  const scoped = createRequire(from);
  try {
    scoped(specifier);
    return null;
  } catch (err) {
    return err;
  }
}

/**
 * DT_NEEDED entries of an ELF64 shared object, read from the dynamic section
 * rather than taken on faith. A .node is ET_DYN with no PT_INTERP, so the
 * only evidence of which libc it was built against is the list of libraries
 * it asks the loader for.
 */
export function elfNeededLibraries(binaryPath) {
  const buf = readFileSync(binaryPath);
  if (buf.toString("latin1", 0, 4) !== "\x7fELF") throw new Error("not an ELF file");
  if (buf[4] !== 2) throw new Error("expected a 64-bit ELF");

  const phoff = Number(buf.readBigUInt64LE(0x20));
  const phentsize = buf.readUInt16LE(0x36);
  const phnum = buf.readUInt16LE(0x38);

  const PT_LOAD = 1;
  const PT_DYNAMIC = 2;
  const loads = [];
  let dynamic = null;
  for (let i = 0; i < phnum; i++) {
    const at = phoff + i * phentsize;
    const type = buf.readUInt32LE(at);
    const segment = {
      offset: Number(buf.readBigUInt64LE(at + 0x08)),
      vaddr: Number(buf.readBigUInt64LE(at + 0x10)),
      filesz: Number(buf.readBigUInt64LE(at + 0x20)),
    };
    if (type === PT_LOAD) loads.push(segment);
    if (type === PT_DYNAMIC) dynamic = segment;
  }
  if (!dynamic) throw new Error("no PT_DYNAMIC segment");

  const fileOffsetOf = (vaddr) => {
    const load = loads.find((s) => vaddr >= s.vaddr && vaddr < s.vaddr + s.filesz);
    if (!load) throw new Error(`vaddr 0x${vaddr.toString(16)} is in no PT_LOAD segment`);
    return load.offset + (vaddr - load.vaddr);
  };

  const DT_NULL = 0;
  const DT_NEEDED = 1;
  const DT_STRTAB = 5;
  const needed = [];
  let strtab = null;
  for (let at = dynamic.offset; at < dynamic.offset + dynamic.filesz; at += 16) {
    const tag = Number(buf.readBigUInt64LE(at));
    const value = Number(buf.readBigUInt64LE(at + 8));
    if (tag === DT_NULL) break;
    if (tag === DT_STRTAB) strtab = fileOffsetOf(value);
    if (tag === DT_NEEDED) needed.push(value);
  }
  if (strtab === null) throw new Error("no DT_STRTAB");

  return needed.map((offset) => {
    const start = strtab + offset;
    const end = buf.indexOf(0, start);
    return buf.toString("latin1", start, end);
  });
}
