import { DateTime } from 'luxon';

// The trap: adding 24 hours and adding 1 day are different operations
// across a DST boundary. plus({ days: 1 }) keeps the wall-clock time and
// may add 23 or 25 real hours; plus({ hours: 24 }) keeps the duration and
// shifts the clock. Picking the wrong one is how "every day at 09:00"
// becomes 08:00 twice a year.
export function inZone(iso, zone) {
  return DateTime.fromISO(iso, { zone });
}

export function plusOneDay(iso, zone) {
  return inZone(iso, zone).plus({ days: 1 });
}

export function plus24Hours(iso, zone) {
  return inZone(iso, zone).plus({ hours: 24 });
}
