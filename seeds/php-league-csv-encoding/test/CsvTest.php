<?php

use Csx\Csv;
use League\Csv\Bom;
use League\Csv\InvalidArgument;
use League\Csv\Reader;
use League\Csv\SyntaxError;
use PHPUnit\Framework\TestCase;

final class CsvTest extends TestCase
{
    private const DOCUMENT = "id,name\n1,ada\n2,bob\n";

    public function testTheUtf8BomIsAlreadyStrippedByDefault(): void
    {
        // Measured, and the opposite of the advice this library is usually
        // handed with: is_input_bom_included defaults to false, so a UTF-8 BOM
        // never reaches the first header name. skipInputBOM() is not the fix,
        // it is the status quo.
        $document = Csv::UTF8_BOM . "id,name\n1,ada\n";
        $reader = Csv::reader($document, 0);

        $this->assertSame(['id', 'name'], $reader->getHeader());
        $this->assertSame(['id' => '1', 'name' => 'ada'], $reader->first());
        $this->assertFalse($reader->isInputBOMIncluded());

        // Calling it explicitly changes nothing.
        $this->assertSame(
            ['id', 'name'],
            Reader::fromString($document)->skipInputBOM()->setHeaderOffset(0)->getHeader()
        );

        // Detecting the BOM and stripping it are separate: the reader still
        // reports which BOM the document carries after removing it from the
        // records, which is how you decide what to write back out.
        $this->assertSame(Csv::UTF8_BOM, $reader->getInputBOM());
        $this->assertSame(Bom::Utf8, Bom::tryFrom($reader->getInputBOM()));
        $this->assertSame(Csv::UTF8_BOM, Bom::Utf8->value);
        $this->assertSame(3, Bom::Utf8->length());

        // The folklore is true for the thing people write by hand. PHP's own
        // parser has no BOM concept at all, so the first column name comes back
        // five bytes long and nothing warns you.
        $firstLine = explode("\n", $document)[0];
        $this->assertSame([Csv::UTF8_BOM . 'id', 'name'], str_getcsv($firstLine, ',', '"', ''));
    }

    public function testIncludeInputBomIsWhatBreaksTheFirstColumnName(): void
    {
        $document = Csv::UTF8_BOM . "id,name\n1,ada\n";
        $reader = Csv::bomKeepingReader($document, 0);

        $this->assertSame([Csv::UTF8_BOM . 'id', 'name'], $reader->getHeader());
        $this->assertSame(5, strlen($reader->getHeader()[0]));

        // The damage is a missing array key on the one column everybody
        // indexes by, and it is a missing key rather than an error: the row
        // count is right, the values are right, only the lookup fails.
        $record = $reader->first();
        $this->assertArrayNotHasKey('id', $record);
        $this->assertArrayHasKey(Csv::UTF8_BOM . 'id', $record);
        $this->assertSame('1', $record[Csv::UTF8_BOM . 'id']);
        $this->assertSame('ada', $record['name']);

        // A quoted first header shows why the BOM has to come off before
        // parsing rather than after: PHP only honours an enclosure at the very
        // start of a field, so three bytes in front of it demote both quotes to
        // ordinary characters. The default reader strips the BOM and then the
        // quotes it was hiding; includeInputBOM() keeps all of it.
        $quoted = Csv::UTF8_BOM . '"id","name"' . "\n1,ada\n";
        $this->assertSame([Csv::UTF8_BOM . '"id"', 'name'], Csv::bomKeepingReader($quoted, 0)->getHeader());
        $this->assertSame(['id', 'name'], Csv::reader($quoted, 0)->getHeader());
    }

