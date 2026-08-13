import { strict as assert } from 'node:assert';
import { isoToPattern, localMidnight, calendarDaysBetween, plusDays } from '../src/index.mjs';

assert.equal(isoToPattern('2026-08-13T09:05:00', 'yyyy-MM-dd'), '2026-08-13');
assert.equal(isoToPattern('2026-08-13T09:05:00', "yyyy-MM-dd'T'HH:mm"), '2026-08-13T09:05');

// Date-only strings become LOCAL midnight, so the day never shifts.
const m = localMidnight('2026-08-13');
assert.equal(m.hours, 0);
assert.equal(m.day, 13);

// Calendar days ignore the time of day: 23:59 -> 00:01 is still one day.
assert.equal(calendarDaysBetween('2026-08-14T00:01:00', '2026-08-13T23:59:00'), 1);
assert.equal(calendarDaysBetween('2026-08-13T00:00:00', '2026-08-13T23:59:00'), 0);

assert.equal(plusDays('2026-08-30', 3), '2026-09-02');
console.log('CONTRACT PASS: date-fns parsed ISO input and formatted without a day shift');
