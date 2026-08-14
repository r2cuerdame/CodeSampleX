import { id, sha256 } from "../src/index.ts";

function assert(cond: unknown, message: string) {
  if (!cond) throw new Error(message);
}

Deno.test("an npm package works through an npm: specifier", () => {
  const value = id();
  assert(value.length === 21, `length ${value.length}`);
  assert(/^[A-Za-z0-9_-]{21}$/.test(value), "not url-safe");
});

Deno.test("node: builtins produce the same result node would", () => {
  assert(
    sha256("") ===
      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "digest mismatch",
  );
});

Deno.test("the Deno global identifies the runtime", () => {
  assert(typeof Deno === "object", "no Deno global");
  assert(typeof Deno.version.deno === "string", "no version");
});

Deno.test("permissions are denied by default, not granted", async () => {
  const status = await Deno.permissions.query({ name: "net" });
  assert(status.state !== "granted", `net was ${status.state}, expected not granted`);
});