    public function testStrippingOnReadMeansTheBomIsGoneFromWhateverYouWriteBack(): void
    {
        $document = Csv::UTF8_BOM . "id,name\n1,ada\n";

        // The regression that turns accented text into mojibake in Excel: the
        // input BOM is not carried over, because the output BOM is a separate
        // and empty setting.
        $this->assertSame('', Csv::writer()->getOutputBOM());
        $this->assertSame("id,name\n1,ada\n", Csv::write(Csv::reader($document)->getRecords()));

        $this->assertSame(
            Csv::UTF8_BOM . "id,name\n1,ada\n",
            Csv::write(Csv::reader($document)->getRecords(), Bom::Utf8)
        );

        // And the BOM you put back is skipped again on the next read, so a
        // pipeline that re-encodes on every pass is stable rather than growing
        // three bytes each time.
        $withBom = Csv::write(Csv::reader($document)->getRecords(), Bom::Utf8);
        $this->assertSame(Csv::UTF8_BOM, Csv::reader($withBom)->getInputBOM());
        $this->assertSame(
            [['id', 'name'], ['1', 'ada']],
            array_values(iterator_to_array(Csv::reader($withBom)->getRecords()))
        );
        $this->assertSame($withBom, Csv::write(Csv::reader($withBom)->getRecords(), Bom::Utf8));
    }

    public function testAQuoteInsideAFieldIsDoubledAndDragsAnEnclosureOntoTheWholeField(): void
    {
        // What people expect is a backslash or a bare quote. RFC 4180 doubles
        // the quote and encloses the entire field, so a one-character change to
        // the data changes four characters of the line.
        $this->assertSame('"he said ""hi""",plain' . "\n", Csv::write([['he said "hi"', 'plain']]));
        $this->assertSame(['he said "hi"', 'plain'], Csv::reader('"he said ""hi""",plain' . "\n")->first());

        // Reading is asymmetric on purpose. A quote that is not the first
        // character of a field is data, so CSV produced by a naive writer still
        // parses the way its author intended.
        $this->assertSame(['a"b', 'c'], Csv::reader("a\"b,c\n")->first());

        // But a field that opens with a quote and then contains a single one is
        // malformed, and there is no error for it. The inner quote closes the
        // field early and everything up to the delimiter is appended raw, so
        // the characters come back reordered: "a"b" becomes ab", not a"b.
        // Assert the exact string, because "it parsed" is not a result.
        $this->assertSame(['ab"', 'c'], Csv::reader("\"a\"b\",c\n")->first());
    }

    public function testTheDefaultEscapeCharacterDestroysADocumentEndingAFieldWithABackslash(): void
    {
        // PHP's CSV functions carry a proprietary escape character on top of
        // RFC 4180, and league/csv still inherits it in 9.28.
        $this->assertSame('\\', Csv::writer()->getEscape());
        $this->assertSame('\\', Csv::reader('')->getEscape());

        $records = [['ends\\', 'next'], ['second', 'row']];
        $serialized = Csv::write($records);

        // The writer encloses the field because it contains the escape
        // character, and then does not double it. The closing quote is now
        // preceded by a backslash.
        $this->assertSame('"ends\\",next' . "\n" . 'second,row' . "\n", $serialized);

        // So the reader escapes that closing quote and keeps consuming:
        // delimiters, the line ending, and the whole next record end up inside
        // one field. Two records in, one record of one field out, no exception.
        $broken = Csv::reader($serialized);
        $this->assertSame(1, $broken->count());
        $this->assertSame(['ends\\",next' . "\n" . 'second,row' . "\n"], $broken->first());
        $this->assertNotSame($records, Csv::roundTrip($records));

        // setEscape('') removes PHP's mechanism and leaves the RFC's doubled
        // enclosure as the only one. It has to be set on both ends.
        $this->assertSame($records, array_values(iterator_to_array(Csv::rfcReader($serialized)->getRecords())));
        $this->assertSame($records, Csv::rfcRoundTrip($records));

        // Writer-only is not enough. With the escape off, a backslash is no
        // longer a reason to enclose, so this particular field goes out bare
        // and even a default reader survives it...
        $this->assertSame('ends\\,next' . "\n", Csv::rfcWrite([['ends\\', 'next']]));
        $this->assertSame(['ends\\', 'next'], Csv::reader('ends\\,next' . "\n")->first());

        // ...but add a delimiter to the same value and the enclosure comes
        // back, the trailing backslash is still not doubled, and a default
        // reader eats the line again.
        $this->assertSame('"a,b\\",c' . "\n", Csv::rfcWrite([['a,b\\', 'c']]));
        $this->assertSame(['a,b\\', 'c'], Csv::rfcReader('"a,b\\",c' . "\n")->first());
        $this->assertSame(['a,b\\",c' . "\n"], Csv::reader('"a,b\\",c' . "\n")->first());

        // Only one character or nothing; there is no "escape sequence" option.
        try {
            Csv::writer()->setEscape('ab');
            $this->fail('a two character escape should have been rejected');
        } catch (InvalidArgument $exception) {
            $this->assertSame(
                'League\Csv\AbstractCsv::setEscape() expects escape to be a single character or an empty string; `ab` given.',
                $exception->getMessage()
            );
        }
    }

