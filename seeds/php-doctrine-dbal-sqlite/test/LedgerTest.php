<?php

use Csx\Ledger;
use Doctrine\DBAL\ArrayParameterType;
use Doctrine\DBAL\ArrayParameters\Exception\MissingNamedParameter;
use Doctrine\DBAL\ArrayParameters\Exception\MissingPositionalParameter;
use Doctrine\DBAL\Connection;
use Doctrine\DBAL\Exception\DriverException;
use Doctrine\DBAL\Exception\InvalidArgumentException;
use Doctrine\DBAL\ParameterType;
use Doctrine\DBAL\Schema\Schema;
use Doctrine\DBAL\Types\Type;
use PHPUnit\Framework\TestCase;

final class LedgerTest extends TestCase
{
    private Connection $conn;

    protected function setUp(): void
    {
        $this->conn = Ledger::connect();
        Ledger::migrate($this->conn);
        Ledger::insert($this->conn, 'A-1', 10);
        Ledger::insert($this->conn, 'B-2', 20);
        Ledger::insert($this->conn, 'C-3', 30);
    }

    /**
     * Runs $fn with PHP diagnostics captured instead of raised, so the test can
     * assert on a warning that would otherwise be invisible.
     *
     * @return array{0: list<string>, 1: mixed}
     */
    private function capturingDiagnostics(callable $fn): array
    {
        $seen = [];
        set_error_handler(static function (int $number, string $message) use (&$seen): bool {
            $seen[] = $message;

            return true;
        });

        try {
            $value = $fn();
        } finally {
            restore_error_handler();
        }

        return [$seen, $value];
    }

    public function testPositionalParametersAreBoundInInsertionOrderAndTheKeysAreIgnored(): void
    {
        $sql = 'SELECT reference FROM orders WHERE id = ? AND quantity = ?';

        // The same two key/value pairs, assembled in two different orders. To
        // PHP these arrays are equal; to DBAL they are different queries.
        $built = [];
        $built[0] = 2;
        $built[1] = 20;

        $reordered = [];
        $reordered[1] = 20;
        $reordered[0] = 2;

        // PHP's == ignores key order, so the two arrays are equal by the
        // comparison most code would use; only === and array_keys() can see the
        // difference, and neither is something you would think to check.
        $this->assertTrue($built == $reordered);
        $this->assertFalse($built === $reordered);
        $this->assertSame([0, 1], array_keys($built));
        $this->assertSame([1, 0], array_keys($reordered));

        $this->assertSame('B-2', $this->conn->executeQuery($sql, $built)->fetchOne());

        // Measured: no row, no warning, no exception. Connection::bindParameters
        // walks the array in insertion order and binds to 1, 2, 3 — it never
        // reads a key. Here that means id = 20 AND quantity = 2.
        $this->assertFalse($this->conn->executeQuery($sql, $reordered)->fetchOne());

        // ksort is enough to fix it, which is also the proof that order, not
        // content, was the difference.
        ksort($reordered);
        $this->assertSame('B-2', $this->conn->executeQuery($sql, $reordered)->fetchOne());

        // Keys being ignored is also why these two "work". A 1-based array is
        // not being matched to PDO's 1-based positions; nothing is matching.
        $this->assertSame('B-2', $this->conn->executeQuery($sql, [1 => 2, 2 => 20])->fetchOne());
        $this->assertSame('B-2', $this->conn->executeQuery($sql, [9 => 2, 4 => 20])->fetchOne());

        // Named parameters go through the SQL parser and are resolved by name,
        // so the order they were set in cannot matter.
        $named = 'SELECT reference FROM orders WHERE id = :id AND quantity = :quantity';
        $this->assertSame('B-2', $this->conn->executeQuery($named, ['quantity' => 20, 'id' => 2])->fetchOne());
        $this->assertSame('B-2', $this->conn->executeQuery($named, ['id' => 2, 'quantity' => 20])->fetchOne());
    }

