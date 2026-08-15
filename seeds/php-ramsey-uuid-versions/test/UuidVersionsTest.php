<?php

use Csx\UuidVersions;
use PHPUnit\Framework\TestCase;
use Ramsey\Uuid\Exception\InvalidUuidStringException;
use Ramsey\Uuid\Exception\UnsupportedOperationException;
use Ramsey\Uuid\Lazy\LazyUuidFromString;
use Ramsey\Uuid\Rfc4122\Fields as Rfc4122Fields;
use Ramsey\Uuid\Rfc4122\FieldsInterface as Rfc4122FieldsInterface;
use Ramsey\Uuid\Rfc4122\UuidV4;
use Ramsey\Uuid\Rfc4122\UuidV7;
use Ramsey\Uuid\Uuid;
use Ramsey\Uuid\UuidInterface;

final class UuidVersionsTest extends TestCase
{
    /** A real v7, fixed so every byte of it can be asserted. */
    private const SAMPLE = '0192397e-6a4b-7c8d-9e0f-102030405060';

    public function testUuid7SortsInCreationOrderWhileUuid4DoesNot(): void
    {
        $v7 = UuidVersions::batchV7(32);
        $created = UuidVersions::strings($v7);

        // The property worth having: sorting the canonical strings gives back
        // the order they were generated in. This is a bare loop with no sleep,
        // so most of the batch shares one millisecond — ramsey increments the
        // random part instead of redrawing it, which is what keeps a burst
        // monotonic. 400 batches of 32 in this image: zero out of order.
        $this->assertSame($created, UuidVersions::sortedCopy($created));

        // Same for the 16-byte storage form, which is the comparison a
        // BINARY(16) index actually makes.
        $bytes = UuidVersions::storageBytes($v7);
        $this->assertSame($bytes, UuidVersions::sortedCopy($bytes));

        // And the reason it works: the leading hex digits are the timestamp,
        // so a burst shares them. The first 8 characters are the top 32 bits of
        // a 48-bit millisecond counter and only change every ~71 minutes.
        $this->assertCount(1, array_unique(array_map(
            static fn (string $s): string => substr($s, 0, 8),
            $created
        )));

        // v4 has no timestamp to sort by, so a batch comes back shuffled. This
        // is the one probabilistic assertion here: an accidental pass needs all
        // 32 draws to land in ascending order, which is 1 in 32! (~4e-36). It
        // did not happen once in 400 batches.
        $created4 = UuidVersions::strings(UuidVersions::batchV4(32));
        $this->assertNotSame($created4, UuidVersions::sortedCopy($created4));

        // Nothing is shared at the front either — every v4 draws all 122 bits.
        $this->assertGreaterThan(1, count(array_unique(array_map(
            static fn (string $s): string => substr($s, 0, 8),
            $created4
        ))));

        // Both versions still pin the same two nibbles: position 14 is the
        // version and position 19 is the variant. Those 6 bits are the only
        // part of a v4 that is not random.
        $this->assertSame('4', $created4[0][14]);
        $this->assertContains($created4[0][19], ['8', '9', 'a', 'b']);
        $this->assertSame('7', $created[0][14]);
        $this->assertContains($created[0][19], ['8', '9', 'a', 'b']);
    }

    public function testOnlyUuid7CarriesTheCreationTime(): void
    {
        // Deterministic version of the ordering claim, with no randomness in
        // it at all: uuid7 accepts the instant, and it lands in the first 12
        // hex digits as the Unix time in milliseconds.
        $at = Uuid::uuid7(new DateTimeImmutable('@1700000000'));
        $this->assertSame(
            str_pad(dechex(1700000000 * 1000), 12, '0', STR_PAD_LEFT),
            substr($at->toString(), 0, 8) . substr($at->toString(), 9, 4)
        );
        $this->assertSame('018bcfe56800', substr($at->toString(), 0, 8) . substr($at->toString(), 9, 4));
        $this->assertSame('1700000000.000000', $at->getDateTime()->format('U.u'));

        // So two v7 values built from known instants order by those instants,
        // as plain strings, with no parsing.
        $early = Uuid::uuid7(new DateTimeImmutable('@1000000000'));
        $late = Uuid::uuid7(new DateTimeImmutable('@2000000000'));
        $this->assertLessThan(0, strcmp($early->toString(), $late->toString()));
        $this->assertSame(-1, $early->compareTo($late));

        // A v4 has no time in it, and asking is a hard error rather than null,
        // so "just sort by the id" cannot be salvaged after the fact.
        try {
            Uuid::uuid4()->getDateTime();
            $this->fail('a v4 has no timestamp to return');
        } catch (UnsupportedOperationException $e) {
            $this->assertSame('Not a time-based UUID', $e->getMessage());
        }
    }

