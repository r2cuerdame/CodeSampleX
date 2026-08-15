<?php

namespace Csx;

use Doctrine\DBAL\ArrayParameterType;
use Doctrine\DBAL\Connection;
use Doctrine\DBAL\DriverManager;
use Doctrine\DBAL\ParameterType;
use Doctrine\DBAL\Result;
use Doctrine\DBAL\Schema\Schema;
use Doctrine\DBAL\Schema\Table;
use Doctrine\DBAL\Types\Type;
use Doctrine\DBAL\Types\Types;

/**
 * Doctrine DBAL 4 against an in-memory SQLite, and the five places where it
 * does something other than what the call site says. All five were measured on
 * dbal 4.4.4 / PHP 8.5.9; where a measurement contradicted the documentation
 * the measurement is what is written down here.
 *
 * 1. Positional parameters are bound in the order the array was BUILT, not by
 *    its keys. Connection::expandArrayParameters only parses the SQL when
 *    `is_string(key($params))` is true or some type is an ArrayParameterType.
 *    Otherwise nothing ever looks at a key: bindParameters() walks the array in
 *    insertion order and binds to 1, 2, 3... So [0 => 2, 1 => 20] and
 *    [1 => 20, 0 => 2] are the same array to array_key_exists and to ksort, and
 *    a different query to DBAL — the second one silently matches no rows. Keys
 *    being ignored is also why a 1-based array, or [9 => .., 4 => ..], "works".
 *    Named parameters go through the parser and are resolved by name, so they
 *    are immune. Bind by name.
 *
 * 2. The docs say "You cannot mix the positional and the named approach."
 *    Measured on 4.4.4, you can, and that is worse than not being able to.
 *    When the first key is a string the parser runs, rewrites every placeholder
 *    (named and positional alike) to `?`, and resolves each one properly — so
 *    `id = :id AND qty = ?` with ['id' => 2, 0 => 20] returns the right row.
 *    Build the same array int-key-first and the parser never runs, the values
 *    are bound in insertion order against SQLite's own placeholder numbering,
 *    and you get a different answer from the same statement and the same
 *    values. Same switch decides whether a missing parameter is caught:
 *    parser on, you get MissingNamedParameter or MissingPositionalParameter;
 *    parser off, you get either the wrong row or SQLSTATE HY000 "column index
 *    out of range" from the driver.
 *
 * 3. fetchAssociative() and fetchOne() return FALSE on exhaustion, not null.
 *    `$row ?? $default` therefore never fires, and `if (!$value)` cannot tell a
 *    missing row from a stored 0 — fetchOne() returns int 0 for a real zero and
 *    bool false for no row, which are == but not ===. firstRow() below is the
 *    one-line bridge; write it once instead of at every call site.
 *
 * 4. A wrong ParameterType does not fail, it rewrites the value. Bound with
 *    ParameterType::INTEGER, '0012' is stored as '12' (leading zeros gone),
 *    'abc' becomes 0, and 2.9 becomes 2. Bound with ParameterType::STRING,
 *    false becomes '' rather than '0'. Nothing warns. It hides on reads,
 *    because SQLite applies column affinity when comparing against an INTEGER
 *    column, so `WHERE id = ?` matches whichever type you declared; it only
 *    bites on writes and on TEXT columns.
 *
 * 5. An IN clause needs createNamedParameter() plus ArrayParameterType, and
 *    each way of getting it wrong fails differently:
 *      - expr()->in('id', [1, 3]) interpolates the values into the SQL. It
 *        returns rows, so it looks correct — it is string concatenation, and
 *        with strings it emits `name IN (alpha, gamma)` and dies on "no such
 *        column: alpha". Never pass user input to it.
 *      - setParameter('ids', [1, 3]) with the default STRING type raises a PHP
 *        "Array to string conversion" warning, runs, and matches nothing.
 *      - createNamedParameter([1, 3], ParameterType::INTEGER) casts the array
 *        to int 1 with no diagnostic at all and silently queries IN (1).
 *    Only ArrayParameterType makes ExpandArrayParameters expand the placeholder
 *    into one `?` per element. An empty list becomes the literal NULL, so
 *    IN (NULL) matches nothing instead of being a syntax error.
 *
 * 6. Schema introspection does not give back the type it was given.
 *    datetime_immutable comes back as datetime, json and simple_array as text,
 *    guid as string(36) fixed. SQLite stores a declared type name, not a
 *    Doctrine type, and SQLitePlatform's mapping is many-to-one.
 *    The part that surprises: the Comparator says the two schemas are
 *    IDENTICAL and generates no ALTER, because it compares the generated DDL
 *    and both sides emit `created DATETIME NOT NULL`. So there is no phantom
 *    migration to warn you. The loss only appears when something uses the
 *    introspected type to convert values: convertToPHPValue then hands back a
 *    mutable DateTime instead of a DateTimeImmutable, and the raw string
 *    '{"a":1}' instead of the decoded array. A tool that introspects the
 *    database to build its own type map is the one that gets hurt.
 *    Introspection also throws outright on a declared type it has no mapping
 *    for: `MONEY` raises InvalidArgumentException, not a fallback to string.
 */
