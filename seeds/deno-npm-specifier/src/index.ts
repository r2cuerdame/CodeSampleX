import { nanoid } from "nanoid";
import { createHash } from "node:crypto";

// Deno reaches npm through an explicit `npm:` specifier — either in the
// import itself or mapped in deno.json, as here. A bare "nanoid" import
// with no mapping fails to resolve; that is the error most Node code hits
// first, and it is a configuration difference rather than an
// incompatibility in the package.
//
// node: builtins are supported, so packages depending on them keep working.
// Permissions are the real difference: Deno denies file, network and
// environment access unless granted, so a package that reads the filesystem
// at import time fails here and nowhere else.
export function id(): string {
  return nanoid();
}

export function sha256(text: string): string {
  return createHash("sha256").update(text).digest("hex");
}
