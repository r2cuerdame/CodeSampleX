package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	cgosqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"codesamplex.dev/sample/gogormsqlite/src"
)

func main() {
	// The environment, taken from the compiler and the filesystem rather than
	// from an environment variable that may have been changed after the build.
	check(!store.CgoEnabled, "built with cgo, so this seed is not testing what it claims to")
	check(store.CCompiler() == "", "found a C compiler at %q, so cgo was avoidable here", store.CCompiler())

	// The trap, measured rather than assumed. gorm.io/driver/sqlite wraps the
	// cgo package mattn/go-sqlite3, and the expectation is that it cannot be
	// built with cgo off. It can: mattn compiles a stub, and this binary is
	// the evidence, because it imports the driver above and links. The stub
	// registers "sqlite3" with database/sql just like the real thing, so the
	// name being present proves nothing about the driver working.
	drivers := sql.Drivers()
	check(slices.Contains(drivers, "sqlite3"),
		"the cgo driver's stub should still register sqlite3, got %v", drivers)
	check(slices.Contains(drivers, "sqlite"),
		"expected the pure-Go engine registered as sqlite, got %v", drivers)

	// It fails only when something connects, and this is the message people
	// paste into a search box.
	silent := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	_, err := gorm.Open(cgosqlite.Open(":memory:"), silent)
	check(err != nil, "the cgo driver must not open a database with cgo off")
	check(strings.Contains(err.Error(), "CGO_ENABLED=0") &&
		strings.Contains(err.Error(), "requires cgo to work"),
		"expected go-sqlite3's stub error, got %q", err.Error())

	// database/sql defers it one step further than that: sql.Open only records
	// the driver name, so nothing is wrong until Ping opens a connection.
	stub, err := sql.Open("sqlite3", ":memory:")
	check(err == nil, "sql.Open does not connect and so cannot report the stub, got %v", err)
	defer stub.Close()
	pingErr := stub.Ping()
	check(pingErr != nil && strings.Contains(pingErr.Error(), "This is a stub"),
		"expected Ping to surface the stub, got %v", pingErr)

	// The working engine is in this same binary, reachable only under the name
	// "sqlite". A snippet copied from a mattn answer says "sqlite3" and lands
	// on the stub above instead.
	db, err := store.Open(":memory:")
	must(err, "open")

	// The engine is whatever modernc.org/sqlite was pinned to, and minimal
	// version selection would have picked a much older one. Asserting it
	// makes the indirect pin in go.mod a fact rather than a preference.
	var engine string
	must(db.Raw("select sqlite_version()").Scan(&engine).Error, "sqlite_version")
	check(engine == "3.53.3", "expected the pinned engine, got %q", engine)

	// AutoMigrate creates the table and can be run again on an unchanged
	// model without error. It is DDL reconciliation rather than a migration
	// history: it adds what is missing, so running it twice is not a second
	// migration to be guarded against.
	migrator := db.Migrator()
	check(!migrator.HasTable(&store.User{}), "users existed before AutoMigrate")
	must(db.AutoMigrate(&store.User{}), "AutoMigrate")
	check(migrator.HasTable(&store.User{}), "AutoMigrate did not create users")
	check(migrator.HasColumn(&store.User{}, "deleted_at"), "no deleted_at column")
	must(db.AutoMigrate(&store.User{}), "second AutoMigrate, which should be a no-op")

	// Create fills the auto-increment primary key back into the struct.
	u := store.User{Name: "ada", Count: 7, Active: true}
	must(db.Create(&u).Error, "create")
	check(u.ID != 0, "Create should have written the primary key back")

	// The single most reported GORM surprise. Updates with a STRUCT skips
	// every field holding its zero value, so this writes the name and
	// silently drops Active=false and Count=0. No error, RowsAffected 1 —
	// there is nothing in the result to tell you two thirds of the update
	// went missing.
	res := db.Model(&u).Updates(store.User{Name: "ada-renamed", Active: false, Count: 0})
	must(res.Error, "struct Updates")
	check(res.RowsAffected == 1, "struct Updates touched %d rows", res.RowsAffected)
	got := reload(db, u.ID)
	check(got.Name == "ada-renamed", "the non-zero field should have been written, got %q", got.Name)
	check(got.Active, "struct Updates must not write false")
	check(got.Count == 7, "struct Updates must not write 0, got %d", got.Count)

	// A MAP has no zero value to be confused by: every key is written,
	// including false and 0. This is the way out when the update is driven
	// by data rather than by a struct.
	must(db.Model(&got).Updates(map[string]any{"active": false, "count": 0}).Error, "map Updates")
	got = reload(db, u.ID)
	check(!got.Active && got.Count == 0, "map Updates must write zero values, got %+v", got)

	// Select names the columns explicitly and is the way out when you do
	// have a struct. Restore first, so the write is a real change.
	must(db.Model(&got).Updates(map[string]any{"active": true, "count": 7}).Error, "restore")
	check(reload(db, u.ID).Active, "restore did not take")
	must(db.Model(&got).Select("active", "count").Updates(store.User{Active: false, Count: 0}).Error,
		"Select+struct Updates")
	got = reload(db, u.ID)
	check(!got.Active && got.Count == 0, "Select must force zero values through, got %+v", got)

	// Update (singular) takes the value as given, which is the third way out.
	must(db.Model(&got).Update("count", 5).Error, "Update to 5")
	check(reload(db, u.ID).Count == 5, "Update should write the literal value")
	must(db.Model(&got).Update("count", 0).Error, "Update to 0")
	check(reload(db, u.ID).Count == 0, "Update must write an explicit zero")

	// First reports a missing row as an error. Find does not: an empty
	// result is a successful query with RowsAffected 0, which is why
	// checking err after Find never catches "nothing matched".
	var missing store.User
	err = db.First(&missing, 424242).Error
	check(errors.Is(err, gorm.ErrRecordNotFound), "expected ErrRecordNotFound, got %v", err)
	var none []store.User
	res = db.Where("id = ?", 424242).Find(&none)
	check(res.Error == nil, "Find on zero rows is not an error, got %v", res.Error)
	check(res.RowsAffected == 0 && len(none) == 0, "Find returned %d rows", res.RowsAffected)

	// gorm.DeletedAt on the model turns Delete into an UPDATE and adds
	// "deleted_at IS NULL" to every later query on it. Unscoped drops that
	// condition, and the row was never physically removed.
	must(db.Delete(&got).Error, "delete")
	var live []store.User
	must(db.Find(&live).Error, "find")
	check(len(live) == 0, "soft-deleted row still visible to Find: %+v", live)
	err = db.First(&missing, got.ID).Error
	check(errors.Is(err, gorm.ErrRecordNotFound), "soft-deleted First should be not-found, got %v", err)
	var all []store.User
	must(db.Unscoped().Find(&all).Error, "unscoped find")
	check(len(all) == 1, "Unscoped should see the soft-deleted row, got %d", len(all))
	check(all[0].DeletedAt.Valid, "deleted_at should be set, got %+v", all[0].DeletedAt)
	var physical int64
	must(db.Raw("select count(*) from users").Scan(&physical).Error, "raw count")
	check(physical == 1, "soft delete must leave the row in the table, found %d", physical)

	// Soft delete frees nothing as far as constraints are concerned. The row
	// is still physically there, so its primary key is still taken, and the
	// error carries SQLite's own constraint message rather than anything GORM
	// invented.
	err = db.Create(&store.User{ID: got.ID, Name: "grace"}).Error
	check(err != nil, "duplicate primary key must fail even when the row is soft-deleted")
	check(strings.Contains(err.Error(), "UNIQUE constraint failed: users.id"),
		"expected the SQLite constraint message, got %q", err.Error())
	// Error translation is off by default, so that stays a driver error and
	// errors.Is against GORM's sentinel does not match it.
	check(!errors.Is(err, gorm.ErrDuplicatedKey), "unexpected translation to ErrDuplicatedKey: %v", err)

	duplicateKeyUnderTranslation()
	memoryIsPerConnection()

	fmt.Println("contract ok")
}

