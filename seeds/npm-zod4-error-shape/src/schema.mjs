import { z } from "zod";

/**
 * What zod 4 hands you when validation fails.
 *
 * A failed validation comes back as a value rather than an exception:
 * safeParse returns `{ success: true, data }` or `{ success: false, error }`,
 * and the losing branch does not carry the other key at all, so there is no
 * partial `data` to inspect after a failure. The error is a ZodError, a real
 * Error subclass, and everything useful about the failure lives on
 * `error.issues`.
 *
 * The zod 3 spelling `error.errors` is gone in 4 — not deprecated, absent —
 * and so is `issue.received` on an ordinary type mismatch. Both are measured
 * in the contract, because code that reads them gets `undefined` rather than
 * a crash, which is how a migration ships a silently empty error page.
 */

export const LineItem = z.object({
  sku: z.string(),
  qty: z.number().int().positive(),
});

export const Order = z.object({
  id: z.string(),
  items: z.array(LineItem),
});

/**
 * The three ways a field can be "not required", which are three different
 * things and not interchangeable. optional() and default() react to
 * undefined; nullable() reacts to null. Neither covers the other.
 */
export const Profile = z.object({
  nickname: z.string().optional(),
  bio: z.string().nullable(),
  locale: z.string().default("en"),
});

/**
 * How a union says which member failed — and it does not always answer with
 * the same shape, which is the part that surprises people.
 *
 * Measured rule, arrived at by trying the combinations rather than by reading
 * the changelog: zod 4 counts the members whose failures were all continuable
 * — a length, a format, the constraint checks that let parsing carry on and
 * collect more. A wrong type aborts a member outright, and so does a wrong
 * literal; the contract measures the literal case, because that is the half of
 * the rule most likely to be guessed the other way. When exactly one member is
 * left standing, zod treats it as the branch you meant and returns that
 * member's issues directly, with paths already relative to the whole input and
 * no wrapper at all. When none is left, or when more than one is, there is no
 * branch to prefer and you get a single invalid_union issue carrying one group
 * of issues per member, in declaration order. Nothing labels a group with the
 * member it came from, so its index is the only handle you get.
 *
 * Code that reaches straight for `issues[0].errors` is therefore reading a
 * union error that may never arrive, and code that assumes `issues[0].path`
 * points at a field breaks on the ambiguous input.
 */
export const Contact = z.union([
  z.object({ email: z.email() }),
  z.object({ phone: z.string().min(7) }),
]);

/**
 * A shared literal key is the reliable way to leave exactly one member
 * standing: a tag it does not match aborts that member, so the member the tag
 * names is the only one still holding continuable failures. That is why a
 * tagged plain z.union already reports like a discriminated one — same issues,
 * and the same union type underneath.
 *
 * The two diverge on one input: a tag matching no member. The plain union has
 * nothing to prefer and dumps every member's issues; the declared version
 * names the discriminator and lists the tags it accepts. That case, not the
 * ordinary one, is what declaring it buys.
 */
export const Payment = z.union([
  z.object({ kind: z.literal("card"), last4: z.string().length(4) }),
  z.object({ kind: z.literal("iban"), iban: z.string().min(15) }),
]);

export const TaggedPayment = z.discriminatedUnion("kind", [
  z.object({ kind: z.literal("card"), last4: z.string().length(4) }),
  z.object({ kind: z.literal("iban"), iban: z.string().min(15) }),
]);

/**
 * Turning issues into something a form can render.
 *
 * `issue.path` is an array of the keys walked to reach the value, and array
 * indices arrive as numbers, not strings. Joining is fine; filtering the path
 * to strings is the mistake, because it collapses items[0] and items[1] onto
 * the same key.
 */
export function fieldErrors(error) {
  const byField = {};
  for (const issue of error.issues) {
    const key = issue.path.length === 0 ? "(root)" : issue.path.join(".");
    (byField[key] ??= []).push(issue.message);
  }
  return byField;
}
