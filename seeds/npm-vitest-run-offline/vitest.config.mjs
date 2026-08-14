import { defineConfig } from "vitest/config";

/**
 * `include` pins the passing run to one file. test/ also holds the runner
 * script and test/not-awaited-rejects.mjs, which is supposed to fail; the
 * runner starts that one as a separate run with its own include, and neither
 * name matches this glob.
 *
 * The pool is pinned to one worker because the sandbox container gets 512MB
 * and a 256 process limit. Left at the default, vitest sizes the pool from
 * the reported CPU count, which is the host's and not the container's.
 *
 * These are top-level test options on purpose. Vitest 4 removed
 * `poolOptions`, and the v3 spelling — poolOptions: { forks: { singleFork } } —
 * does not error. It prints one deprecation line and then runs with the
 * defaults it was meant to replace, so a copied v3 config looks like it is
 * being honoured.
 */
export default defineConfig({
  test: {
    include: ["test/*.test.mjs"],
    pool: "forks",
    maxWorkers: 1,
    fileParallelism: false,
  },
});
