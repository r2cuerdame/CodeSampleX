import { createRequire } from "node:module";
import { openSync, readSync, closeSync } from "node:fs";

const require = createRequire(import.meta.url);

/**
 * "Does esbuild work on Alpine?" — the folklore says you need a musl build,
 * and the measurement says there isn't one and you don't.
 *
 * esbuild ships one optional dependency per platform (@esbuild/linux-x64
 * and friends) and npm picks the matching one from its `os` and `cpu`
 * fields. None of them declares `libc`, and none of them is a musl variant,
 * because the binary is a statically linked Go executable: there is nothing
 * for it to link against, so glibc and musl are the same environment to it.
 *
 * The package does declare a postinstall script, which is what makes people
 * add --ignore-scripts to the list of suspects. It is a fallback, not the
 * mechanism. This sample is verified with lifecycle scripts disabled, so
 * the receipt is the proof.
 */

export async function transformTypeScript(source) {
  const esbuild = await import("esbuild");
  const { code } = await esbuild.transform(source, { loader: "ts" });
  return code;
}

export function platformPackage() {
  const esbuildPkg = require("esbuild/package.json");
  const names = Object.keys(esbuildPkg.optionalDependencies ?? {});
  return {
    optionalDependencies: names,
    declaresPostinstall: Boolean(esbuildPkg.scripts?.postinstall),
    installed: require("@esbuild/linux-x64/package.json"),
  };
}

/**
 * Reads the ELF program headers and returns the interpreter path, or null
 * when there is no PT_INTERP segment — which is what "statically linked"
 * means in the only way that can be checked rather than assumed.
 */
export function elfInterpreter(binaryPath) {
  const fd = openSync(binaryPath, "r");
  try {
    const header = Buffer.alloc(64);
    readSync(fd, header, 0, 64, 0);
    if (header.toString("latin1", 0, 4) !== "\x7fELF") {
      throw new Error("not an ELF file");
    }
    if (header[4] !== 2) throw new Error("expected a 64-bit ELF");

    const phoff = Number(header.readBigUInt64LE(0x20));
    const phentsize = header.readUInt16LE(0x36);
    const phnum = header.readUInt16LE(0x38);

    const entry = Buffer.alloc(phentsize);
    for (let i = 0; i < phnum; i++) {
      readSync(fd, entry, 0, phentsize, phoff + i * phentsize);
      const PT_INTERP = 3;
      if (entry.readUInt32LE(0) !== PT_INTERP) continue;
      const offset = Number(entry.readBigUInt64LE(0x08));
      const size = Number(entry.readBigUInt64LE(0x20));
      const interp = Buffer.alloc(size);
      readSync(fd, interp, 0, size, offset);
      return interp.toString("latin1").replace(/\0+$/, "");
    }
    return null;
  } finally {
    closeSync(fd);
  }
}
