<?php

use Carbon\Carbon;
use Carbon\CarbonImmutable;
use Carbon\CarbonInterface;
use Carbon\Exceptions\InvalidFormatException;
use Csx\Schedule;
use PHPUnit\Framework\TestCase;

final class ScheduleTest extends TestCase
{
    private const ZONE = Schedule::ZONE;

    private string $ambientZone;

    protected function setUp(): void
    {
        $this->ambientZone = date_default_timezone_get();
    }

    protected function tearDown(): void
    {
        // The cleanup the last test is about. Every test file that touches
        // setTestNow needs this, because the frozen clock is global and
        // survives into whatever runs next.
        Carbon::setTestNow();
        date_default_timezone_set($this->ambientZone);
    }

    public function testAddDayRewritesEveryReferenceToACarbonAndNoneToACarbonImmutable(): void
    {
        // Same code, same values, one word different in the class name.
        $mutable = Carbon::parse('2024-01-01 08:00', self::ZONE);
        $alsoMutable = $mutable;
        $returnedMutable = $mutable->addDay();

        // The receiver moved. So did the other name for it, because there is
        // only ever one object: addDay() returns $this, it does not build a
        // new date. Storing a Carbon on an entity and handing it out is enough
        // to let any caller rewrite it.
        $this->assertSame('2024-01-02', $mutable->format('Y-m-d'));
        $this->assertSame('2024-01-02', $alsoMutable->format('Y-m-d'));
        $this->assertSame($mutable, $alsoMutable);
        $this->assertSame($mutable, $returnedMutable);

        $immutable = CarbonImmutable::parse('2024-01-01 08:00', self::ZONE);
        $alsoImmutable = $immutable;
        $returnedImmutable = $immutable->addDay();

        // The receiver did not move, and neither did the second name for it.
        // Only the returned value carries the new date.
        $this->assertSame('2024-01-01', $immutable->format('Y-m-d'));
        $this->assertSame('2024-01-01', $alsoImmutable->format('Y-m-d'));
        $this->assertSame('2024-01-02', $returnedImmutable->format('Y-m-d'));
        $this->assertNotSame($immutable, $returnedImmutable);

        // Which is why the statement that discards the result is a write on
        // one class and a no-op on the other. Neither line warns.
        $write = Carbon::parse('2024-01-01 08:00', self::ZONE);
        $write->addDay();
        $this->assertSame('2024-01-02', $write->format('Y-m-d'));

        $noop = CarbonImmutable::parse('2024-01-01 08:00', self::ZONE);
        $noop->addDay();
        $this->assertSame('2024-01-01', $noop->format('Y-m-d'));
    }

    public function testTheTwoClassesAreNotSubstitutableButCompareEqualAnyway(): void
    {
        $mutable = Carbon::parse('2024-06-15 12:00', 'UTC');
        $immutable = CarbonImmutable::parse('2024-06-15 12:00', 'UTC');

        // Carbon extends DateTime, CarbonImmutable extends DateTimeImmutable,
        // and neither extends the other. A parameter typed DateTime rejects
        // half of Carbon; only DateTimeInterface or CarbonInterface takes both.
        $this->assertInstanceOf(DateTime::class, $mutable);
        $this->assertNotInstanceOf(DateTimeImmutable::class, $mutable);
        $this->assertInstanceOf(DateTimeImmutable::class, $immutable);
        $this->assertNotInstanceOf(DateTime::class, $immutable);
        $this->assertInstanceOf(CarbonInterface::class, $mutable);
        $this->assertInstanceOf(CarbonInterface::class, $immutable);

        // And comparison will not tell you which one you were handed: PHP
        // compares date objects by the instant they hold, across classes and
        // across zones. The class check above is the only test on the value
        // itself that catches the mixup; identity answers a different question,
        // since === is false for two clones of one class too.
        $this->assertEquals($mutable, $immutable);
        $this->assertTrue($mutable == $immutable);
        $this->assertTrue($mutable->equalTo($immutable));
        $this->assertTrue($immutable->equalTo(CarbonImmutable::parse('2024-06-15 14:00', self::ZONE)));

        // copy() clones unconditionally, even where there is nothing to
        // protect. avoidMutation() is the one that stays free on an immutable,
        // which is what makes it safe to write in shared code.
        $this->assertNotSame($mutable, $mutable->copy());
        $this->assertNotSame($mutable, $mutable->avoidMutation());
        $this->assertNotSame($immutable, $immutable->copy());
        $this->assertSame($immutable, $immutable->avoidMutation());
    }

