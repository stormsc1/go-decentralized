package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	_ "modernc.org/sqlite"
)

// SQLite is a store of records and of pairs in a SQLite database: records as
// JSON, with an index on the expression for each indexed field.
type SQLite struct {
	db *sql.DB
}

// sqlitePath resolves a configured path: relative ones under dataDir, and
// everything to memory without a dataDir.
func sqlitePath(path, dataDir string) string {
	if dataDir == "" || path == ":memory:" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dataDir, path)
}

// OpenSQLite opens the database at path, creating it if it doesn't exist.
// An empty path keeps the database in memory.
func OpenSQLite(path string) (*SQLite, error) {
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
	return &SQLite{db: db}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

// Entities returns the database as a store of records.
func (s *SQLite) Entities() EntityStore { return sqliteRecords{s.db} }

// KV returns the database as a store of pairs.
func (s *SQLite) KV() KVStore { return sqlitePairs{s.db} }

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
	if ifVersion != Any && ifVersion != have {
		return &Conflict{Want: ifVersion, Have: have}
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

// sqliteRecords is the records in a SQLite database.
type sqliteRecords struct{ db *sql.DB }

// column is how SQL reaches field: the id column, or a field of the record.
// Fields must match Field, as they become part of the SQL.
func column(field string) (string, error) {
	if field == "id" {
		return "id", nil
	}
	if !Field.MatchString(field) {
		return "", fmt.Errorf("invalid field %q", field)
	}
	return "json_extract(record, '$." + field + "')", nil
}

func (r sqliteRecords) Index(ctx context.Context, ns, entity, field string) error {
	col, err := column(field)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(ns + "\x00" + entity + "\x00" + field))
	name := "idx_" + hex.EncodeToString(sum[:8])
	_, err = r.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+name+" ON records (ns, entity, "+col+")")
	return err
}

func (r sqliteRecords) Put(ctx context.Context, ns, entity, id string, record json.RawMessage, ifVersion int64) (int64, error) {
	return putRecord(ctx, r.db, ns, entity, id, record, ifVersion)
}

func putRecord(ctx context.Context, e execer, ns, entity, id string, record json.RawMessage, ifVersion int64) (int64, error) {
	have, err := version(ctx, e, "SELECT version FROM records WHERE ns = ? AND entity = ? AND id = ?", ns, entity, id)
	if err != nil {
		return 0, err
	}
	if err := check(ifVersion, have); err != nil {
		return 0, err
	}
	_, err = e.ExecContext(ctx,
		"INSERT INTO records (ns, entity, id, version, record) VALUES (?, ?, ?, ?, ?) "+
			"ON CONFLICT (ns, entity, id) DO UPDATE SET version = excluded.version, record = excluded.record",
		ns, entity, id, have+1, string(record))
	return have + 1, err
}

func (r sqliteRecords) Get(ctx context.Context, ns, entity, id string) (Record, error) {
	rec := Record{ID: id}
	var record string
	err := r.db.QueryRowContext(ctx, "SELECT version, record FROM records WHERE ns = ? AND entity = ? AND id = ?",
		ns, entity, id).Scan(&rec.Version, &record)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	rec.Data = json.RawMessage(record)
	return rec, err
}

func (r sqliteRecords) Delete(ctx context.Context, ns, entity, id string, ifVersion int64) error {
	return deleteRecord(ctx, r.db, ns, entity, id, ifVersion)
}

func deleteRecord(ctx context.Context, e execer, ns, entity, id string, ifVersion int64) error {
	have, err := version(ctx, e, "SELECT version FROM records WHERE ns = ? AND entity = ? AND id = ?", ns, entity, id)
	if err != nil {
		return err
	}
	if err := check(ifVersion, have); err != nil {
		return err
	}
	_, err = e.ExecContext(ctx, "DELETE FROM records WHERE ns = ? AND entity = ? AND id = ?", ns, entity, id)
	return err
}

func (r sqliteRecords) Query(ctx context.Context, ns, entity string, q Query) ([]Record, error) {
	query := "SELECT id, version, record FROM records WHERE ns = ? AND entity = ?"
	args := []any{ns, entity}
	for _, field := range slices.Sorted(maps.Keys(q.Where)) { // the same query, the same SQL
		col, err := column(field)
		if err != nil {
			return nil, err
		}
		cond, isCond := q.Where[field].(map[string]any)
		if !isCond {
			cond = map[string]any{"eq": q.Where[field]}
		}
		for _, op := range slices.Sorted(maps.Keys(cond)) {
			value := sqlValue(cond[op])
			switch {
			case op == "eq" && value == nil:
				query += " AND " + col + " IS NULL"
			case op == "eq":
				query += " AND " + col + " = ?"
				args = append(args, value)
			case operators[op] != "":
				query += " AND " + col + " " + operators[op] + " ?"
				args = append(args, value)
			default:
				return nil, fmt.Errorf("invalid condition %q on %s", op, field)
			}
		}
	}
	order := "id"
	if q.OrderBy != "" {
		col, err := column(q.OrderBy)
		if err != nil {
			return nil, err
		}
		order = col
	}
	dir := " ASC"
	if q.Desc {
		dir = " DESC"
	}
	query += " ORDER BY " + order + dir
	if order != "id" {
		query += ", id" + dir
	}
	query += " LIMIT ?"
	args = append(args, q.Limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var rec Record
		var record string
		if err := rows.Scan(&rec.ID, &rec.Version, &record); err != nil {
			return nil, err
		}
		rec.Data = json.RawMessage(record)
		records = append(records, rec)
	}
	return records, rows.Err()
}

