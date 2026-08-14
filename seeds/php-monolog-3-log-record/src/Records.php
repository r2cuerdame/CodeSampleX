<?php

namespace Csx;

use Monolog\Handler\TestHandler;
use Monolog\Level;
use Monolog\Logger;
use Monolog\LogRecord;

/**
 * Monolog 2 -> 3, where the break is NOT where the guides put it.
 *
 * Measured against monolog 3 rather than assumed: Logger::WARNING and the
 * other integer constants still exist and still equal the enum's ->value,
 * and addRecord still accepts an int. So the "replace every constant" step
 * is not what breaks an upgrade.
 *
 * What breaks is the handler side. A record is a LogRecord object now, not
 * an array — and because LogRecord implements ArrayAccess, $record['message']
 * keeps working. Code reading records looks fine and keeps passing until it
 * reaches an is_array(), an array_key_exists() or an array function, which
 * are the paths that silently stop matching.
 */
final class Records
{
    /** @return array{0: LogRecord, 1: TestHandler} */
    public static function capture(string $message, array $context = []): array
    {
        $handler = new TestHandler();
        $logger = new Logger('csx', [$handler]);
        $logger->warning($message, $context);
        return [$handler->getRecords()[0], $handler];
    }

    public static function levelIntStillMatchesEnum(): bool
    {
        return Logger::WARNING === Level::Warning->value;
    }

    public static function addRecordStillAcceptsInt(): string
    {
        return (string) (new \ReflectionMethod(Logger::class, 'addRecord'))
            ->getParameters()[0]->getType();
    }
}
