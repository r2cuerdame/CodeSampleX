package localdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestUpgradePreExitCodeObservationsPreservesRowsAndCreatesIndexAfterColumn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE observations(epoch TEXT NOT NULL,purl TEXT NOT NULL,symbol TEXT NOT NULL DEFAULT '',env_hash TEXT NOT NULL,stage TEXT NOT NULL,result TEXT NOT NULL,count INTEGER NOT NULL DEFAULT 0,error_fp TEXT NOT NULL DEFAULT '',uploaded INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(epoch,purl,symbol,env_hash,stage,result,error_fp))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`INSERT INTO observations(epoch,purl,env_hash,stage,result,count) VALUES('2026-11-11','pkg:npm/axios@1.12.0','env','PROJECT_TEST','FAIL',7)`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := Open(file)
	if err != nil {
		t.Fatalf("upgrade failed before command could return: %v", err)
	}
	defer db.Close()
	var count, indexes int
	if err := db.sql.QueryRow(`SELECT count FROM observations`).Scan(&count); err != nil || count != 7 {
		t.Fatalf("legacy observation lost: %d %v", count, err)
	}
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='observations_legacy_windows'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("exit index missing: %d %v", indexes, err)
	}
}