    public function testTheSameSlotLoopIsRightOnImmutableAndCollapsesOnMutable(): void
    {
        $immutableStart = CarbonImmutable::parse('2024-01-01 08:00', self::ZONE);
        $good = Schedule::dailySlots($immutableStart, 3);

        $this->assertSame(
            ['2024-01-01', '2024-01-02', '2024-01-03'],
            array_map(fn (CarbonInterface $d) => $d->format('Y-m-d'), $good)
        );
        $this->assertCount(3, array_unique(array_map('spl_object_id', $good)));
        $this->assertSame('2024-01-01', $immutableStart->format('Y-m-d'));

        // The identical function, handed a Carbon. Three entries, one object,
        // and the value is start plus three days because the cursor kept
        // advancing the very date already in the array. This is the bug people
        // report as "all my slots are the same day".
        $mutableStart = Carbon::parse('2024-01-01 08:00', self::ZONE);
        $broken = Schedule::dailySlots($mutableStart, 3);

        $this->assertSame(
            ['2024-01-04', '2024-01-04', '2024-01-04'],
            array_map(fn (CarbonInterface $d) => $d->format('Y-m-d'), $broken)
        );
        $this->assertCount(1, array_unique(array_map('spl_object_id', $broken)));
        // The caller's own start date was advanced too, by a function that
        // looks like it only reads its argument.
        $this->assertSame('2024-01-04', $mutableStart->format('Y-m-d'));
        $this->assertSame($mutableStart, $broken[0]);

        // avoidMutation() at both ends makes the one implementation correct for
        // either class, and leaves the caller's date alone.
        $freshMutable = Carbon::parse('2024-01-01 08:00', self::ZONE);
        $freshImmutable = CarbonImmutable::parse('2024-01-01 08:00', self::ZONE);
        $expected = ['2024-01-01', '2024-01-02', '2024-01-03'];

        $this->assertSame($expected, array_map(
            fn (CarbonInterface $d) => $d->format('Y-m-d'),
            Schedule::dailySlotsSafe($freshMutable, 3)
        ));
        $this->assertSame($expected, array_map(
            fn (CarbonInterface $d) => $d->format('Y-m-d'),
            Schedule::dailySlotsSafe($freshImmutable, 3)
        ));
        $this->assertSame('2024-01-01', $freshMutable->format('Y-m-d'));
    }

