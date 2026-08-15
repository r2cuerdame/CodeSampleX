<?php

declare(strict_types=1);

namespace Csx;

use Brick\Math\BigDecimal;
use Brick\Math\BigInteger;
use Brick\Math\RoundingMode;

/**
 * brick/math 0.19 is not the 0.11-era library that almost every snippet and every
 * remembered signature was written against. The package went 0.13 -> 0.19 inside six
 * months of 2026, and four of the changes turn ordinary-looking code into a fatal
 * error or, once, into a silently wrong number.
 *
 * 1. RoundingMode was a final class of int constants: RoundingMode::HALF_UP === 5.
 *    It is now a pure (non-backed) enum with PascalCase cases. RoundingMode::HALF_UP
 *    is an "Undefined constant" Error, and passing the int 5 is a TypeError. There
 *    are 11 cases now, not 10 -- HalfOdd was added alongside HalfEven.
 *
 * 2. BigNumber::of() is typed BigNumber|int|string. It does NOT accept float.
 *    Floats go through two explicitly named constructors instead, because the
 *    conversion a caller wants is genuinely ambiguous and the library refuses to
 *    guess. See fromPriceFeed() / exactIeeeValue() below.
 *
 * 3. BigDecimal::dividedBy() takes a MANDATORY int $scale in argument 2:
 *      dividedBy(BigNumber|int|string $that, int $scale, RoundingMode $mode = Unnecessary)
 *    So $d->dividedBy($x) is an ArgumentCountError -- not the RoundingNecessaryException
 *    that 0.11 threw -- and $d->dividedBy($x, RoundingMode::HalfUp) is a TypeError.
 *    Dividing without naming a scale is now a separate method: dividedByExact().
 *
 * 4. BigInteger::dividedBy() takes the RoundingMode in argument 2, because an integer
 *    has no scale to ask for. The same method name on two sibling classes means
 *    something different in the same argument position.
 */
final class Money
{
    /**
     * Turn a float coming off a price feed into an exact decimal.
     *
     * The obvious call, BigDecimal::of($price), does not work: of() has no float in
     * its parameter type. fromFloatShortest() is what a human means by "the float
     * 19.99" -- the shortest decimal string that round-trips back to the same double.
     */
    public static function fromPriceFeed(float $price): BigDecimal
    {
        return BigDecimal::fromFloatShortest($price);
    }

    /**
     * The other float constructor: the full IEEE-754 expansion of the double.
     *
     * This is what Java's `new BigDecimal(double)` does, and it is the reason
     * brick/math refuses to pick a default. It is almost never what a money app
     * wants -- fromFloatExact(0.1) has 55 significant digits.
     */
    public static function exactIeeeValue(float $price): BigDecimal
    {
        return BigDecimal::fromFloatExact($price);
    }

    /**
     * Per-unit price, rounded to cents.
     *
     * Argument 2 is the SCALE (an int), argument 3 is the RoundingMode. Writing
     * dividedBy($units, RoundingMode::HalfUp) reads perfectly well and is a TypeError.
     */
    public static function unitPrice(string $total, int $units): BigDecimal
    {
        return BigDecimal::of($total)->dividedBy($units, 2, RoundingMode::HalfUp);
    }

    /**
     * Division that must come out exact, with no scale named up front.
     *
     * This is the method that inherited 0.11's dividedBy($that) behaviour: it throws
     * RoundingNecessaryException on a non-terminating quotient rather than an
     * ArgumentCountError, so it is the one to reach for when the old snippet's
     * intent was "divide and complain if it does not divide cleanly".
     */
    public static function exactly(string $total, string $divisor): BigDecimal
    {
        return BigDecimal::of($total)->dividedByExact($divisor);
    }

    /**
     * Integer cents split N ways, truncating the remainder.
     *
     * Note the shape: no scale argument exists here at all. Passing the int 2 in the
     * position that BigDecimal uses for a scale is a TypeError on BigInteger.
     */
    public static function centsPerShare(int $cents, int $shares): BigInteger
    {
        return BigInteger::of($cents)->dividedBy($shares, RoundingMode::Down);
    }
}