    public function testToStringAndGetBytesAreDifferentLengthsAndGetBytesIsNotHex(): void
    {
        $uuid = Uuid::fromString(self::SAMPLE);

        // Two different serialisations, and the length is the giveaway:
        // CHAR(36) for the string, BINARY(16) for the bytes.
        $this->assertSame(36, strlen($uuid->toString()));
        $this->assertSame(16, strlen($uuid->getBytes()));

        // getBytes() is NOT the hex digits with the hyphens taken out. It is
        // raw binary, so it is not printable and not hex-digit text; dropping
        // it into a text column writes control bytes.
        $hex = str_replace('-', '', self::SAMPLE);
        $this->assertNotSame($hex, $uuid->getBytes());
        $this->assertFalse(ctype_print($uuid->getBytes()));
        $this->assertFalse(ctype_xdigit($uuid->getBytes()));
        $this->assertSame("\x01\x92\x39\x7e\x6a\x4b\x7c\x8d\x9e\x0f\x10\x20\x30\x40\x50\x60", $uuid->getBytes());

        // The hex form is a third thing, 32 characters, and it is what
        // bin2hex(getBytes()) gives you.
        $this->assertSame($hex, bin2hex($uuid->getBytes()));
        $this->assertSame($hex, $uuid->getHex()->toString());
        $this->assertSame(32, strlen($uuid->getHex()->toString()));

        // Round trip through the bytes, which is the point of storing them.
        $this->assertTrue(Uuid::fromBytes($uuid->getBytes())->equals($uuid));
    }

    public function testFromStringRejectsMalformedInputAndDisagreesWithIsValid(): void
    {
        try {
            Uuid::fromString('not-a-uuid');
            $this->fail('a malformed string should not parse');
        } catch (InvalidUuidStringException $e) {
            // A specific exception, and the message echoes the input.
            $this->assertSame('Invalid UUID string: not-a-uuid', $e->getMessage());
            // It is a package subclass of the SPL class, so an existing
            // \InvalidArgumentException handler already catches it, and the
            // package interface lets you catch every uuid error at once.
            $this->assertInstanceOf(InvalidArgumentException::class, $e);
            $this->assertInstanceOf(\Ramsey\Uuid\Exception\UuidExceptionInterface::class, $e);
        }

        // One bad hex digit and one trailing character are both rejected.
        $this->assertNull(UuidVersions::parseOrNull('g192397e-6a4b-7c8d-9e0f-102030405060'));
        $this->assertNull(UuidVersions::parseOrNull(self::SAMPLE . 'x'));
        $this->assertNull(UuidVersions::parseOrNull(''));

        // The trap: isValid() is not the guard for fromString(). fromString()
        // strips hyphens before it validates, so the 32-character hex form
        // parses — while isValid() on that exact string says false. Code that
        // validates first and parses second rejects input the parser accepts.
        $hyphenless = str_replace('-', '', self::SAMPLE);
        $this->assertFalse(Uuid::isValid($hyphenless));
        $this->assertSame(self::SAMPLE, Uuid::fromString($hyphenless)->toString());

        // Braces, the urn form and uppercase go through both, and all four
        // spellings normalise to the same lowercase canonical string.
        foreach ([self::SAMPLE, '{' . self::SAMPLE . '}', 'urn:uuid:' . self::SAMPLE, strtoupper(self::SAMPLE)] as $spelling) {
            $this->assertTrue(Uuid::isValid($spelling));
            $this->assertSame(self::SAMPLE, Uuid::fromString($spelling)->toString());
        }
    }

    public function testTheNilAndMaxUuidsAreExactlyWhatTheRfcSays(): void
    {
        $this->assertSame('00000000-0000-0000-0000-000000000000', Uuid::NIL);
        $this->assertSame('ffffffff-ffff-ffff-ffff-ffffffffffff', Uuid::MAX);

        $nil = Uuid::fromString(Uuid::NIL);
        $max = Uuid::fromString(Uuid::MAX);

        // 128 zero bits and 128 one bits, which is the definition.
        $this->assertSame(str_repeat("\x00", 16), $nil->getBytes());
        $this->assertSame(str_repeat("\xff", 16), $max->getBytes());
        $this->assertSame('0', (string) $nil->getInteger());
        $this->assertSame('340282366920938463463374607431768211455', (string) $max->getInteger());
        $this->assertSame(Uuid::NIL, Uuid::fromBytes(str_repeat("\x00", 16))->toString());

        // Both are valid UUIDs, so a validity check will not screen them out —
        // an all-zero id arriving from a client is well-formed.
        $this->assertTrue(Uuid::isValid(Uuid::NIL));
        $this->assertTrue(Uuid::isValid(Uuid::MAX));

        // getVersion() is null for both, not 0 and not 15: the version nibble
        // holds a literal 0 or f and neither is a version. A match() on
        // getVersion() lands in the null arm for both of these.
        $this->assertNull($nil->getVersion());
        $this->assertNull($max->getVersion());
        // Nor do they carry the RFC 4122 variant, so a variant check rejects
        // them as well.
        $this->assertSame(Uuid::RESERVED_NCS, $nil->getFields()->getVariant());
        $this->assertSame(Uuid::RESERVED_FUTURE, $max->getFields()->getVariant());
        $this->assertNotSame(Uuid::RFC_4122, $nil->getFields()->getVariant());
        $this->assertNotSame(Uuid::RFC_4122, $max->getFields()->getVariant());

        // The fields object has the real predicates — and they are not
        // symmetric. isNil() is on the RFC 4122 fields interface; isMax() is
        // not, it only exists on the concrete Fields class. Code typed against
        // the interface can detect nil and cannot detect max.
        $this->assertTrue(method_exists(Rfc4122FieldsInterface::class, 'isNil'));
        $this->assertFalse(method_exists(Rfc4122FieldsInterface::class, 'isMax'));
        $this->assertTrue(method_exists(Rfc4122Fields::class, 'isMax'));
        $this->assertSame('nil', UuidVersions::describe($nil));
        $this->assertSame('max', UuidVersions::describe($max));
        $this->assertSame('v7', UuidVersions::describe(Uuid::fromString(self::SAMPLE)));
        $this->assertSame('v4', UuidVersions::describe(Uuid::uuid4()));

        // They are the ends of the ordering, which is the other thing the RFC
        // fixes them for: a generated UUID always sorts strictly between them.
        $this->assertSame(-1, $nil->compareTo($max));
        $this->assertSame(1, $max->compareTo($nil));
        $generated = Uuid::uuid4();
        $this->assertGreaterThan(0, $generated->compareTo($nil));
        $this->assertLessThan(0, $generated->compareTo($max));
    }

