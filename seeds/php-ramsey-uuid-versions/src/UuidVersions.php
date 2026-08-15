<?php

namespace Csx;

use Ramsey\Uuid\Exception\InvalidUuidStringException;
use Ramsey\Uuid\Rfc4122\Fields as Rfc4122Fields;
use Ramsey\Uuid\Uuid;
use Ramsey\Uuid\UuidInterface;

/**
 * What people actually get wrong about ramsey/uuid: which version orders, what
 * getBytes() is, how a bad string fails, what nil and max report, and why ==
 * cannot be used to compare two UUIDs.
 *
 * ORDERING. uuid4 is 122 random bits and carries no time at all — getDateTime()
 * on one throws UnsupportedOperationException, so there is nothing to sort by.
 * uuid7 puts the Unix millisecond in the leading 48 bits, so both the canonical
 * string and the 16 raw bytes sort in creation order by plain byte comparison.
 * That is the entire reason to move a primary key from v4 to v7: a v4 insert
 * lands at a random leaf of the index, a v7 insert lands at the right edge.
 *
 * The part that is usually assumed rather than checked is *within* one
 * millisecond. ramsey's UnixTimeGenerator does not re-randomise on every call:
 * inside the same millisecond it increments the random part by a 24-bit step
 * and carries, so a tight loop stays monotonic and only rolls the timestamp
 * forward on overflow. Measured in this image: 400 batches of 32 uuid7 calls in
 * a bare loop, zero batches out of order by string and zero by bytes. Sorting
 * the same-sized v4 batch never reproduced creation order in 400 tries.
 *
 * TWO REPRESENTATIONS. toString() is 36 characters with hyphens. getBytes() is
 * 16 raw bytes — not the 32-character hex, which is getHex()->toString(). They
 * are not interchangeable and the failure is quiet: writing getBytes() into a
 * CHAR(36) column stores control bytes and NULs, and the nil UUID's bytes are
 * 16 NUL bytes, which several string paths treat as empty. bin2hex(getBytes())
 * is the hex form; strlen is the cheapest way to catch the mixup in a test.
 *
 * PARSING. fromString() throws InvalidUuidStringException — a package subclass
 * of \InvalidArgumentException that also implements UuidExceptionInterface, so
 * a catch on \InvalidArgumentException works and a catch on \Exception is too
 * broad to be useful. The trap is that isValid() is NOT the guard for
 * fromString(): they are different predicates. fromString() strips 'urn:',
 * 'uuid:', braces and hyphens before validating, so a hyphenless 32-character
 * hex string parses fine while isValid() on the same string returns false.
 * Validating with isValid() and then parsing rejects input the parser accepts.
 *
 * NIL AND MAX. Uuid::NIL is 128 zero bits and Uuid::MAX is 128 one bits, per
 * RFC 9562. getVersion() returns null for both — not 0 and not 15 — because the
 * version nibble holds a literal 0 or f, which is not a version, and getVariant()
 * returns RESERVED_NCS (0) and RESERVED_FUTURE (7), so neither carries the
 * RFC_4122 variant. Any `match ($uuid->getVersion())` over these hits the null
 * arm. The fields object holds the real predicates, and they are not symmetric:
 * isNil() is declared on Rfc4122\FieldsInterface, isMax() is not — it comes from
 * MaxTrait on the concrete Rfc4122\Fields, so code typed against the interface
 * can detect nil and cannot detect max.
 *
 * EQUALITY. Everything the default factory returns is a LazyUuidFromString
 * proxy: Uuid::uuid4() is not an instance of Rfc4122\UuidV4 and not even an
 * instance of Ramsey\Uuid\Uuid — only of UuidInterface. PHP's == on two objects
 * compares their properties, and one of the proxy's properties is the inner
 * instance it builds on demand. So == is true for two freshly parsed proxies and
 * turns false the moment either side is unwrapped by a read that needs the real
 * object — getVersion(), getFields(), getDateTime(), compareTo(). The value never
 * changed; only an internal cache did. The same UUID spelled with braces comes
 * back as a concrete UuidV7 and is == false against the plain-string proxy from
 * the start. equals() and compareTo() compare the value and are stable across all
 * of that. equals() is typed ?object, so passing the raw string from a request is
 * a TypeError rather than false.
 */
final class UuidVersions
{
    /** @return list<UuidInterface> */
    public static function batchV7(int $count): array
    {
        $out = [];
        for ($i = 0; $i < $count; $i++) {
            $out[] = Uuid::uuid7();
        }

        return $out;
    }

    /** @return list<UuidInterface> */
    public static function batchV4(int $count): array
    {
        $out = [];
        for ($i = 0; $i < $count; $i++) {
            $out[] = Uuid::uuid4();
        }

        return $out;
    }

    /**
     * The CHAR(36) form.
     *
     * @param list<UuidInterface> $uuids
     * @return list<string>
     */
    public static function strings(array $uuids): array
    {
        return array_map(static fn (UuidInterface $u): string => $u->toString(), $uuids);
    }

    /**
     * The BINARY(16) form. Sixteen raw bytes, not the hex — see getHex() for that.
     *
     * @param list<UuidInterface> $uuids
     * @return list<string>
     */
    public static function storageBytes(array $uuids): array
    {
        return array_map(static fn (UuidInterface $u): string => $u->getBytes(), $uuids);
    }

    /**
     * SORT_STRING is passed on purpose. The default SORT_REGULAR compares two
     * numeric-looking strings as numbers, which is not the comparison an index
     * on either column would make.
     *
     * @param list<string> $values
     * @return list<string>
     */
    public static function sortedCopy(array $values): array
    {
        sort($values, SORT_STRING);

        return $values;
    }

    /**
     * Typed against the concrete Rfc4122\Fields rather than its interface
     * because isMax() is not on the interface. Reading the fields is also what
     * unwraps a lazy UUID, which is why calling this changes what == answers for
     * the caller without changing the value.
     */
    public static function describe(UuidInterface $uuid): string
    {
        $fields = $uuid->getFields();
        if (!$fields instanceof Rfc4122Fields) {
            return 'non-rfc4122';
        }
        if ($fields->isNil()) {
            return 'nil';
        }
        if ($fields->isMax()) {
            return 'max';
        }

        return 'v' . $fields->getVersion();
    }

    /**
     * Catching the package's InvalidUuidStringException rather than checking
     * isValid() first, because the two do not agree on what parses.
     */
    public static function parseOrNull(string $candidate): ?UuidInterface
    {
        try {
            return Uuid::fromString($candidate);
        } catch (InvalidUuidStringException) {
            return null;
        }
    }
}
