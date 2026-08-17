# Luxon 3.6.1 locale weeks and minimalDays

Luxon exposes two week systems on the same `DateTime`. `weekNumber` and
`weekYear` are ISO values, while `localWeekNumber` and `localWeekYear` use the
locale's first weekday and `minimalDays` rule.

The difference is visible on 2021-01-01. Under `en-US` it belongs to local week
1 of 2021; under `en-GB` it belongs to local week 53 of 2020. Its ISO week is
53 of 2020 in both locales. Likewise, `startOf("week")` stays ISO/Monday unless
`useLocaleWeeks: true` is passed.

`Settings.defaultWeekSettings` can override `firstDay`, `minimalDays`, and the
weekend globally. The override wins even when a locale is explicitly supplied,
but existing `DateTime` instances keep the week settings they captured when
created. Changing only `minimalDays` changes the week-year assignment without
changing the week's start date.
