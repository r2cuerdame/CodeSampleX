<?php

// Deliberately NO declare(strict_types=1) in this file. strict_types is a property of
// the CALLING file, and most PHP codebases never declare it. That makes the float
// trap far worse than a TypeError: in a non-strict file, PHP coerces the float to int
// before of() ever sees it, so BigDecimal::of(19.99) quietly becomes BigDecimal(19)
// and the only trace is an E_DEPRECATED notice that a test suite usually swallows.
//
// This class exists to pin that silent path. It is the whole reason of() must never
// be handed a float, in either kind of file.

namespace Csx;

use Brick\Math\BigDecimal;

final class LooseCall
{
    /**
     * Calls BigDecimal::of() with a float from a file that has no strict_types.
     *
     * Returns the resulting decimal as a string plus whether PHP raised a deprecation,
     * with a local error handler so the notice is observed rather than printed.
     *
     * @return array{string, bool}
     */
    public static function ofFloat(float $value): array
    {
        $deprecated = false;

        set_error_handler(static function (int $errno) use (&$deprecated): bool {
            if ($errno === E_DEPRECATED) {
                $deprecated = true;
            }

            // Returning true marks the error handled, so it is not also printed.
            return true;
        });

        try {
            // The array is built left to right, so the call runs before $deprecated
            // is read, and the finally below runs after both.
            return [(string) BigDecimal::of($value), $deprecated];
        } finally {
            restore_error_handler();
        }
    }
}