    public function testMixingNamedAndPositionalPlaceholdersWorksAndTheFirstArrayKeyDecides(): void
    {
        // The 4.4 manual says "You cannot mix the positional and the named
        // approach." Measured on 4.4.4, you can — and that is worse than the
        // documented refusal, because whether it is handled properly depends on
        // something invisible at the call site.
        //
        // Connection::expandArrayParameters only parses the SQL when
        // is_string(key($params)) is true, or when some type is an
        // ArrayParameterType. When it parses, ExpandArrayParameters rewrites
        // every placeholder — named and positional alike — into `?` and
        // resolves each from its own name or index.
        $sql = 'SELECT reference FROM orders WHERE id = :id AND quantity = ?';

        $stringKeyFirst = ['id' => 2, 0 => 20];
        $this->assertSame('B-2', $this->conn->executeQuery($sql, $stringKeyFirst)->fetchOne());

        // Identical values, identical SQL, the array assembled the other way
        // round. The parser never runs, so the values are bound by insertion
        // order against SQLite's own placeholder numbering (:id is 1, ? is 2):
        // id = 20 AND quantity = 2. No row, and nothing said so.
        $intKeyFirst = [0 => 20, 'id' => 2];
        $this->assertFalse($this->conn->executeQuery($sql, $intKeyFirst)->fetchOne());

        // Proof that the parsing path really resolves by name rather than by
        // order: here the positional placeholder comes FIRST in the SQL while
        // the named value is first in the array, and the answer is still right.
        $this->assertSame(
            'B-2',
            $this->conn->executeQuery(
                'SELECT reference FROM orders WHERE quantity = ? AND id = :id',
                ['id' => 2, 0 => 20],
            )->fetchOne(),
        );
    }

    public function testTheSameSwitchDecidesWhetherABadParameterListIsCaught(): void
    {
        // Parser on: a placeholder with no value is named precisely.
        try {
            $this->conn->executeQuery('SELECT reference FROM orders WHERE id = :id', ['Id' => 2]);
            $this->fail('a misspelled placeholder should have been rejected');
        } catch (MissingNamedParameter $exception) {
            $this->assertSame('Named parameter "id" does not have a bound value.', $exception->getMessage());
        }

        try {
            $this->conn->executeQuery('SELECT reference FROM orders WHERE id = ?', ['id' => 2]);
            $this->fail('a positional placeholder with a named array should have been rejected');
        } catch (MissingPositionalParameter $exception) {
            $this->assertSame('Positional parameter at index 0 does not have a bound value.', $exception->getMessage());
        }

        // Parser on: a surplus named value is simply unused.
        $this->assertSame(
            'B-2',
            $this->conn->executeQuery(
                'SELECT reference FROM orders WHERE id = :id',
                ['id' => 2, 'unused' => 9],
            )->fetchOne(),
        );

        // Parser off: the same surplus is a driver error rather than a DBAL
        // one, and it names nothing you wrote.
        try {
            $this->conn->executeQuery('SELECT reference FROM orders WHERE id = ?', [2, 99, 100]);
            $this->fail('surplus positional values should have reached the driver');
        } catch (DriverException $exception) {
            $this->assertStringContainsString('column index out of range', $exception->getMessage());
        }
    }

    public function testFetchAssociativeReturnsFalseNotNullWhenTheResultIsExhausted(): void
    {
        $result = $this->conn->executeQuery('SELECT * FROM orders WHERE id = 999');
        $row = $result->fetchAssociative();

        $this->assertFalse($row);
        $this->assertNotNull($row);
        $this->assertFalse(is_array($row));

        // So the habitual null guards are dead code. ?? only fires on null.
        $this->assertFalse($row ?? ['fallback' => true]);

        // Which is what firstRow() is for: one place knows about the false.
        $this->assertNull(Ledger::firstRow($this->conn->executeQuery('SELECT * FROM orders WHERE id = 999')));
        $this->assertSame('B-2', Ledger::findByReference($this->conn, 'B-2')['reference']);
        $this->assertNull(Ledger::findByReference($this->conn, 'nope'));

        // A Result that did return a row returns false from then on, so the
        // `while ($row = $r->fetchAssociative())` idiom does terminate.
        $one = $this->conn->executeQuery('SELECT * FROM orders WHERE id = 1');
        $this->assertIsArray($one->fetchAssociative());
        $this->assertFalse($one->fetchAssociative());

        // fetchOne is the dangerous one: false for "no row", int 0 for a row
        // holding zero. They are ==, so only === separates them.
        $this->conn->executeStatement("INSERT INTO orders (reference, quantity) VALUES ('D-4', 0)");
        $missing = $this->conn->executeQuery('SELECT quantity FROM orders WHERE reference = ?', ['nope'])->fetchOne();
        $zero = $this->conn->executeQuery('SELECT quantity FROM orders WHERE reference = ?', ['D-4'])->fetchOne();
        $this->assertFalse($missing);
        $this->assertSame(0, $zero);
        $this->assertTrue($missing == $zero);
        $this->assertFalse($missing === $zero);

        // fetchAllAssociative gives an empty array instead, so the two APIs
        // disagree about how "nothing" is spelled.
        $this->assertSame([], $this->conn->executeQuery('SELECT * FROM orders WHERE id = 999')->fetchAllAssociative());

        // And rowCount is not a row count for a SELECT on pdo_sqlite: it
        // reports 0 for four rows. Count in SQL, not in PHP.
        $this->assertSame(4, (int) $this->conn->executeQuery('SELECT COUNT(*) FROM orders')->fetchOne());
        $this->assertSame(0, $this->conn->executeQuery('SELECT * FROM orders')->rowCount());
    }

