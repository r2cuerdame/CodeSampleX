module codesamplex.dev/sample/gogormsqlite

go 1.25.0

require (
	github.com/glebarez/sqlite v1.11.0
	gorm.io/driver/sqlite v1.6.0
	gorm.io/gorm v1.31.2
)

// glebarez/sqlite v1.11.0 is the latest release and dates from March 2024, so
// its own go.mod asks for modernc.org/sqlite v1.23.1. That is what minimal
// version selection settles on if you just run `go get github.com/glebarez/
// sqlite`, and it was measured to link SQLite 3.41.2, released in April 2023.
// Forcing the current engine gives 3.53.3, which the contract asserts so the
// pin cannot drift back unnoticed. modernc.org/libc follows from the engine
// and is not a separate choice.
//
// mattn/go-sqlite3 arrives under gorm.io/driver/sqlite, which the contract
// imports only to measure that the cgo driver builds here and then fails at
// the first connection. It is pinned to the current release so the stub
// behaviour being recorded is the current one, not the 2024 one the driver
// asks for.
require (
	github.com/mattn/go-sqlite3 v1.14.49 // indirect
	modernc.org/sqlite v1.56.0 // indirect
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.20.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
