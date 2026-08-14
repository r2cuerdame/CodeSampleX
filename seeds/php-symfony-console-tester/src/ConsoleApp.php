<?php

namespace Csx;

use Symfony\Component\Console\Application;
use Symfony\Component\Console\Command\Command;

/**
 * Builds the Application the tests use, with the two settings that a test
 * harness needs and a real console entry point does not.
 *
 * setAutoExit(false) stops Application::run() from calling exit() with the
 * status code, which would take PHPUnit down with it. setCatchExceptions(false)
 * stops it from rendering an exception as a pretty error block and returning a
 * non-zero status, so a broken invocation reaches the test as a throwable
 * instead of as formatted text.
 *
 * add() was removed in symfony/console 8.0; addCommand() is the replacement,
 * and it is what every pre-7.4 snippet gets wrong.
 */
final class ConsoleApp
{
    public static function build(Command ...$commands): Application
    {
        $application = new Application('csx', '1.0.0');
        $application->setAutoExit(false);
        $application->setCatchExceptions(false);

        foreach ($commands as $command) {
            $application->addCommand($command);
        }

        return $application;
    }
}