final class Ledger
{
    public const TABLE = 'orders';

    /**
     * ':memory:' is scoped to the connection, so every Connection built here
     * gets its own empty database and tests cannot leak into each other.
     */
    public static function connect(): Connection
    {
        return DriverManager::getConnection(['driver' => 'pdo_sqlite', 'memory' => true]);
    }

    /**
     * Builds the table through the Schema API and returns the Table that was
     * intended, so a caller can hold it next to the one introspection gives
     * back. The four nullable columns exist to be round-tripped.
     */
    public static function migrate(Connection $conn): Table
    {
        $schema = new Schema();
        $table = $schema->createTable(self::TABLE);
        $table->addColumn('id', Types::INTEGER, ['autoincrement' => true]);
        $table->addColumn('reference', Types::STRING, ['length' => 32]);
        $table->addColumn('quantity', Types::INTEGER);
        $table->addColumn('placed_at', Types::DATETIME_IMMUTABLE, ['notnull' => false]);
        $table->addColumn('metadata', Types::JSON, ['notnull' => false]);
        $table->addColumn('tags', Types::SIMPLE_ARRAY, ['notnull' => false]);
        $table->addColumn('external_id', Types::GUID, ['notnull' => false]);
        $table->addColumn('amount', Types::DECIMAL, ['precision' => 10, 'scale' => 2, 'notnull' => false]);
        $table->setPrimaryKey(['id']);

        foreach ($conn->getDatabasePlatform()->getCreateTableSQL($table) as $ddl) {
            $conn->executeStatement($ddl);
        }

        return $table;
    }

    /**
     * Named parameters and an explicit type for every one of them. Because the
     * keys are strings the SQL parser runs, which is what makes the argument
     * order here irrelevant and a typo in a placeholder name an exception
     * rather than a wrong answer.
     */
    public static function insert(Connection $conn, string $reference, int $quantity): void
    {
        $conn->executeStatement(
            'INSERT INTO ' . self::TABLE . ' (reference, quantity) VALUES (:reference, :quantity)',
            ['reference' => $reference, 'quantity' => $quantity],
            ['reference' => ParameterType::STRING, 'quantity' => ParameterType::INTEGER],
        );
    }

    /**
     * The false-to-null bridge. fetchAssociative() returns false when the
     * result is exhausted, so this is the only place in the codebase that has
     * to know that, and callers can use ?-> and ?? on the result.
     *
     * @return array<string, mixed>|null
     */
    public static function firstRow(Result $result): ?array
    {
        $row = $result->fetchAssociative();

        return $row === false ? null : $row;
    }

    /** @return array<string, mixed>|null */
    public static function findByReference(Connection $conn, string $reference): ?array
    {
        return self::firstRow($conn->executeQuery(
            'SELECT id, reference, quantity FROM ' . self::TABLE . ' WHERE reference = :reference',
            ['reference' => $reference],
            ['reference' => ParameterType::STRING],
        ));
    }

    /**
     * The correct IN clause: createNamedParameter puts a single :dcValueN into
     * the SQL, and ArrayParameterType is what makes ExpandArrayParameters turn
     * that one placeholder into one `?` per element at execute time. getSQL()
     * still shows the unexpanded form; the expansion happens inside
     * Connection::executeQuery.
     *
     * @param list<int> $ids
     *
     * @return list<string>
     */
    public static function referencesForIds(Connection $conn, array $ids): array
    {
        $qb = $conn->createQueryBuilder();
        $qb->select('reference')
            ->from(self::TABLE)
            ->where($qb->expr()->in('id', $qb->createNamedParameter($ids, ArrayParameterType::INTEGER)))
            ->orderBy('id');

        return $qb->executeQuery()->fetchFirstColumn();
    }

    /**
     * Kept because it is what people write and it is not a query builder at
     * all: expr()->in() implodes whatever array it is handed straight into the
     * SQL string. With ints it returns the right rows, which is exactly why it
     * survives review.
     *
     * @param list<int|string> $values
     *
     * @return list<int>
     */
    public static function idsForInlinedIn(Connection $conn, string $column, array $values): array
    {
        $qb = $conn->createQueryBuilder();
        $qb->select('id')->from(self::TABLE)->where($qb->expr()->in($column, $values))->orderBy('id');

        return $qb->executeQuery()->fetchFirstColumn();
    }

    /**
     * Doctrine type names as the database reports them back.
     *
     * @return array<string, string>
     */
    public static function introspectedTypeNames(Connection $conn): array
    {
        return self::typeNames($conn->createSchemaManager()->introspectTable(self::TABLE));
    }

    /**
     * Doctrine type names as they were declared.
     *
     * @return array<string, string>
     */
    public static function typeNames(Table $table): array
    {
        $names = [];
        foreach ($table->getColumns() as $column) {
            // Type::getName() is gone in DBAL 4; lookupName() asks the registry.
            $names[$column->getName()] = Type::lookupName($column->getType());
        }

        return $names;
    }
}