    public function testTheWrongParameterTypeRewritesTheValueInsteadOfFailing(): void
    {
        // The write path is where it costs you. reference is VARCHAR(32); bound
        // as an INTEGER the value is converted before SQLite ever sees it, and
        // the stored string has lost its leading zeros. No warning, no error.
        $this->conn->executeStatement(
            'INSERT INTO orders (reference, quantity) VALUES (:reference, :quantity)',
            ['reference' => '0012', 'quantity' => 1],
            ['reference' => ParameterType::INTEGER, 'quantity' => ParameterType::INTEGER],
        );
        $this->conn->executeStatement(
            'INSERT INTO orders (reference, quantity) VALUES (:reference, :quantity)',
            ['reference' => '0012', 'quantity' => 2],
            ['reference' => ParameterType::STRING, 'quantity' => ParameterType::INTEGER],
        );

        $stored = $this->conn->executeQuery(
            'SELECT reference FROM orders WHERE quantity IN (1, 2) ORDER BY quantity',
        )->fetchFirstColumn();
        $this->assertSame(['12', '0012'], $stored);

        // What the driver actually handed SQLite, straight from the engine.
        $this->assertSame('integer', $this->conn->executeQuery('SELECT typeof(?)', ['2'], [ParameterType::INTEGER])->fetchOne());
        $this->assertSame('text', $this->conn->executeQuery('SELECT typeof(?)', ['2'], [ParameterType::STRING])->fetchOne());

        // Non-numeric text bound as an integer is 0, not an error.
        $this->assertSame(0, $this->conn->executeQuery('SELECT ?', ['abc'], [ParameterType::INTEGER])->fetchOne());
        // A float bound as an integer is truncated, not rounded and not refused.
        $this->assertSame(2, $this->conn->executeQuery('SELECT ?', [2.9], [ParameterType::INTEGER])->fetchOne());
        $this->assertSame('2.9', $this->conn->executeQuery('SELECT ?', [2.9], [ParameterType::STRING])->fetchOne());
        // false bound as a string is the empty string, not '0'.
        $this->assertSame('', $this->conn->executeQuery('SELECT ?', [false], [ParameterType::STRING])->fetchOne());
        $this->assertSame(1, $this->conn->executeQuery('SELECT ?', [true], [ParameterType::INTEGER])->fetchOne());
        // null outranks the declared type.
        $this->assertSame('null', $this->conn->executeQuery('SELECT typeof(?)', [null], [ParameterType::INTEGER])->fetchOne());

        // And this is why it survives in a codebase: on the read path SQLite
        // applies the column's affinity to the comparison, so an INTEGER column
        // matches whichever of the two types you declared, right or wrong.
        $this->assertSame('B-2', $this->conn->executeQuery('SELECT reference FROM orders WHERE id = ?', ['2'], [ParameterType::STRING])->fetchOne());
        $this->assertSame('B-2', $this->conn->executeQuery('SELECT reference FROM orders WHERE id = ?', ['2'], [ParameterType::INTEGER])->fetchOne());
        // Against a text column the same mistake matches nothing, because
        // 'A-1' became 0 before the comparison.
        $this->assertFalse($this->conn->executeQuery('SELECT id FROM orders WHERE reference = ?', ['A-1'], [ParameterType::INTEGER])->fetchOne());
    }

