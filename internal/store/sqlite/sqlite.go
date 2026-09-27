// Package sqlite is the SQLite store driver: records and key-value pairs in
// one database file, or in memory. It registers itself as "sqlite".
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"go-decentralized/internal/store"
)

func init() {
	store.Register("sqlite", func(cfg store.Config, dataDir string) (store.Store, error) {
		return Open(resolve(cfg.Options.String("path"), dataDir))
	})
}

// A DB is an open SQLite database: a store of records (entity_store.go) and
// of pairs (kv_store.go).
type DB struct {
	db *sql.DB
}

// resolve resolves a configured path: relative ones under dataDir, and
// everything to memory without a dataDir.
func resolve(path, dataDir string) string {
	if dataDir == "" || path == ":memory:" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dataDir, path)
}

// Open opens the database at path, creating it if it doesn't exist. An empty
// path keeps the database in memory.
func Open(path string) (*DB, error) {
	dsn := ":memory:"
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		dsn = "file:" + filepath.ToSlash(path) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite writes one at a time anyway, and an in-memory database lives
	// in a single connection.
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS records (
			ns TEXT NOT NULL, entity TEXT NOT NULL, id TEXT NOT NULL,
			version INTEGER NOT NULL, record TEXT NOT NULL,
			PRIMARY KEY (ns, entity, id));
		CREATE TABLE IF NOT EXISTS pairs (
			ns TEXT NOT NULL, key TEXT NOT NULL,
			version INTEGER NOT NULL, value TEXT NOT NULL,
			PRIMARY KEY (ns, key));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// Entities returns the database as a store of records.
func (d *DB) Entities() store.EntityStore { return entityStore{d.db} }

// KV returns the database as a store of pairs.
func (d *DB) KV() store.KVStore { return kvStore{d.db} }

// execer runs statements: the database, or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// transact runs f in a transaction, committing unless it fails.
func transact(ctx context.Context, db *sql.DB, f func(tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// check checks a write's condition against the version there is, have (0
// for none).
func check(ifVersion, have int64) error {
	if ifVersion != store.Any && ifVersion != have {
		return &store.Conflict{Want: ifVersion, Have: have}
	}
	return nil
}

// version returns the version of a row, 0 if there's none.
func version(ctx context.Context, e execer, query string, args ...any) (int64, error) {
	var v int64
	err := e.QueryRowContext(ctx, query, args...).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}