    public function testSetHeaderOffsetAlsoChangesTheRecordKeysNotJustTheirShape(): void
    {
        $flat = iterator_to_array(Csv::reader(self::DOCUMENT)->getRecords());
        $this->assertSame([0, 1, 2], array_keys($flat));
        $this->assertSame(['id', 'name'], $flat[0]);

        $assoc = iterator_to_array(Csv::reader(self::DOCUMENT, 0)->getRecords());
        $this->assertSame(['id' => '1', 'name' => 'ada'], $assoc[1]);

        // The trap behind foreach working and $records[0] not existing: the
        // keys are document offsets, not a fresh 0-based index, so the header
        // row leaves a hole at 0 that iterator_to_array faithfully reproduces.
        $this->assertSame([1, 2], array_keys($assoc));
        $this->assertArrayNotHasKey(0, $assoc);

        // The record-oriented accessors do renumber, so nth(0) and first() are
        // the first *data* row.
        $this->assertSame(['id' => '1', 'name' => 'ada'], Csv::reader(self::DOCUMENT, 0)->nth(0));
        $this->assertSame(['id' => '1', 'name' => 'ada'], Csv::reader(self::DOCUMENT, 0)->first());
        $this->assertSame(2, Csv::reader(self::DOCUMENT, 0)->count());
        $this->assertSame(3, Csv::reader(self::DOCUMENT)->count());
    }

    public function testADuplicateHeaderIsFatalButOnlyWhenTheRecordsAreAskedFor(): void
    {
        $document = "id,name,id\n1,ada,9\n";

        // Neither setHeaderOffset() nor getHeader() objects. If your validation
        // reads the header and stops there, it passes.
        $reader = Csv::reader($document, 0);
        $this->assertSame(0, $reader->getHeaderOffset());
        $this->assertSame(['id', 'name', 'id'], $reader->getHeader());

        // getRecords() is not a generator, so the header is checked when it is
        // called rather than on the first iteration: the throw lands at the
        // call site, not inside the foreach.
        try {
            $reader->getRecords();
            $this->fail('a duplicate column name should have been rejected');
        } catch (SyntaxError $exception) {
            $this->assertSame('The header record contains duplicate column names.', $exception->getMessage());
            $this->assertSame(['id'], $exception->duplicateColumnNames());
        }

        // count() goes through the same path, so even counting the rows throws.
        $this->expectingSyntaxError(fn () => Csv::reader($document, 0)->count());

        // The mapper form is checked identically, which is easy to miss because
        // there is no header row involved at all.
        $this->expectingSyntaxError(fn () => Csv::reader("1,ada,9\n")->getRecords(['id', 'name', 'id']));

        // Without a header offset duplicates are just data.
        $this->assertSame(
            [['id', 'name', 'id'], ['1', 'ada', '9']],
            array_values(iterator_to_array(Csv::reader($document)->getRecords()))
        );
    }