    public function testSetTimezoneKeepsTheInstantWhileShiftTimezoneMovesIt(): void
    {
        $utc = CarbonImmutable::parse('2024-06-15 12:00:00', 'UTC');
        $this->assertSame(1718452800, $utc->getTimestamp());

        $converted = Schedule::inZone($utc, self::ZONE);
        $reinterpreted = Schedule::reinterpretIn($utc, self::ZONE);

        // Both answers claim to be in Paris, and printing either one in Paris
        // looks plausible. The wall clocks are what differ on screen.
        $this->assertSame(self::ZONE, $converted->getTimezone()->getName());
        $this->assertSame(self::ZONE, $reinterpreted->getTimezone()->getName());
        $this->assertSame('2024-06-15 14:00', $converted->format('Y-m-d H:i'));
        $this->assertSame('2024-06-15 12:00', $reinterpreted->format('Y-m-d H:i'));

        // The timestamps are what prove which one you used. setTimezone is a
        // change of view: same instant, different clock face. shiftTimezone
        // keeps the clock face and therefore moves the instant back by the
        // +02:00 offset it now claims to have been read in.
        $this->assertSame($utc->getTimestamp(), $converted->getTimestamp());
        $this->assertNotSame($utc->getTimestamp(), $reinterpreted->getTimestamp());
        $this->assertSame(-7200, $reinterpreted->getTimestamp() - $utc->getTimestamp());
        $this->assertSame('12:00', $converted->utc()->format('H:i'));
        $this->assertSame('10:00', $reinterpreted->utc()->format('H:i'));

        // Both of them mutate a Carbon in place, so the "convert this for
        // display" helper rewrites the caller's date. The returned value and
        // the argument are the same object.
        $mutable = Carbon::parse('2024-06-15 12:00:00', 'UTC');
        $keptByCaller = $mutable;
        $returned = Schedule::inZone($mutable, self::ZONE);

        $this->assertSame($mutable, $returned);
        $this->assertSame('2024-06-15 14:00', $keptByCaller->format('Y-m-d H:i'));
        $this->assertSame(self::ZONE, $keptByCaller->getTimezone()->getName());

        // shiftTimezone does the same to its argument, and this one is the
        // dangerous half: the caller's wall clock is untouched, so nothing on
        // screen changes while the instant it holds has moved two hours.
        $shifted = Carbon::parse('2024-06-15 12:00:00', 'UTC');
        $shiftReturned = Schedule::reinterpretIn($shifted, self::ZONE);

        $this->assertSame($shifted, $shiftReturned);
        $this->assertSame('2024-06-15 12:00', $shifted->format('Y-m-d H:i'));
        $this->assertSame(self::ZONE, $shifted->getTimezone()->getName());
        $this->assertSame(1718452800 - 7200, $shifted->getTimestamp());
    }

    public function testDiffInDaysCountsCalendarDaysUnlessYouAskForElapsedTime(): void
    {
        // Europe/Paris springs forward at 02:00 on 2024-03-31, so this span is
        // two calendar days and 47 real hours.
        $from = CarbonImmutable::parse('2024-03-30 00:00', self::ZONE);
        $to = CarbonImmutable::parse('2024-04-01 00:00', self::ZONE);
        $this->assertSame(47 * 3600, $to->getTimestamp() - $from->getTimestamp());

        // Measured, and the opposite of the "diffInDays is just seconds / 86400"
        // assumption: the default answer is the calendar one, computed in the
        // instance's own zone, so the missing hour does not show up. The third
        // argument switches to elapsed time and does show it.
        $this->assertSame(2.0, Schedule::spanInDays($from, $to));
        $this->assertSame(47 / 24, Schedule::spanInDays($from, $to, elapsed: true));

        // The calendar answer is conditional on both dates naming the same
        // zone. Carbon compares in UTC by itself when the names differ, so the
        // very same instants, read through a UTC view on either side, return
        // the elapsed answer that nobody asked for. A date arriving from a
        // database column in UTC is enough to flip it.
        $this->assertSame(47 / 24, Schedule::spanInDays($from, $to->utc()));
        $this->assertSame(47 / 24, Schedule::spanInDays($from->utc(), $to));

        // The autumn boundary is the mirror image: 49 real hours, still two
        // calendar days, and elapsed time now overshoots instead.
        $fallFrom = CarbonImmutable::parse('2024-10-26 00:00', self::ZONE);
        $fallTo = CarbonImmutable::parse('2024-10-28 00:00', self::ZONE);
        $this->assertSame(49 * 3600, $fallTo->getTimestamp() - $fallFrom->getTimestamp());
        $this->assertSame(2.0, Schedule::spanInDays($fallFrom, $fallTo));
        $this->assertSame(49 / 24, Schedule::spanInDays($fallFrom, $fallTo, elapsed: true));

        // Hours are not treated the same way as days: diffInHours divides
        // seconds and reports real elapsed time, so the two units disagree
        // about the same span. It never grew the $utc parameter diffInDays has,
        // and the third argument below therefore does nothing whatsoever — PHP
        // accepts extra arguments to a userland method and discards them, so
        // the call that reads like "elapsed hours, please" is neither an error
        // nor a change in behaviour.
        $this->assertSame(47.0, $from->diffInHours($to));
        $this->assertSame(49.0, $fallFrom->diffInHours($fallTo));
        $this->assertSame(47.0, $from->diffInHours($to, false, true));
        $this->assertSame(
            ['date', 'absolute'],
            array_map(
                fn (ReflectionParameter $p) => $p->getName(),
                (new ReflectionMethod(CarbonImmutable::class, 'diffInHours'))->getParameters()
            )
        );
        $this->assertSame(
            ['date', 'absolute', 'utc'],
            array_map(
                fn (ReflectionParameter $p) => $p->getName(),
                (new ReflectionMethod(CarbonImmutable::class, 'diffInDays'))->getParameters()
            )
        );

        // Carbon 3 returns a signed float where Carbon 2 returned an absolute
        // int. Upgraded code that compares a diff against a positive threshold
        // silently inverts when the arguments arrive the other way round; the
        // second argument is what restores the old behaviour.
        $this->assertSame(-2.0, Schedule::spanInDays($to, $from));
        $this->assertSame(2.0, $to->diffInDays($from, true));
        $this->assertSame(-47.0, $to->diffInHours($from));
        $this->assertIsFloat($from->diffInDays($to));
        $this->assertIsFloat($from->diffInHours($to));
    }