    public function testAnInClauseNeedsCreateNamedParameterAndArrayParameterType(): void
    {
        // The one that works.
        $this->assertSame(['A-1', 'C-3'], Ledger::referencesForIds($this->conn, [1, 3]));

        // getSQL() shows one placeholder; the expansion into one `?` per
        // element happens inside Connection::executeQuery, not in the builder.
        $qb = $this->conn->createQueryBuilder();
        $qb->select('reference')
            ->from('orders')
            ->where($qb->expr()->in('id', $qb->createNamedParameter([1, 3], ArrayParameterType::INTEGER)));
        $this->assertSame('SELECT reference FROM orders WHERE id IN (:dcValue1)', $qb->getSQL());
        $this->assertSame(['dcValue1' => [1, 3]], $qb->getParameters());

        // An empty list becomes the literal NULL rather than a syntax error, so
        // IN () never happens and the query matches nothing.
        $this->assertSame([], Ledger::referencesForIds($this->conn, []));

        // Failure 1: expr()->in() with raw values is string interpolation. With
        // integers it returns the right rows, which is how it gets shipped.
        $this->assertSame([1, 3], Ledger::idsForInlinedIn($this->conn, 'id', [1, 3]));
        // With strings the same call emits `reference IN (A-1, C-3)` and SQLite
        // reads the bare words as column names.
        try {
            Ledger::idsForInlinedIn($this->conn, 'reference', ['A-1', 'C-3']);
            $this->fail('unquoted values should have been read as identifiers');
        } catch (DriverException $exception) {
            $this->assertStringContainsString('no such column', $exception->getMessage());
        }

        // Failure 2: setParameter with an array and the default STRING type.
        // The array is converted to the string "Array" one level down, in
        // PDO\Statement::bindValue. It is a PHP warning, not an exception, so
        // in production it lands in a log nobody reads and the query returns
        // nothing.
        $qb = $this->conn->createQueryBuilder();
        $qb->select('reference')->from('orders')->where($qb->expr()->in('id', ':ids'))->setParameter('ids', [1, 3]);
        [$diagnostics, $rows] = $this->capturingDiagnostics(static fn () => $qb->executeQuery()->fetchFirstColumn());
        $this->assertSame(['Array to string conversion'], $diagnostics);
        $this->assertSame([], $rows);

        // Failure 3: the array is there but the type is scalar. This one is
        // completely silent — no diagnostic at all — because casting an array
        // to int yields 1, so the query really asked for IN (1).
        $qb = $this->conn->createQueryBuilder();
        $qb->select('reference')
            ->from('orders')
            ->where($qb->expr()->in('id', $qb->createNamedParameter([1, 3], ParameterType::INTEGER)));
        [$diagnostics, $rows] = $this->capturingDiagnostics(static fn () => $qb->executeQuery()->fetchFirstColumn());
        $this->assertSame([], $diagnostics);
        $this->assertSame(['A-1'], $rows);

        // setParameter is also the wrong tool in a quieter way: it binds a
        // value but never adds a placeholder, so a parameter whose placeholder
        // is missing from the SQL is discarded without complaint.
        $qb = $this->conn->createQueryBuilder();
        $qb->select('reference')->from('orders')->where('id = 2')->setParameter('id', 999);
        $this->assertSame('B-2', $qb->executeQuery()->fetchOne());

        // ArrayParameterType works through setParameter too, and on
        // Connection::executeQuery directly.
        $qb = $this->conn->createQueryBuilder();
        $qb->select('reference')
            ->from('orders')
            ->where($qb->expr()->in('id', ':ids'))
            ->setParameter('ids', [1, 3], ArrayParameterType::INTEGER);
        $this->assertSame(['A-1', 'C-3'], $qb->executeQuery()->fetchFirstColumn());
        $this->assertSame(
            [1, 3],
            $this->conn->executeQuery(
                'SELECT id FROM orders WHERE reference IN (:refs) ORDER BY id',
                ['refs' => ['A-1', 'C-3']],
                ['refs' => ArrayParameterType::STRING],
            )->fetchFirstColumn(),
        );

        // createNamedParameter allocates a fresh :dcValueN every call, so two
        // parameters never collide and the counter is per builder.
        $qb = $this->conn->createQueryBuilder();
        $this->assertSame(':dcValue1', $qb->createNamedParameter(1));
        $this->assertSame(':dcValue2', $qb->createNamedParameter(2));
        $this->assertSame(['dcValue1' => 1, 'dcValue2' => 2], $qb->getParameters());
    }

