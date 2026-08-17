import assert from "node:assert/strict";

import { DateTime, Info, Settings } from "luxon";


function atYearBoundary(locale) {
  return DateTime.fromISO("2021-01-01T12:00:00Z", {
    zone: "UTC",
    locale,
  });
}


assert.equal(Info.getStartOfWeek({ locale: "en-US" }), 7);
assert.equal(Info.getMinimumDaysInFirstWeek({ locale: "en-US" }), 1);
assert.equal(Info.getStartOfWeek({ locale: "en-GB" }), 1);
assert.equal(Info.getMinimumDaysInFirstWeek({ locale: "en-GB" }), 4);

const us = atYearBoundary("en-US");
assert.equal(us.localWeekNumber, 1);
assert.equal(us.localWeekYear, 2021);
assert.equal(us.weekNumber, 53);
assert.equal(us.weekYear, 2020);

const gb = atYearBoundary("en-GB");
assert.equal(gb.localWeekNumber, 53);
assert.equal(gb.localWeekYear, 2020);
assert.equal(gb.weekNumber, 53);
assert.equal(gb.weekYear, 2020);

// startOf("week") remains ISO/Monday unless useLocaleWeeks is explicitly set.
assert.equal(us.startOf("week").toISODate(), "2020-12-28");
assert.equal(
  us.startOf("week", { useLocaleWeeks: true }).toISODate(),
  "2020-12-27",
);
assert.equal(
  gb.startOf("week", { useLocaleWeeks: true }).toISODate(),
  "2020-12-28",
);

try {
  Settings.defaultWeekSettings = {
    firstDay: 7,
    minimalDays: 1,
    weekend: [6, 7],
  };
  const oneDayRule = atYearBoundary("en-GB");
  assert.equal(Info.getStartOfWeek({ locale: "en-GB" }), 7);
  assert.equal(Info.getMinimumDaysInFirstWeek({ locale: "en-GB" }), 1);
  assert.equal(oneDayRule.localWeekNumber, 1);
  assert.equal(oneDayRule.localWeekYear, 2021);
  assert.equal(
    oneDayRule.startOf("week", { useLocaleWeeks: true }).toISODate(),
    "2020-12-27",
  );

  Settings.defaultWeekSettings = {
    firstDay: 7,
    minimalDays: 4,
    weekend: [6, 7],
  };
  const fourDayRule = atYearBoundary("en-US");

  // Changing the global affects new Locale instances, even with an explicit
  // locale, but does not mutate a DateTime that already captured its settings.
  assert.equal(Info.getMinimumDaysInFirstWeek({ locale: "en-US" }), 4);
  assert.equal(Info.getMinimumDaysInFirstWeek({ locale: "en-GB" }), 4);
  assert.equal(fourDayRule.localWeekNumber, 53);
  assert.equal(fourDayRule.localWeekYear, 2020);
  assert.equal(oneDayRule.localWeekNumber, 1);
  assert.equal(oneDayRule.localWeekYear, 2021);

  // minimalDays changes week-year assignment, not the first day of the week.
  assert.equal(
    fourDayRule.startOf("week", { useLocaleWeeks: true }).toISODate(),
    "2020-12-27",
  );

  assert.throws(
    () => {
      Settings.defaultWeekSettings = {
        firstDay: 7,
        minimalDays: 0,
        weekend: [6, 7],
      };
    },
    /Invalid week settings/,
  );
} finally {
  Settings.defaultWeekSettings = null;
}

assert.equal(Info.getMinimumDaysInFirstWeek({ locale: "en-US" }), 1);
assert.equal(Info.getMinimumDaysInFirstWeek({ locale: "en-GB" }), 4);

console.log("Luxon locale-week contract passed");
