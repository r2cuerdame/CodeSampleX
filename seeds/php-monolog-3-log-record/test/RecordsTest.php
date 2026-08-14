<?php

use Csx\Records;
use Monolog\Level;
use Monolog\Logger;
use Monolog\LogRecord;
use PHPUnit\Framework\TestCase;

final class RecordsTest extends TestCase
{
    public function testTheIntegerLevelConstantsWereNotRemoved(): void
    {
        // Measured, and the opposite of the usual advice: Logger::WARNING
        // is still there on monolog 3 and still equals the enum's value.
        $this->assertTrue(defined('Monolog' . chr(92) . 'Logger::WARNING'));
        $this->assertSame(300, Logger::WARNING);
        $this->assertSame(300, Level::Warning->value);
        $this->assertTrue(Records::levelIntStillMatchesEnum());
        $this->assertStringContainsString('int', Records::addRecordStillAcceptsInt());
    }

    public function testARecordIsAnObjectSoIsArrayStopsMatching(): void
    {
        [$record] = Records::capture('hello', ['k' => 'v']);
        $this->assertInstanceOf(LogRecord::class, $record);
        // The line that breaks an upgraded handler, and it breaks quietly:
        // no error, the branch is simply never taken again.
        $this->assertFalse(is_array($record));
    }

    public function testButArrayReadsKeepWorkingWhichIsWhyItLooksFine(): void
    {
        [$record] = Records::capture('hello', ['k' => 'v']);
        $this->assertSame('hello', $record['message']);
        $this->assertSame('hello', $record->message);
        $this->assertSame(['k' => 'v'], $record->context);
        $this->assertSame('WARNING', $record->level->getName());
    }

    public function testDatetimeIsADateTimeImmutableSubclass(): void
    {
        [$record] = Records::capture('hello');
        $this->assertInstanceOf(DateTimeImmutable::class, $record->datetime);
        $this->assertNotSame(DateTimeImmutable::class, get_class($record->datetime));
    }

    public function testTestHandlerAssertionsReadTheObjectForm(): void
    {
        [, $handler] = Records::capture('hello');
        $this->assertTrue($handler->hasWarningThatContains('hello'));
        $this->assertFalse($handler->hasErrorThatContains('hello'));
    }
}