// sqlValue converts a JSON value to what SQLite's json_extract compares it
// with.
func sqlValue(v any) any {
	if b, ok := v.(bool); ok {
		if b {
			return 1
		}
		return 0
	}
	return v
}

func (r sqliteRecords) Batch(ctx context.Context, ns string, ops []EntityOp) ([]int64, error) {
	versions := make([]int64, len(ops))
	err := transact(ctx, r.db, func(tx *sql.Tx) error {
		for i, op := range ops {
			var err error
			if op.Record != nil {
				versions[i], err = putRecord(ctx, tx, ns, op.Entity, op.ID, op.Record, op.IfVersion)
			} else {
				err = deleteRecord(ctx, tx, ns, op.Entity, op.ID, op.IfVersion)
			}
			if err != nil {
				return fmt.Errorf("op %d: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return versions, nil
}

// sqlitePairs is the pairs in a SQLite database.
type sqlitePairs struct{ db *sql.DB }

func (p sqlitePairs) Get(ctx context.Context, ns, key string) (Pair, error) {
	pair := Pair{Key: key}
	var value string
	err := p.db.QueryRowContext(ctx, "SELECT version, value FROM pairs WHERE ns = ? AND key = ?", ns, key).Scan(&pair.Version, &value)
	if errors.Is(err, sql.ErrNoRows) {
		return Pair{}, ErrNotFound
	}
	pair.Value = json.RawMessage(value)
	return pair, err
}

func (p sqlitePairs) Put(ctx context.Context, ns, key string, value json.RawMessage, ifVersion int64) (int64, error) {
	return putPair(ctx, p.db, ns, key, value, ifVersion)
}

func putPair(ctx context.Context, e execer, ns, key string, value json.RawMessage, ifVersion int64) (int64, error) {
	have, err := version(ctx, e, "SELECT version FROM pairs WHERE ns = ? AND key = ?", ns, key)
	if err != nil {
		return 0, err
	}
	if err := check(ifVersion, have); err != nil {
		return 0, err
	}
	_, err = e.ExecContext(ctx,
		"INSERT INTO pairs (ns, key, version, value) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT (ns, key) DO UPDATE SET version = excluded.version, value = excluded.value",
		ns, key, have+1, string(value))
	return have + 1, err
}

func (p sqlitePairs) Delete(ctx context.Context, ns, key string, ifVersion int64) error {
	return deletePair(ctx, p.db, ns, key, ifVersion)
}

func deletePair(ctx context.Context, e execer, ns, key string, ifVersion int64) error {
	have, err := version(ctx, e, "SELECT version FROM pairs WHERE ns = ? AND key = ?", ns, key)
	if err != nil {
		return err
	}
	if err := check(ifVersion, have); err != nil {
		return err
	}
	_, err = e.ExecContext(ctx, "DELETE FROM pairs WHERE ns = ? AND key = ?", ns, key)
	return err
}

func (p sqlitePairs) List(ctx context.Context, ns, prefix string, limit int) ([]Pair, error) {
	// Keys are UTF-8, so every key starting with prefix sorts below prefix
	// followed by the highest code point.
	rows, err := p.db.QueryContext(ctx,
		"SELECT key, version, value FROM pairs WHERE ns = ? AND key >= ? AND key < ? ORDER BY key LIMIT ?",
		ns, prefix, prefix+"\U0010FFFF", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pairs []Pair
	for rows.Next() {
		var pair Pair
		var value string
		if err := rows.Scan(&pair.Key, &pair.Version, &value); err != nil {
			return nil, err
		}
		pair.Value = json.RawMessage(value)
		if strings.HasPrefix(pair.Key, prefix) {
			pairs = append(pairs, pair)
		}
	}
	return pairs, rows.Err()
}

func (p sqlitePairs) Batch(ctx context.Context, ns string, ops []KVOp) ([]int64, error) {
	versions := make([]int64, len(ops))
	err := transact(ctx, p.db, func(tx *sql.Tx) error {
		for i, op := range ops {
			var err error
			if op.Value != nil {
				versions[i], err = putPair(ctx, tx, ns, op.Key, op.Value, op.IfVersion)
			} else {
				err = deletePair(ctx, tx, ns, op.Key, op.IfVersion)
			}
			if err != nil {
				return fmt.Errorf("op %d: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return versions, nil
}