    public function testAShortRowIsPaddedWithNullAndAnOverlongRowIsSilentlyTruncated(): void
    {
        $records = array_values(iterator_to_array(Csv::reader("a,b,c\n1,2\n1,2,3,4\n", 0)->getRecords()));

        // The header decides the shape in both directions. A missing field
        // becomes a present key holding null — not an absent key — so
        // isset() and array_key_exists() disagree about it.
        $this->assertSame(['a' => '1', 'b' => '2', 'c' => null], $records[0]);
        $this->assertArrayHasKey('c', $records[0]);
        $this->assertFalse(isset($records[0]['c']));

        // And a field with no header to land in is dropped without a word.
        $this->assertSame(['a' => '1', 'b' => '2', 'c' => '3'], $records[1]);
        $this->assertCount(3, $records[1]);
    }

    public function testANewlineAndAWhitespaceOnlyFieldBothSurviveTheRoundTrip(): void
    {
        // Measured, against the claim that whitespace-only fields do not
        // survive: through this library's own writer and reader they do,
        // byte for byte, and so does an embedded newline.
        $record = ["line1\nline2", ' ', '   ', '', 'x'];
        $serialized = Csv::write([$record]);

        // A field holding a plain space is enclosed even though RFC 4180 does
        // not ask for it — PHP encloses on space and tab as well as on the
        // delimiter, enclosure, escape and line endings — while a genuinely
        // empty field is written bare. That asymmetry is why CSV diffs are
        // noisy, and it is also what keeps the whitespace intact.
        $this->assertSame('"line1' . "\n" . 'line2"," ","   ",,x' . "\n", $serialized);
        $this->assertSame([$record], Csv::roundTrip([$record]));

        // The newline inside the enclosure does not become a record boundary.
        $this->assertSame(1, Csv::reader($serialized)->count());
        $this->assertCount(5, Csv::reader($serialized)->first());
    }

    public function testWhitespaceIsLostOnlyWhenSomethingElseWroteTheFile(): void
    {
        // Where whitespace actually disappears: PHP skips blanks between the
        // delimiter and an enclosure, so a leading space is discarded together
        // with both quotes while a trailing space outside the closing quote is
        // kept and appended. Exporters that pretty-print with a space after the
        // comma produce exactly this.
        $this->assertSame(['b ', 'c'], Csv::reader(' "b" ,c' . "\n")->first());
        $this->assertSame(['b', 'c'], Csv::reader(' "b",c' . "\n")->first());

        // Inside the enclosure it is data.
        $this->assertSame([' b ', 'c'], Csv::reader('" b ",c' . "\n")->first());

        // And with no enclosure anywhere nothing is trimmed at all. So two
        // spellings that look equally padded, ` "b" ` and ` b `, disagree about
        // the leading space, and only the quoted one loses it.
        $this->assertSame(['a', ' b ', 'c'], Csv::reader("a, b ,c\n")->first());
    }

    public function testABlankLineIsDroppedAndTakesItsOffsetWithIt(): void
    {
        $document = "a\n\nb\n";

        $this->assertFalse(Csv::reader($document)->isEmptyRecordsIncluded());
        $this->assertSame(2, Csv::reader($document)->count());

        // The dropped line still consumes its offset, so the keys jump. Code
        // that trusts the key as a line number for an error message reports
        // the wrong line for everything after the first blank.
        $this->assertSame([0, 2], array_keys(iterator_to_array(Csv::reader($document)->getRecords())));

        $included = iterator_to_array(Csv::reader($document)->includeEmptyRecords()->getRecords());
        $this->assertSame([0, 1, 2], array_keys($included));
        $this->assertSame([], $included[1]);
        $this->assertSame(3, Csv::reader($document)->includeEmptyRecords()->count());

        // A line holding a single space is not empty and is never dropped, so
        // "blank" here means zero bytes, not blank-looking.
        $this->assertSame(
            [['a'], [' '], ['b']],
            array_values(iterator_to_array(Csv::reader("a\n \nb\n")->getRecords()))
        );

        // With a header offset the hole and the header both come off the keys.
        $this->assertSame([2], array_keys(iterator_to_array(Csv::reader("id,name\n\n1,ada\n", 0)->getRecords())));
    }

