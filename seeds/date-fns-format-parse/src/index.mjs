import { parseISO, format, differenceInCalendarDays, addDays } from 'date-fns';

// Two traps live here. parseISO reads a date-only string ("2026-08-13") as
// LOCAL midnight, while new Date() on the same string reads it as UTC —
// which is why a date can appear a day earlier west of Greenwich. And the
// format tokens are not the same as moment's: 'yyyy-MM-dd' is right, while
// 'YYYY-MM-DD' means week-year and day-of-year and throws in date-fns v4.
export function isoToPattern(iso, pattern) {
  return format(parseISO(iso), pattern);
}

export function localMidnight(dateOnly) {
  const d = parseISO(dateOnly);
  return { hours: d.getHours(), day: d.getDate() };
}

export function calendarDaysBetween(aIso, bIso) {
  return differenceInCalendarDays(parseISO(aIso), parseISO(bIso));
}

export function plusDays(iso, n) {
  return format(addDays(parseISO(iso), n), 'yyyy-MM-dd');
}
