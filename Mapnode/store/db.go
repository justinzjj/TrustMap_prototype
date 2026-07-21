package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var (
	ErrMigrationChecksum = errors.New("migration checksum mismatch")
	ErrFutureMigration   = errors.New("database contains a future migration")
	ErrMigrationGap      = errors.New("database migration history has a gap")
	ErrRecordNotFound    = errors.New("record not found")
	ErrRecordConflict    = errors.New("record conflicts with persisted identity")
	ErrConcurrentUpdate  = errors.New("record state changed concurrently")
)

type DB struct {
	sql *sql.DB
}

func Open(path string) (*DB, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("a non-memory SQLite file path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "recursive_triggers(1)")
	query.Set("_txlock", "immediate")
	dsnURL.RawQuery = query.Encode()

	sqlDB, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(8)
	db := &DB{sql: sqlDB}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("connect SQLite database: %w", err)
	}
	if err := db.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}
