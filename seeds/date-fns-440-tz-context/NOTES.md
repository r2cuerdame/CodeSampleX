# date-fns v4 time-zone context and DST

`TZDate` and `TZDateMini` both perform getters, setters, and date-fns arithmetic
in their named zone. `TZDateMini` is smaller because it does not also format
`toString()` and `toISOString()` in that zone; those methods retain native
system-zone or UTC formatting.

The date-fns v4 `{ in: tz(zone) }` context controls both the calculation zone
and the result constructor, so a plain `Date` input can produce a `TZDate`.
Calendar-day arithmetic and elapsed-hour arithmetic intentionally differ across
DST transitions.
