<?php

namespace Csx;

use Carbon\CarbonImmutable;
use Carbon\CarbonInterface;

/**
 * Carbon 3, and the fact that decides whether your date code is correct:
 * Carbon extends DateTime and is MUTABLE, CarbonImmutable extends
 * DateTimeImmutable and is not. Every method below is written against
 * CarbonInterface, so the same call sites accept both classes and behave
 * oppositely — which is the whole problem. `$date->addDay()` on a Carbon
 * rewrites the object in place and hands you back the same instance, so every
 * other variable, array entry and object property holding that date moves a day
 * forward too. On a CarbonImmutable it returns a new instance and leaves the
 * receiver alone.
 *
 * Nothing warns you. `==` compares DateTime objects by instant, so a mutable
 * and an immutable holding the same moment are equal; the class check and the
 * after-effects are what tell the two apart, and `===` is not one of them,
 * because it is false for two clones of the same class as well. The failure
 * shows up far from the call that caused it, usually as "why are all my slots
 * the same day".
 *
 * dailySlots() and dailySlotsSafe() are the same loop written twice. The first
 * is the one people write; it is correct on CarbonImmutable and wrong on
 * Carbon, and both are proven in the contract. avoidMutation() is what makes it
 * safe for both: a clone on Carbon, `$this` on CarbonImmutable, so it costs
 * nothing to write it defensively. copy() clones unconditionally, even on an
 * immutable where there is nothing to protect against.
 *
 * The timezone pair is the other coin-flip. setTimezone() keeps the instant and
 * changes the wall clock you read off it; shiftTimezone() keeps the wall clock
 * and therefore changes the instant. Reach for the wrong one and the appointment
 * moves by the offset — silently, because both return a date in the zone you
 * asked for and printing it in that zone looks right either way. On a Carbon
 * both of them also mutate the argument you were handed, so a "convert this to
 * the user's zone" helper corrupts the caller's value.
 *
 * Day arithmetic is calendar arithmetic, not elapsed time. addDay() on
 * 2024-03-31 in Europe/Paris moves 23 real hours because that day loses an hour
 * to DST; addHours(24) moves a real day and lands at 01:00. diffInDays()
 * follows the same rule and reports 2.0 across that boundary even though 47
 * hours passed — its third argument, $utc, is what asks for elapsed time
 * instead, and Carbon switches to it on its own whenever the two dates carry
 * different timezone names, so the calendar answer only appears when both sides
 * sit in the same zone. Hours are always elapsed: diffInHours is defined as
 * seconds and has no $utc parameter, and a third argument passed to it is
 * dropped in silence, because PHP does not complain about extra arguments to a
 * userland method. Carbon 3 returns a signed float where Carbon 2 returned an
 * absolute int, so upgraded code that compares a diff to a positive number
 * quietly inverts when the arguments are the other way round.
 */
final class Schedule
{
    /**
     * A fixed zone with two DST transitions a year, so every assertion in the
     * contract is stable regardless of where the test runs. The container's
     * default zone is UTC, which is exactly the setup that hides zone bugs.
     */
    public const ZONE = 'Europe/Paris';

    /**
     * The loop nearly everybody writes. Correct on CarbonImmutable; on Carbon
     * every entry is the same object and the array ends up holding $count
     * references to one date that has been advanced $count times.
     *
     * @return list<CarbonInterface>
     */
    public static function dailySlots(CarbonInterface $start, int $count): array
    {
        $slots = [];
        $cursor = $start;

        for ($i = 0; $i < $count; $i++) {
            $slots[] = $cursor;
            $cursor = $cursor->addDay();
        }

        return $slots;
    }

    /**
     * The same loop, correct for both classes. avoidMutation() clones a Carbon
     * and returns $this for a CarbonImmutable, so the defensive copy is free
     * where it is not needed. The first one also protects the caller's $start,
     * which the naive version advances as a side effect.
     *
     * @return list<CarbonInterface>
     */
    public static function dailySlotsSafe(CarbonInterface $start, int $count): array
    {
        $slots = [];
        $cursor = $start->avoidMutation();

        for ($i = 0; $i < $count; $i++) {
            $slots[] = $cursor->avoidMutation();
            $cursor = $cursor->addDay();
        }

        return $slots;
    }

    /**
     * Read the same instant in another zone. The timestamp does not move; the
     * wall clock does. This is what "show it in the customer's timezone" means.
     */
    public static function inZone(CarbonInterface $at, string $tz): CarbonInterface
    {
        return $at->setTimezone($tz);
    }

    /**
     * Keep the wall clock and declare it to have been in another zone. The
     * instant moves by the offset difference. This is what you want when a
     * source system sent you a local time stamped with the wrong zone, and a
     * bug in every other situation.
     */
    public static function reinterpretIn(CarbonInterface $at, string $tz): CarbonInterface
    {
        return $at->shiftTimezone($tz);
    }

    /**
     * The day boundaries in the instance's own zone. Local midnight in a zone
     * with an offset is never midnight UTC, so converting first and then taking
     * the boundary lands on a different instant, and on a DST transition day
     * this window is 23 or 25 hours wide rather than 24.
     *
     * @return array{0: CarbonInterface, 1: CarbonInterface}
     */
    public static function dayWindow(CarbonInterface $at): array
    {
        return [$at->avoidMutation()->startOfDay(), $at->avoidMutation()->endOfDay()];
    }

    /**
     * Signed, fractional days. $elapsed is diffInDays' own $utc argument: it
     * switches from calendar days in the instance's zone to real elapsed time,
     * which is the difference a DST boundary makes visible. Carbon turns it on
     * by itself when $from and $to name different zones.
     */
    public static function spanInDays(CarbonInterface $from, CarbonInterface $to, bool $elapsed = false): float
    {
        return $from->diffInDays($to, false, $elapsed);
    }

    /**
     * The kind of call Carbon::setTestNow() exists to freeze. It reads the
     * clock through Carbon, so a test now set by any test — including one that
     * forgot to clear it — decides what this returns.
     */
    public static function receiptStamp(): CarbonImmutable
    {
        return CarbonImmutable::now();
    }
}