    public function testAddDayMovesTwentyThreeRealHoursOnTheShortDay(): void
    {
        $midnight = CarbonImmutable::parse('2024-03-31 00:00', self::ZONE);

        // Day arithmetic is calendar arithmetic: the clock reads the same on
        // the next day, so only 23 real hours pass. Asking for 24 hours asks
        // for real time and lands an hour further on. Both are correct; which
        // one you want depends on whether "tomorrow at this time" or "24 hours
        // of runtime" is the promise you made.
        $this->assertSame('2024-04-01 00:00', $midnight->addDay()->format('Y-m-d H:i'));
        $this->assertSame(23 * 3600, $midnight->addDay()->getTimestamp() - $midnight->getTimestamp());
        $this->assertSame('2024-04-01 01:00', $midnight->addHours(24)->format('Y-m-d H:i'));
        $this->assertSame(24 * 3600, $midnight->addHours(24)->getTimestamp() - $midnight->getTimestamp());

        // The same day measured end to end, which is where a "24 hours in a
        // day" assumption in a billing or availability calculation breaks.
        [$start, $end] = Schedule::dayWindow(CarbonImmutable::parse('2024-03-31 12:00', self::ZONE));
        $this->assertSame('+01:00', $start->format('P'));
        $this->assertSame('+02:00', $end->format('P'));
        $this->assertSame('2024-03-31 23:59:59.999999', $end->format('Y-m-d H:i:s.u'));
        // endOfDay stops a microsecond short of midnight, so the whole-second
        // difference is one less than the 82800 seconds the day actually holds.
        $this->assertSame(23 * 3600 - 1, $end->getTimestamp() - $start->getTimestamp());

        // The autumn transition day is the other half of it: the identical call
        // spans 25 hours, again one second short of it in whole seconds.
        // Bounding a day as start plus 86400 seconds is wrong twice a year, in
        // both directions.
        [$fallStart, $fallEnd] = Schedule::dayWindow(CarbonImmutable::parse('2024-10-27 12:00', self::ZONE));
        $this->assertSame('+02:00', $fallStart->format('P'));
        $this->assertSame('+01:00', $fallEnd->format('P'));
        $this->assertSame(25 * 3600 - 1, $fallEnd->getTimestamp() - $fallStart->getTimestamp());
    }