    public function testTheEncloseMethodsAreGettersAndTheSettersHaveDifferentNames(): void
    {
        $writer = Csv::writer();

        // encloseAll()/encloseNecessary()/encloseNone() answer a question. The
        // setters are forceEnclosure()/necessaryEnclosure()/noEnclosure().
        $this->assertTrue($writer->encloseNecessary());
        $this->assertFalse($writer->encloseAll());

        // So this line, which looks like configuration and passes review,
        // returns a bool and does nothing.
        $writer->encloseAll();
        $writer->insertOne(['a', 'b']);
        $this->assertSame("a,b\n", $writer->toString());

        $forced = Csv::writer()->forceEnclosure();
        $forced->insertOne(['a', 'b"c', '', ' ']);
        $this->assertSame('"a","b""c",""," "' . "\n", $forced->toString());
        $this->assertTrue($forced->encloseAll());

        // noEnclosure() is the one to be careful with: it does not validate,
        // it just stops quoting. A field holding the delimiter becomes two
        // fields on the way back and nothing throws.
        $none = Csv::writer()->noEnclosure();
        $none->insertOne(['a,b', 'c']);
        $this->assertSame("a,b,c\n", $none->toString());
        $this->assertSame(['a', 'b', 'c'], Csv::reader($none->toString())->first());

        // A field holding a newline becomes two records the same way.
        $split = Csv::writer()->noEnclosure();
        $split->insertOne(["a\nb", 'c']);
        $this->assertSame("a\nb,c\n", $split->toString());
        $this->assertSame(
            [['a'], ['b', 'c']],
            array_values(iterator_to_array(Csv::reader($split->toString())->getRecords()))
        );
    }

    public function testCreateFromStringStillWorksButNowRaisesADeprecation(): void
    {
        // Every BOM and enclosure snippet written before 9.27 opens with
        // createFromString(). It is not a silent alias: the #[Deprecated]
        // attribute raises on every call, which a test suite configured to
        // fail on deprecations will stop on long before the method is removed.
        $captured = [];
        set_error_handler(function (int $severity, string $message) use (&$captured): bool {
            $captured[] = [$severity, $message];

            return true;
        });

        try {
            $reader = Reader::createFromString("a,b\n");
        } finally {
            restore_error_handler();
        }

        $this->assertSame(['a', 'b'], $reader->first());
        $this->assertCount(1, $captured);
        $this->assertSame(E_USER_DEPRECATED, $captured[0][0]);
        $this->assertSame(
            'Method League\Csv\AbstractCsv::createFromString() is deprecated since league/csv:9.27.0, use League\Csv\AbstractCsv::fromString() instead',
            $captured[0][1]
        );

        // The replacement carries no such attribute; createFromPath() carries
        // the matching one, so the file-based factory moved at the same time.
        $this->assertSame([], (new ReflectionMethod(Reader::class, 'fromString'))->getAttributes(Deprecated::class));
        $this->assertCount(1, (new ReflectionMethod(Reader::class, 'createFromPath'))->getAttributes(Deprecated::class));
    }

    /** @param callable():mixed $call */
    private function expectingSyntaxError(callable $call): void
    {
        try {
            $call();
            $this->fail('a duplicate column name should have been rejected');
        } catch (SyntaxError $exception) {
            $this->assertSame('The header record contains duplicate column names.', $exception->getMessage());
        }
    }
}
