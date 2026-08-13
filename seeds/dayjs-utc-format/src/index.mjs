import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc.js';

// The utc plugin must be registered once before dayjs.utc() exists;
// forgetting it is the usual "dayjs.utc is not a function" error.
dayjs.extend(utc);

export function formatUTC(iso, pattern = 'YYYY-MM-DD HH:mm:ss') {
  return dayjs.utc(iso).format(pattern);
}

export function addHoursUTC(iso, hours) {
  return dayjs.utc(iso).add(hours, 'hour').toISOString();
}
