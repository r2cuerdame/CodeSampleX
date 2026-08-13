import { strict as assert } from 'node:assert';
import { DateTime } from 'luxon';
import { inZone, plusOneDay, plus24Hours } from '../src/index.mjs';

// One instant, two local days.
const instant = '2026-01-01T02:30:00Z';
assert.equal(inZone(instant, 'Asia/Seoul').toFormat('yyyy-MM-dd HH:mm'), '2026-01-01 11:30');
assert.equal(inZone(instant, 'America/New_York').toFormat('yyyy-MM-dd HH:mm'), '2025-12-31 21:30');

// Across the US spring-forward, a "day" is 23 hours.
const before = '2026-03-07T09:00:00';
const day = plusOneDay(before, 'America/New_York');
const hours = plus24Hours(before, 'America/New_York');
assert.equal(day.toFormat('HH:mm'), '09:00', 'plus days keeps wall-clock time');
assert.equal(hours.toFormat('HH:mm'), '10:00', 'plus hours keeps duration');
assert.equal(day.diff(DateTime.fromISO(before, { zone: 'America/New_York' }), 'hours').hours, 23);

// A bad zone is invalid, not silently UTC.
assert.equal(DateTime.fromISO('2026-01-01T00:00:00', { zone: 'Not/AZone' }).isValid, false);

console.log('CONTRACT PASS: luxon kept zones and DST arithmetic honest');