// gorm.Config{TranslateError: true} makes errors.Is(err, gorm.ErrDuplicatedKey)
// work, which is the portable way to catch a conflict. The trade is real and
// goes the other way: this dialector's Translate returns the sentinel INSTEAD
// of the driver error rather than wrapping it, so the message naming the
// failing constraint is gone from the error a handler logs.
func duplicateKeyUnderTranslation() {
	db, err := store.OpenTranslating(":memory:")
	must(err, "open translating")
	must(db.AutoMigrate(&store.User{}), "translating AutoMigrate")
	must(db.Create(&store.User{ID: 1, Name: "ada"}).Error, "translating create")

	err = db.Create(&store.User{ID: 1, Name: "grace"}).Error
	check(errors.Is(err, gorm.ErrDuplicatedKey), "expected ErrDuplicatedKey, got %v", err)
	check(!strings.Contains(err.Error(), "UNIQUE constraint failed"),
		"translation was expected to replace the driver message, got %q", err.Error())
}

// ":memory:" names a private database per connection, and database/sql hands
// out connections from a pool, so a second connection is a second empty
// database. Two explicit connections make that deterministic instead of a
// race: the migrated table exists on the first one only. store.Open pins the
// pool to a single connection for exactly this reason.
func memoryIsPerConnection() {
	db, err := store.OpenPooled(":memory:")
	must(err, "open pooled")
	must(db.AutoMigrate(&store.User{}), "pooled AutoMigrate")

	sqlDB, err := db.DB()
	must(err, "sql handle")
	ctx := context.Background()

	first, err := sqlDB.Conn(ctx)
	must(err, "first conn")
	defer first.Close()
	second, err := sqlDB.Conn(ctx)
	must(err, "second conn")
	defer second.Close()

	const q = "select count(*) from sqlite_master where type = 'table' and name = 'users'"
	var onFirst, onSecond int
	must(first.QueryRowContext(ctx, q).Scan(&onFirst), "query first")
	must(second.QueryRowContext(ctx, q).Scan(&onSecond), "query second")
	check(onFirst == 1, "the migrated connection should have users, got %d", onFirst)
	check(onSecond == 0, "a second connection should be a different empty database, got %d", onSecond)
}

func reload(db *gorm.DB, id uint) store.User {
	var out store.User
	must(db.First(&out, id).Error, fmt.Sprintf("reload %d", id))
	return out
}

func must(err error, what string) {
	check(err == nil, "%s: %v", what, err)
}

func check(ok bool, format string, args ...any) {
	if !ok {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}
}