    public function testParseDisambiguatesBySeparatorAndPicksStandardTimeInTheRepeatedHour(): void
    {
        // PHP's date parser, which Carbon::parse defers to, decides what a
        // numeric date means from its separator: slashes are American m/d/y,
        // dashes and dots are European d-m-y. The same three numbers become two
        // different dates, with no warning and no failure to catch.
        $this->assertSame('2024-01-02', CarbonImmutable::parse('01/02/2024', self::ZONE)->format('Y-m-d'));
        $this->assertSame('2024-02-01', CarbonImmutable::parse('01-02-2024', self::ZONE)->format('Y-m-d'));
        $this->assertSame('2024-02-01', CarbonImmutable::parse('01.02.2024', self::ZONE)->format('Y-m-d'));
        // A four-digit leading component is unambiguous, which is the reason to
        // send ISO 8601 and never a locale-shaped string.
        $this->assertSame('2024-01-02', CarbonImmutable::parse('2024-01-02', self::ZONE)->format('Y-m-d'));

        // The failure only arrives when the American reading is impossible, so
        // European input passes silently for twelve days of every month and
        // then throws on the thirteenth.
        try {
            CarbonImmutable::parse('13/02/2024', self::ZONE);
            $this->fail('13 is not a month, so the slash form should not parse');
        } catch (InvalidFormatException $exception) {
            $this->assertInstanceOf(InvalidArgumentException::class, $exception);
            $this->assertStringContainsString("Could not parse '13/02/2024'", $exception->getMessage());
        }

        // Paris falls back at 03:00 on 2024-10-27, so 02:30 happens twice that
        // morning. Measured, against the usual claim that PHP keeps the first
        // occurrence: parsing the bare string picks the second one, the
        // standard-time reading at +01:00.
        $repeated = CarbonImmutable::parse('2024-10-27 02:30', self::ZONE);
        $this->assertSame('+01:00', $repeated->format('P'));
        $this->assertFalse($repeated->isDST());
        $this->assertSame(1729992600, $repeated->getTimestamp());

        // Arriving at the same wall clock by arithmetic gives the other
        // instant. Two dates that print identically, an hour apart, and only
        // the timestamp tells them apart.
        $reachedByAdding = CarbonImmutable::parse('2024-10-27 01:30', self::ZONE)->addHour();
        $this->assertSame('2024-10-27 02:30', $reachedByAdding->format('Y-m-d H:i'));
        $this->assertSame('+02:00', $reachedByAdding->format('P'));
        $this->assertSame(3600, $repeated->getTimestamp() - $reachedByAdding->getTimestamp());

        // The spring gap is the other half: 02:30 on 2024-03-31 never happens
        // in Paris, and parse rolls it forward rather than rejecting it.
        $missing = CarbonImmutable::parse('2024-03-31 02:30', self::ZONE);
        $this->assertSame('2024-03-31 03:30', $missing->format('Y-m-d H:i'));
        $this->assertSame('+02:00', $missing->format('P'));
    }

    public function testStartOfDayUsesTheInstanceZoneSoConvertingFirstChangesTheAnswer(): void
    {
        $evening = CarbonImmutable::parse('2024-03-15 23:30', self::ZONE);

        // startOfDay works on the zone the instance carries, so it is midnight
        // in Paris — which is still the previous calendar day in UTC. A query
        // built from this and then compared against UTC columns is off by the
        // offset, and off by a whole date label for anything after 23:00.
        $inZone = $evening->startOfDay();
        $this->assertSame('2024-03-15 00:00', $inZone->format('Y-m-d H:i'));
        $this->assertSame('2024-03-14 23:00', $inZone->utc()->format('Y-m-d H:i'));

        // Converting to UTC first and then taking the day boundary is a
        // different instant, one offset hour later, for the same input date.
        $convertedFirst = $evening->utc()->startOfDay();
        $this->assertSame('2024-03-15 00:00', $convertedFirst->format('Y-m-d H:i'));
        $this->assertNotSame($inZone->getTimestamp(), $convertedFirst->getTimestamp());
        $this->assertSame(3600, $convertedFirst->getTimestamp() - $inZone->getTimestamp());

        // Both readings print the same date and time. Only the zone attached to
        // them says which midnight was meant.
        $this->assertSame($inZone->format('Y-m-d H:i'), $convertedFirst->format('Y-m-d H:i'));
        $this->assertSame(self::ZONE, $inZone->getTimezone()->getName());
        $this->assertSame('UTC', $convertedFirst->getTimezone()->getName());
    }

