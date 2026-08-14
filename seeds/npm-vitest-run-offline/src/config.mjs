/**
 * The code under test. Two shapes chosen because they are the ones that make
 * vitest's matchers disagree with each other in real suites.
 *
 * normalize() sets `timeout` unconditionally, so the returned object OWNS a
 * property whose value is undefined. Every normalizer written with defaults
 * and optional fields does this. It is the difference between toEqual and
 * toStrictEqual, and it is invisible when you read the code.
 *
 * loadConfig() is async, so its failure is a rejected promise and never a
 * thrown exception, no matter how the failure is written inside the function.
 */

export class ConfigError extends Error {
  constructor(message, field) {
    super(message);
    this.name = "ConfigError";
    this.field = field;
  }
}

export function normalize(input) {
  return {
    name: input.name,
    retries: input.retries ?? 3,
    timeout: input.timeout,
  };
}

export async function loadConfig(input) {
  if (!input?.name) throw new ConfigError("name is required", "name");
  return normalize(input);
}
