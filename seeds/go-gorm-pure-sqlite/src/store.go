// Package store runs GORM against SQLite in an image with no C toolchain.
//
// The answer everyone finds first is gorm.io/driver/sqlite, which wraps
// github.com/mattn/go-sqlite3. That is a cgo package, so the obvious guess is
// that it refuses to build when CGO_ENABLED=0. Measured here, it does not:
// mattn compiles a cgo-less stub instead, so the import builds, the binary
// links, and the stub still registers itself with database/sql under the name
// "sqlite3". Nothing objects until something actually connects, and then the
// call returns
//
//	Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work. This is a stub
//
// This is why the failure shows up in a container and not in CI: the build
// that produced the image succeeded. sql.Open hides it one step further,
// since it only records the driver name — Ping is where it surfaces.
//
// github.com/glebarez/sqlite is the same GORM dialector over a pure-Go engine
// (modernc.org/sqlite, SQLite transpiled to Go). It needs no C, and it
// registers under the driver name "sqlite". A snippet written for mattn says
// "sqlite3" and so reaches the stub rather than this engine, even when both
// are linked into the same binary.
package store

import (
	"os/exec"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// User carries the two field types this contract is about: scalars with
// meaningful zero values (Active, Count) and gorm.DeletedAt, which is what
// turns every query on this model into a soft-delete query.
type User struct {
	ID        uint `gorm:"primaryKey"`
	Name      string
	Count     int
	Active    bool
	CreatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// Open returns a GORM handle on an in-memory database that behaves like one
// database.
//
// Without SetMaxOpenConns(1) it would not. ":memory:" names a private
// database per connection, and database/sql hands out connections from a
// pool, so AutoMigrate can run on one connection and the next query can land
// on a fresh, empty one — the "no such table" that gets blamed on GORM.
// OpenPooled leaves the limit off so the contract can show that happening.
func Open(dsn string) (*gorm.DB, error) {
	db, err := OpenPooled(dsn)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	return db, nil
}

// OpenPooled is Open without the connection limit.
func OpenPooled(dsn string) (*gorm.DB, error) {
	return open(dsn, false)
}

// OpenTranslating switches on GORM's error translation, which routes driver
// errors through the dialector's Translate method. It is off by default and
// it is not free: Translate returns the sentinel in place of the original
// error rather than wrapping it, so turning it on buys errors.Is at the cost
// of the message naming the column.
func OpenTranslating(dsn string) (*gorm.DB, error) {
	db, err := open(dsn, true)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	return db, nil
}

func open(dsn string, translateError bool) (*gorm.DB, error) {
	return gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: translateError,
	})
}

// CCompiler reports the C compiler on PATH, or "" when there is none. It
// matters because rebuilding with CGO_ENABLED=1 is the usual advice for the
// stub error above, and here there is nothing for cgo to invoke.
func CCompiler() string {
	for _, name := range []string{"gcc", "cc", "clang"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}
