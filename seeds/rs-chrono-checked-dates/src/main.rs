//! chrono 0.4 replaced its panicking constructors with `_opt` ones that
//! return Option, and deprecated the timestamp helpers that used to live
//! on NaiveDateTime. Copying an older answer gives you either a
//! deprecation warning or a panic on input you do not control.
//!
//! The part worth knowing beyond the renames: month arithmetic CLAMPS.
//! January 31 plus one month is February 28, not an error and not March 3.
//! Day arithmetic does not clamp, because a day is unambiguous. So
//! "add a month" and "add 30 days" are different questions with different
//! answers, and only one of them can be wrong silently.

use chrono::{DateTime, Days, Months, NaiveDate, NaiveDateTime, Utc};

fn main() {
    // The replacement for the deprecated NaiveDateTime::from_timestamp.
    let epoch = DateTime::from_timestamp(1_767_225_600, 0).expect("in range");
    assert_eq!(epoch.to_rfc3339(), "2026-01-01T00:00:00+00:00");
    // It returns Option rather than panicking, and the range really is
    // reachable: this is the whole reason for the change.
    assert!(DateTime::from_timestamp(i64::MAX, 0).is_none());

    // Invalid civil dates are None, never a rolled-over date.
    assert!(NaiveDate::from_ymd_opt(2026, 2, 30).is_none());
    assert!(NaiveDate::from_ymd_opt(2026, 2, 28).is_some());

    let naive: NaiveDateTime = NaiveDate::from_ymd_opt(2026, 1, 2)
        .and_then(|d| d.and_hms_opt(3, 4, 5))
        .expect("valid");
    // and_utc() is how a naive value becomes a DateTime<Utc> now.
    assert_eq!(naive.and_utc().to_rfc3339(), "2026-01-02T03:04:05+00:00");

    let parsed: DateTime<Utc> = "2026-01-02T03:04:05Z".parse().expect("rfc3339");
    assert_eq!(parsed.timestamp(), 1_767_323_045);
    assert_eq!(parsed.naive_utc(), naive);

    // Parsing refuses an impossible day rather than rolling it over.
    assert!(NaiveDate::parse_from_str("2026-02-30", "%Y-%m-%d").is_err());

    // Days are exact.
    let jan2 = NaiveDate::from_ymd_opt(2026, 1, 2).expect("valid");
    assert_eq!(
        jan2.checked_add_days(Days::new(30)),
        NaiveDate::from_ymd_opt(2026, 2, 1)
    );

    // Months clamp to the end of the target month. This is the line to
    // read twice before using it for a billing date.
    let jan31 = NaiveDate::from_ymd_opt(2026, 1, 31).expect("valid");
    assert_eq!(
        jan31.checked_add_months(Months::new(1)),
        NaiveDate::from_ymd_opt(2026, 2, 28)
    );
    // And it does not come back: February 28 plus a month is March 28, so
    // adding one month twice is not the same as adding two months.
    let twice = jan31
        .checked_add_months(Months::new(1))
        .and_then(|d| d.checked_add_months(Months::new(1)));
    assert_eq!(twice, NaiveDate::from_ymd_opt(2026, 3, 28));
    assert_eq!(
        jan31.checked_add_months(Months::new(2)),
        NaiveDate::from_ymd_opt(2026, 3, 31)
    );

    println!("contract ok");
}