    public function testDoubleEqualsIsNotEqualsBecauseTheFactoryReturnsALazyProxy(): void
    {
        // First surprise, and the cause of the rest: the factory hands back a
        // proxy. Uuid::uuid4() is not a UuidV4 and is not even a
        // Ramsey\Uuid\Uuid — only a UuidInterface. Type hints and instanceof
        // checks written against the concrete classes never match.
        $generated = Uuid::uuid4();
        $this->assertInstanceOf(LazyUuidFromString::class, $generated);
        $this->assertInstanceOf(UuidInterface::class, $generated);
        $this->assertNotInstanceOf(UuidV4::class, $generated);
        $this->assertNotInstanceOf(Uuid::class, $generated);
        $this->assertSame(4, $generated->getVersion());
        $this->assertInstanceOf(LazyUuidFromString::class, Uuid::uuid7());
        $this->assertNotInstanceOf(UuidV7::class, Uuid::uuid7());

        $a = Uuid::fromString(self::SAMPLE);
        $b = Uuid::fromString(self::SAMPLE);

        // Measured, and the opposite of the usual warning: == on two freshly
        // parsed UUIDs of the same value is TRUE. That is what makes this
        // dangerous — == looks like it works, so nothing pushes you to equals().
        $this->assertTrue($a == $b);
        $this->assertFalse($a === $b);
        $this->assertTrue($a->equals($b));

        // Then a read that needs the real object builds it and caches it on
        // that instance. == compares properties, so filling one side's cache
        // flips the answer. The value did not change: equals() and compareTo()
        // still agree, and both strings are still identical.
        UuidVersions::describe($a);
        $this->assertFalse($a == $b);
        $this->assertTrue($a->equals($b));
        $this->assertSame(0, $a->compareTo($b));
        $this->assertSame($a->toString(), $b->toString());

        // Filling the other side's cache flips it back. An equality that
        // depends on which methods were called earlier is not an equality.
        UuidVersions::describe($b);
        $this->assertTrue($a == $b);

        // It also breaks with no method calls at all, purely from how the
        // value was spelled: the braced form takes the decoding path and comes
        // back as a concrete UuidV7 rather than a proxy.
        $braced = Uuid::fromString('{' . self::SAMPLE . '}');
        $plain = Uuid::fromString(self::SAMPLE);
        $this->assertInstanceOf(UuidV7::class, $braced);
        $this->assertInstanceOf(LazyUuidFromString::class, $plain);
        $this->assertFalse($braced == $plain);
        $this->assertTrue($braced->equals($plain));
        $this->assertTrue($plain->equals($braced));
        $this->assertSame(0, $braced->compareTo($plain));

        // in_array/array_search default to ==, so a lookup over a mixed list
        // misses the value it is holding.
        $this->assertFalse(in_array($plain, [$braced], true));
        $this->assertFalse(in_array($plain, [$braced], false));

        // equals() is typed ?object. Handing it the string off a request is a
        // TypeError, not a false — the one place a bare == on the toString()
        // values is what you actually want.
        try {
            $plain->equals(self::SAMPLE);
            $this->fail('equals() does not take a string');
        } catch (TypeError $e) {
            $this->assertStringContainsString('must be of type ?object', $e->getMessage());
        }
        $this->assertFalse($plain->equals(null));
        $this->assertFalse($plain->equals(new stdClass()));
        $this->assertSame(self::SAMPLE, (string) $plain);
    }
}