    public function testIntrospectionLosesTheDoctrineTypeWhileTheComparatorSeesNoDifference(): void
    {
        $declared = Ledger::migrate(Ledger::connect());
        $introspected = $this->conn->createSchemaManager()->introspectTable('orders');

        // SQLite stores a declared type name, not a Doctrine type, and
        // SQLitePlatform's name mapping is many-to-one. Four of these eight
        // columns come back as a different Doctrine type than they went in as.
        $this->assertSame([
            'id' => 'integer',
            'reference' => 'string',
            'quantity' => 'integer',
            'placed_at' => 'datetime_immutable',
            'metadata' => 'json',
            'tags' => 'simple_array',
            'external_id' => 'guid',
            'amount' => 'decimal',
        ], Ledger::typeNames($declared));

        $this->assertSame([
            'id' => 'integer',
            'reference' => 'string',
            'quantity' => 'integer',
            'placed_at' => 'datetime',
            'metadata' => 'text',
            'tags' => 'text',
            'external_id' => 'string',
            'amount' => 'decimal',
        ], Ledger::introspectedTypeNames($this->conn));

        // Length, precision, scale and autoincrement do survive; guid picks up
        // a length and fixed flag it never had, because it was written as
        // CHAR(36) and read back as one.
        $this->assertSame(32, $introspected->getColumn('reference')->getLength());
        $this->assertSame(10, $introspected->getColumn('amount')->getPrecision());
        $this->assertSame(2, $introspected->getColumn('amount')->getScale());
        $this->assertTrue($introspected->getColumn('id')->getAutoincrement());
        $this->assertNull($declared->getColumn('external_id')->getLength());
        $this->assertSame(36, $introspected->getColumn('external_id')->getLength());
        $this->assertFalse($declared->getColumn('external_id')->getFixed());
        $this->assertTrue($introspected->getColumn('external_id')->getFixed());

        // The measurement that contradicts the obvious guess: this does NOT
        // produce a phantom migration. The Comparator calls the two schemas
        // identical and emits no ALTER, because it compares the DDL a column
        // generates rather than its Doctrine type — and both sides emit the
        // same DATETIME.
        $platform = $this->conn->getDatabasePlatform();
        $diff = $this->conn->createSchemaManager()->createComparator()->compareSchemas(
            new Schema([$introspected]),
            new Schema([$declared]),
        );
        $this->assertTrue($diff->isEmpty());
        $this->assertSame([], $platform->getAlterSchemaSQL($diff));
        $this->assertSame(
            $platform->getColumnDeclarationSQL('placed_at', $declared->getColumn('placed_at')->toArray()),
            $platform->getColumnDeclarationSQL('placed_at', $introspected->getColumn('placed_at')->toArray()),
        );

        // So nothing flags it, and the loss only shows up where the type is
        // used to convert a value. This is what breaks a tool that introspects
        // the database to build its own type map.
        $raw = '2024-03-31 12:00:00';
        $this->assertInstanceOf(
            DateTimeImmutable::class,
            $declared->getColumn('placed_at')->getType()->convertToPHPValue($raw, $platform),
        );
        $mutable = $introspected->getColumn('placed_at')->getType()->convertToPHPValue($raw, $platform);
        $this->assertInstanceOf(DateTime::class, $mutable);
        $this->assertNotInstanceOf(DateTimeImmutable::class, $mutable);

        $this->assertSame(
            ['a' => 1],
            $declared->getColumn('metadata')->getType()->convertToPHPValue('{"a":1}', $platform),
        );
        $this->assertSame(
            '{"a":1}',
            $introspected->getColumn('metadata')->getType()->convertToPHPValue('{"a":1}', $platform),
        );

        $this->assertSame(
            ['a', 'b', 'c'],
            $declared->getColumn('tags')->getType()->convertToPHPValue('a,b,c', $platform),
        );
        $this->assertSame(
            'a,b,c',
            $introspected->getColumn('tags')->getType()->convertToPHPValue('a,b,c', $platform),
        );

        // Introspection has no fallback for a declared type it does not know.
        // SQLite accepts any word as a type; DBAL refuses to read it back.
        $this->conn->executeStatement('CREATE TABLE ledger_extra (v MONEY)');
        try {
            $this->conn->createSchemaManager()->introspectTable('ledger_extra');
            $this->fail('an unmapped declared type should not have been readable');
        } catch (InvalidArgumentException $exception) {
            $this->assertStringContainsString('Unknown database type "money" requested', $exception->getMessage());
        }

        // Type::getName() is gone in DBAL 4 — lookupName() on the registry is
        // the replacement, and it is a static taking the type as an argument.
        $this->assertFalse(method_exists(Type::class, 'getName'));
        $this->assertSame('datetime', Type::lookupName($introspected->getColumn('placed_at')->getType()));
    }
}
