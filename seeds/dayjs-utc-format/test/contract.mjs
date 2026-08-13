import { strict as assert } from 'node:assert';

// A non-UTC host timezone is exactly the condition this sample is about.
process.env.TZ = 'Asia/Seoul';

const { formatUTC, addHoursUTC } = await import('../src/index.mjs');

assert.equal(formatUTC('2026-08-13T01:02:03Z'), '2026-08-13 01:02:03');
assert.equal(addHoursUTC('2026-08-13T23:00:00Z', 2), '2026-08-14T01:00:00.000Z');
console.log('CONTRACT PASS: dayjs utc plugin formats in UTC under TZ=Asia/Seoul');
