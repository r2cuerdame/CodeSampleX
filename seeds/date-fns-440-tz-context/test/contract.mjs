import assert from 'node:assert/strict';
import { TZDate, TZDateMini, tz } from '@date-fns/tz';
import { addDays, addHours, differenceInHours, startOfDay } from 'date-fns';

process.env.TZ = 'UTC';

const zone = 'America/Los_Angeles';
const springMidnight = new TZDate(2022, 2, 13, 0, zone);
const twoHoursLater = addHours(springMidnight, 2);

assert.ok(twoHoursLater instanceof TZDate);
assert.equal(twoHoursLater.timeZone, zone);
assert.equal(twoHoursLater.getHours(), 3);
assert.match(springMidnight.toString(), /00:00:00 GMT-0800/);
assert.match(twoHoursLater.toString(), /03:00:00 GMT-0700/);
assert.equal(twoHoursLater.getTime() - springMidnight.getTime(), 2 * 60 * 60 * 1000);

const nextCalendarDay = addDays(springMidnight, 1);
assert.ok(nextCalendarDay instanceof TZDate);
assert.equal(nextCalendarDay.getHours(), 0);
assert.equal(differenceInHours(nextCalendarDay, springMidnight), 23);

const plainInstant = new Date('2022-03-13T20:00:00.000Z');
const zonedStart = startOfDay(plainInstant, { in: tz(zone) });
assert.ok(zonedStart instanceof TZDate);
assert.equal(zonedStart.timeZone, zone);
assert.equal(zonedStart.toISOString(), '2022-03-13T00:00:00.000-08:00');

const contextResult = addHours(
  new Date('2022-03-13T08:00:00.000Z'),
  2,
  { in: tz(zone) }
);
assert.ok(contextResult instanceof TZDate);
assert.equal(contextResult.timeZone, zone);
assert.equal(contextResult.getHours(), 3);
assert.equal(contextResult.toISOString(), '2022-03-13T03:00:00.000-07:00');

const miniMidnight = new TZDateMini(2022, 2, 13, 0, zone);
const miniLater = addHours(miniMidnight, 2);
assert.ok(miniLater instanceof TZDateMini);
assert.equal(miniLater.timeZone, zone);
assert.equal(miniLater.getHours(), 3);
assert.equal(miniLater.getTime(), twoHoursLater.getTime());
assert.equal(miniLater.toISOString(), '2022-03-13T10:00:00.000Z');
assert.match(miniLater.toString(), /10:00:00 GMT\+0000/);
assert.match(twoHoursLater.toString(), /03:00:00 GMT-0700/);

console.log('CONTRACT PASS: date-fns 4.4.0 TZ context and DST behavior are measured');
