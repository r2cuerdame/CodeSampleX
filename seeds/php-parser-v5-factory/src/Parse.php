<?php

namespace Csx;

use PhpParser\Error;
use PhpParser\ParserFactory;

/**
 * php-parser v5 removed ParserFactory::create(ParserFactory::PREFER_PHP7).
 * Every v4 snippet on the internet calls it, and on v5 that is a fatal
 * "Call to undefined method" rather than a deprecation — the PREFER_*
 * constants were removed too, so even the argument fails to resolve. The
 * replacements are createForNewestSupportedVersion() and createForVersion().
 *
 * The other thing v4 code gets wrong: a parse error is an exception in v5,
 * not a null return paired with an errorHandler out-parameter.
 */
final class Parse
{
    public static function statementCount(string $code): int
    {
        $parser = (new ParserFactory())->createForNewestSupportedVersion();
        return count($parser->parse($code));
    }

    /** Returns the error message, or null when the code parses. */
    public static function errorOf(string $code): ?string
    {
        $parser = (new ParserFactory())->createForNewestSupportedVersion();
        try {
            $parser->parse($code);
            return null;
        } catch (Error $e) {
            return $e->getRawMessage();
        }
    }

    public static function factoryHasV4Method(): bool
    {
        return method_exists(ParserFactory::class, 'create');
    }
}
