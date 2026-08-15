<?php

namespace Csx;

use League\Csv\Bom;
use League\Csv\Reader;
use League\Csv\Writer;

/**
 * league/csv 9.28 on PHP 8.5, and the encoding/enclosure behaviour that does
 * not match either the folklore or the shape of the API.
 *
 * The BOM claim everyone repeats is out of date. A UTF-8 BOM does NOT end up
 * glued to the first header name: is_input_bom_included defaults to false, so
 * skipInputBOM() is already in force and getHeader() returns "id". Calling
 * skipInputBOM() changes nothing; includeInputBOM() is what reproduces the
 * classic bug, and it really is a bug — the key becomes "\xEF\xBB\xBFid", so
 * $record['id'] is an undefined array key on the one column people index by,
 * while the row count and every other column stay correct. The
 * BOM still exists as far as detection goes (getInputBOM() reports it either
 * way) — detection and stripping are separate switches. A hand-rolled
 * str_getcsv() has neither, which is where the folklore comes from.
 *
 * Stripping on the way in means nothing is put back on the way out: read a
 * BOM'd file and write the records to a new Writer and the BOM is gone, which
 * is exactly the regression that makes Excel render UTF-8 as mojibake.
 * setOutputBOM() is a separate, explicit decision.
 *
 * The escape character is the one that silently destroys documents. PHP's CSV
 * functions carry a proprietary escape character on top of RFC 4180 — a
 * backslash by default — and league/csv still inherits it in 9.28. A field
 * whose value ends in a backslash is enclosed by the writer precisely because
 * it holds the escape character, and the backslash is then not doubled, so the
 * line that gets written ends the field with a backslash immediately followed
 * by the closing quote. The reader reads that pair as an escaped quote and
 * keeps going: the delimiter, the line ending and the whole next record are
 * swallowed into one field, and a two-record document comes back as one record
 * of one field with nothing thrown. setEscape('') on BOTH sides is the fix, and
 * it has to be both — a writer with the escape disabled still encloses a value
 * that contains a comma, so the backslash-before-quote pair reappears and a
 * default reader still eats the line.
 *
 * The enclosure API reads like a fluent setter and is not one. encloseAll(),
 * encloseNecessary() and encloseNone() are predicates returning bool; the
 * setters are forceEnclosure(), necessaryEnclosure() and noEnclosure().
 * $writer->encloseAll() compiles, returns false, and changes nothing.
 *
 * setHeaderOffset() changes three things at once, not one. Records become
 * associative; their keys stay the document offsets, so the first data record
 * is at key 1 and $records[0] does not exist; and a duplicate column name
 * becomes fatal — but only when the records are asked for. getHeader() hands
 * back ["id","name","id"] without complaint and getRecords() throws SyntaxError
 * before yielding anything.
 *
 * Round-tripping is better than its reputation for whitespace and worse for
 * hand-written input. A field holding a newline, a single space, or several
 * spaces survives Writer -> Reader byte for byte (a space forces an enclosure,
 * an empty field does not get one). What loses whitespace is CSV written by
 * something else: PHP skips blanks before an enclosure, so ` "b" ` parses to
 * "b " — the leading space and the quotes vanish while the trailing space is
 * kept. Blank lines disappear entirely and take their offset with them.
 */
final class Csv
{
    public const UTF8_BOM = "\xEF\xBB\xBF";

    /**
     * The library defaults. fromString() rather than createFromString(),
     * which is deprecated since 9.27.0 and raises on every call.
     */
    public static function reader(string $document, ?int $headerOffset = null): Reader
    {
        $reader = Reader::fromString($document);

        return null === $headerOffset ? $reader : $reader->setHeaderOffset($headerOffset);
    }

    /**
     * The pre-9.4 behaviour, which is what most BOM advice on the internet is
     * still describing: the BOM stays in the first field of the first record.
     */
    public static function bomKeepingReader(string $document, ?int $headerOffset = null): Reader
    {
        $reader = Reader::fromString($document)->includeInputBOM();

        return null === $headerOffset ? $reader : $reader->setHeaderOffset($headerOffset);
    }

    /**
     * The only reader setting that makes a backslash survive. An empty escape
     * turns off PHP's proprietary escaping and leaves the RFC 4180 rule —
     * a doubled enclosure — as the only escape mechanism.
     */
    public static function rfcReader(string $document, ?int $headerOffset = null): Reader
    {
        $reader = Reader::fromString($document)->setEscape('');

        return null === $headerOffset ? $reader : $reader->setHeaderOffset($headerOffset);
    }

    public static function writer(?Bom $outputBom = null): Writer
    {
        $writer = Writer::fromString();

        return null === $outputBom ? $writer : $writer->setOutputBOM($outputBom);
    }

    public static function rfcWriter(?Bom $outputBom = null): Writer
    {
        return self::writer($outputBom)->setEscape('');
    }

    /** @param iterable<array<string|null>> $records */
    public static function write(iterable $records, ?Bom $outputBom = null): string
    {
        $writer = self::writer($outputBom);
        $writer->insertAll($records);

        return $writer->toString();
    }

    /** @param iterable<array<string|null>> $records */
    public static function rfcWrite(iterable $records, ?Bom $outputBom = null): string
    {
        $writer = self::rfcWriter($outputBom);
        $writer->insertAll($records);

        return $writer->toString();
    }

    /**
     * Serialize with the defaults and parse the result back with the defaults.
     *
     * @param list<list<string>> $records
     *
     * @return list<list<string>>
     */
    public static function roundTrip(array $records): array
    {
        return array_values(iterator_to_array(self::reader(self::write($records))->getRecords()));
    }

    /**
     * The same trip with the escape character disabled on both ends. Disabling
     * it on only one end is worse than leaving it alone.
     *
     * @param list<list<string>> $records
     *
     * @return list<list<string>>
     */
    public static function rfcRoundTrip(array $records): array
    {
        return array_values(iterator_to_array(self::rfcReader(self::rfcWrite($records))->getRecords()));
    }
}
