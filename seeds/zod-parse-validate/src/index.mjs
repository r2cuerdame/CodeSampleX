import { z } from 'zod';

// One schema, two ways to use it: parse throws, safeParse returns a result
// object. safeParse is what you want at a trust boundary.
export const User = z.object({
  email: z.string().email(),
  age: z.number().int().min(0),
});

export function parseUser(input) {
  return User.parse(input);
}

export function checkUser(input) {
  const result = User.safeParse(input);
  if (result.success) return { ok: true, value: result.data };
  return { ok: false, issues: result.error.issues };
}
