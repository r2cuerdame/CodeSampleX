<?php

use Csx\Parse;
use PHPUnit\Framework\TestCase;

final class ParseTest extends TestCase
{
    public function testParsesWithTheV5Factory(): void
    {
        $this->assertSame(2, Parse::statementCount('<?php $a = 1; $b = 2;'));
    }

    public function testTheV4FactoryMethodIsGone(): void
    {
        // The migration stated as a fact rather than a warning: v4 code
        // calling ParserFactory::create() fatals on v5.
        $this->assertFalse(Parse::factoryHasV4Method());
    }

    public function testParseErrorsAreExceptionsNotNull(): void
    {
        $this->assertNull(Parse::errorOf('<?php $ok = 1;'));
        $this->assertStringContainsString('Syntax error', Parse::errorOf('<?php $a = ;'));
    }
}