    public function testSetTestNowFreezesBothClassesAndLeaksUntilItIsCleared(): void
    {
        // The ambient default zone is UTC here, which is why a Paris test now
        // reads back two hours earlier below. This is also why zone bugs stay
        // invisible on a server and appear on someone's laptop.
        $this->assertSame('UTC', date_default_timezone_get());
        $this->assertFalse(Carbon::hasTestNow());

        $frozen = CarbonImmutable::parse('2024-05-01 10:00:00', self::ZONE);
        Carbon::setTestNow($frozen);

        // One switch for both classes: setting it on Carbon freezes
        // CarbonImmutable as well, and they read back the same object.
        $this->assertTrue(Carbon::hasTestNow());
        $this->assertTrue(CarbonImmutable::hasTestNow());
        $this->assertSame(Carbon::getTestNow(), CarbonImmutable::getTestNow());
        $this->assertTrue(Carbon::now()->equalTo($frozen));
        $this->assertTrue(CarbonImmutable::now()->equalTo($frozen));
        $this->assertTrue((new Carbon())->equalTo($frozen));
        $this->assertTrue(Carbon::parse('now')->equalTo($frozen));
        $this->assertTrue(Schedule::receiptStamp()->equalTo($frozen));

        // Deterministic means identical, not merely close: two reads a moment
        // apart are the same microsecond.
        $first = Carbon::now();
        usleep(2000);
        $this->assertSame($first->format('Y-m-d H:i:s.u'), Carbon::now()->format('Y-m-d H:i:s.u'));

        // setTestNow freezes the instant, not the zone. now() is still rendered
        // in the ambient default zone, so the hour you set is not the hour you
        // read back — the assertion people write against the string fails while
        // the date is exactly right.
        $this->assertSame('08:00', Carbon::now()->format('H:i'));
        $this->assertSame('10:00', Carbon::now(self::ZONE)->format('H:i'));
        $this->assertSame('UTC', Carbon::now()->getTimezone()->getName());

        // Relative strings resolve against the frozen clock, absolute ones do
        // not, and PHP's own clock is untouched — so any code mixing date() or
        // time() with Carbon disagrees with itself while a test now is set.
        $this->assertSame('2024-05-02 00:00', Carbon::parse('tomorrow')->format('Y-m-d H:i'));
        $this->assertSame('2020-01-01 00:00', Carbon::parse('2020-01-01 00:00', self::ZONE)->format('Y-m-d H:i'));
        $this->assertNotSame(time(), Carbon::now()->getTimestamp());

        // Clearing is a global switch too, and it is the step that gets
        // forgotten: nothing scopes the frozen clock to this test, so every
        // later test in the process would keep seeing May 2024.
        Carbon::setTestNow();
        $this->assertFalse(Carbon::hasTestNow());
        $this->assertFalse(CarbonImmutable::hasTestNow());
        $this->assertNull(Carbon::getTestNow());
        $this->assertTrue(Carbon::now()->greaterThan($frozen));

        // setTestNowAndTimezone is the variant that also moves the default
        // zone, so now() reads back the hour you set. It restores the previous
        // zone when cleared, which the plain setter has no reason to do.
        Carbon::setTestNowAndTimezone($frozen);
        $this->assertSame(self::ZONE, date_default_timezone_get());
        $this->assertSame('10:00', Carbon::now()->format('H:i'));
        Carbon::setTestNowAndTimezone();
        $this->assertSame('UTC', date_default_timezone_get());
        $this->assertFalse(Carbon::hasTestNow());

        // withTestNow is the version that cannot leak: it restores the previous
        // state on the way out, including when the body throws.
        $seen = Carbon::withTestNow($frozen, fn () => Carbon::now()->getTimestamp());
        $this->assertSame($frozen->getTimestamp(), $seen);
        $this->assertFalse(Carbon::hasTestNow());

        try {
            Carbon::withTestNow($frozen, function (): void {
                throw new RuntimeException('the test failed inside the frozen block');
            });
            $this->fail('withTestNow should not swallow the exception');
        } catch (RuntimeException $exception) {
            $this->assertSame('the test failed inside the frozen block', $exception->getMessage());
        }

        $this->assertFalse(Carbon::hasTestNow());
    }
}
