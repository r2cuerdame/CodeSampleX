<?php

declare(strict_types=1);

use Brick\Math\BigDecimal;
use Brick\Math\BigInteger;
use Brick\Math\Exception\MathException;
use Brick\Math\Exception\RoundingNecessaryException;
use Brick\Math\RoundingMode;
use Csx\LooseCall;
use Csx\Money;
use PHPUnit\Framework\TestCase;

final class MoneyTest extends TestCase
{
    /** Runs $fn and returns whatever it threw, so the wrong call and the right one
     *  can be asserted side by side in a single test. */
    private static function thrownBy(callable $fn): \Throwable
    {
        try {
            $fn();
        } catch (\Throwable $e) {
            return $e;
        }

        self::fail('expected a throwable, the call succeeded');
    }

    public function testRoundingModeIsAnEnumNotIntConstants(): void
    {
        // The 0.11 spelling. RoundingMode::HALF_UP is now an undefined constant,
        // which is a plain Error at the point of use -- there is no deprecation
        // shim and no BC alias.
        $e = self::thrownBy(static fn() => constant(RoundingMode::class . '::HALF_UP'));
        self::assertInstanceOf(\Error::class, $e);
        self::assertStringContainsString('Undefined constant', $e->getMessage());

        self::assertTrue(enum_exists(RoundingMode::class));
        self::assertSame('HalfUp', RoundingMode::HalfUp->name);

        // Pure enum, not backed: there is no ->value and no ::from(5) either, so
        // code that stored the old integer in a column cannot map it back directly.
        self::assertFalse((new ReflectionEnum(RoundingMode::class))->isBacked());

        // 11 cases, not the 10 of the old constant list: HalfOdd joined HalfEven.
        self::assertCount(11, RoundingMode::cases());
        self::assertSame('3', (string) BigDecimal::of('2.5')->toScale(0, RoundingMode::HalfOdd));
        self::assertSame('2', (string) BigDecimal::of('2.5')->toScale(0, RoundingMode::HalfEven));
    }

    public function testOfRejectsFloatsAndTwoNamedConstructorsReplaceIt(): void
    {
        // BigNumber::of() is typed BigNumber|int|string. Under strict_types the
        // obvious BigDecimal::of(19.99) does not reach any math code at all.
        $e = self::thrownBy(static fn() => BigDecimal::of(19.99));
        self::assertInstanceOf(\TypeError::class, $e);
        self::assertStringContainsString('must be of type Brick\Math\BigNumber|string|int', $e->getMessage());

        // The replacement pair. Both are exact; they disagree about what the float
        // 0.1 "is", which is precisely why of() refuses to choose for you.
        self::assertSame('19.99', (string) Money::fromPriceFeed(19.99));
        self::assertSame('0.1', (string) BigDecimal::fromFloatShortest(0.1));
        self::assertSame(
            '0.1000000000000000055511151231257827021181583404541015625',
            (string) Money::exactIeeeValue(0.1),
        );

        // Shortest is not "prettiest": it round-trips, so accumulated float error
        // survives into the decimal rather than being rounded away.
        self::assertSame('0.30000000000000004', (string) BigDecimal::fromFloatShortest(0.1 + 0.2));
    }

    public function testOfWithAFloatSilentlyTruncatesWhereStrictTypesIsOff(): void
    {
        // The dangerous half of the same trap. In a file without strict_types the
        // float is coerced to int at the call boundary, so of() sees 19, succeeds,
        // and hands back a BigDecimal that is wrong by 99 cents.
        [$value, $deprecated] = LooseCall::ofFloat(19.99);
        self::assertSame('19', $value);
        self::assertTrue($deprecated, 'the only signal is an E_DEPRECATED notice');
    }

    public function testDividedByNeedsAMandatoryIntScaleInArgumentTwo(): void
    {
        // 0.11's single-argument division. It is now an ArgumentCountError -- a
        // TypeError subclass, so a `catch (MathException)` around it never fires.
        $e = self::thrownBy(static fn() => BigDecimal::of('10')->dividedBy(3));
        self::assertInstanceOf(\ArgumentCountError::class, $e);
        self::assertNotInstanceOf(MathException::class, $e);

        // Reading argument 2 as the rounding mode is the natural misreading, and
        // it fails on the type rather than on the arity.
        $e = self::thrownBy(static fn() => BigDecimal::of('10')->dividedBy(3, RoundingMode::HalfUp));
        self::assertInstanceOf(\TypeError::class, $e);
        self::assertStringContainsString('Argument #2 ($scale) must be of type int', $e->getMessage());

        // The correct call: divisor, scale, mode.
        self::assertSame('3.3333', (string) BigDecimal::of('10')->dividedBy(3, 4, RoundingMode::HalfUp));
        self::assertSame('16.67', (string) Money::unitPrice('50.00', 3));

        // The mode still defaults to Unnecessary, so naming a scale that cannot hold
        // the quotient throws rather than rounding quietly.
        self::assertInstanceOf(
            RoundingNecessaryException::class,
            self::thrownBy(static fn() => BigDecimal::of('10')->dividedBy(3, 4)),
        );
    }

    public function testDividedByExactCarriesTheOldNoScaleBehaviour(): void
    {
        self::assertSame('2.5', (string) Money::exactly('10', '4'));

        $e = self::thrownBy(static fn() => Money::exactly('10', '3'));
        self::assertInstanceOf(RoundingNecessaryException::class, $e);

        // Every brick/math exception implements the MathException INTERFACE; there
        // is no MathException base class to extend or instantiate.
        self::assertInstanceOf(MathException::class, $e);
        self::assertTrue(interface_exists(MathException::class));
        self::assertFalse(class_exists(MathException::class));
    }

    public function testBigIntegerDividedByPutsTheModeInArgumentTwoInstead(): void
    {
        // Same method name, sibling class, different meaning for argument 2: an
        // integer has no scale, so the mode moves up one position.
        self::assertSame('3', (string) Money::centsPerShare(10, 3));
        self::assertSame('3', (string) BigInteger::of(10)->dividedBy(3, RoundingMode::HalfUp));

        // Handing BigInteger the scale that BigDecimal demands is a TypeError.
        $e = self::thrownBy(static fn() => BigInteger::of(10)->dividedBy(3, 2));
        self::assertInstanceOf(\TypeError::class, $e);
        self::assertStringContainsString('$roundingMode', $e->getMessage());

        // And BigInteger's default really is Unnecessary, so a remainder throws.
        self::assertInstanceOf(
            RoundingNecessaryException::class,
            self::thrownBy(static fn() => BigInteger::of(10)->dividedBy(3)),
        );
    }

    public function testEqualityIgnoresScaleButPhpsEqualsOperatorDoesNot(): void
    {
        // isEqualTo compares values, so it does not repeat Java's BigDecimal.equals
        // scale trap.
        self::assertTrue(BigDecimal::of('1.0')->isEqualTo('1.00'));
        self::assertSame(0, BigDecimal::of('1.0')->compareTo('1.00'));

        // But BigDecimal is a readonly class holding an unscaled value and a scale,
        // so PHP's own == compares those two properties and reports false. Any code
        // that reached for == or in_array() without the strict flag gets the Java
        // behaviour back by accident.
        self::assertFalse(BigDecimal::of('1.0') == BigDecimal::of('1.00'));

        // The scale is still carried in the string form and in the accessors.
        self::assertSame('1.10', (string) BigDecimal::of('1.10'));
        self::assertSame(2, BigDecimal::of('1.10')->getScale());
        self::assertSame('110', (string) BigDecimal::of('1.10')->getUnscaledValue());
    }
}
