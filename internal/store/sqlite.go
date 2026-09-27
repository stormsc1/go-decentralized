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

// SQLite stores modules' data in a SQLite database: records as JSON, with
// an index on the expression for each indexed field.
type SQLite struct {
	db *sql.DB
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
		CREATE TABLE IF NOT EXISTS entities (
			module TEXT NOT NULL, entity TEXT NOT NULL, id TEXT NOT NULL, record TEXT NOT NULL,
			PRIMARY KEY (module, entity, id));
		CREATE TABLE IF NOT EXISTS kv (
			module TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL,
			PRIMARY KEY (module, key));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

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

func (s *SQLite) Index(ctx context.Context, module, entity, field string) error {
	col, err := column(field)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(module + "\x00" + entity + "\x00" + field))
	name := "idx_" + hex.EncodeToString(sum[:8])
	_, err = s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+name+" ON entities (module, entity, "+col+")")
	return err
}

func (s *SQLite) Put(ctx context.Context, module, entity, id string, record json.RawMessage) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO entities (module, entity, id, record) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT (module, entity, id) DO UPDATE SET record = excluded.record",
		module, entity, id, string(record))
	return err
}

func (s *SQLite) Get(ctx context.Context, module, entity, id string) (json.RawMessage, error) {
	var record string
	err := s.db.QueryRowContext(ctx, "SELECT record FROM entities WHERE module = ? AND entity = ? AND id = ?",
		module, entity, id).Scan(&record)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return json.RawMessage(record), err
}

func (s *SQLite) Delete(ctx context.Context, module, entity, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM entities WHERE module = ? AND entity = ? AND id = ?", module, entity, id)
	return err
}

func (s *SQLite) Query(ctx context.Context, module, entity string, q Query) ([]json.RawMessage, error) {
	query := "SELECT record FROM entities WHERE module = ? AND entity = ?"
	args := []any{module, entity}
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

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []json.RawMessage
	for rows.Next() {
		var record string
		if err := rows.Scan(&record); err != nil {
			return nil, err
		}
		records = append(records, json.RawMessage(record))
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

func (s *SQLite) KVGet(ctx context.Context, module, key string) (json.RawMessage, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM kv WHERE module = ? AND key = ?", module, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return json.RawMessage(value), err
}

func (s *SQLite) KVPut(ctx context.Context, module, key string, value json.RawMessage) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO kv (module, key, value) VALUES (?, ?, ?) ON CONFLICT (module, key) DO UPDATE SET value = excluded.value",
		module, key, string(value))
	return err
}

func (s *SQLite) KVDelete(ctx context.Context, module, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM kv WHERE module = ? AND key = ?", module, key)
	return err
}

func (s *SQLite) KVList(ctx context.Context, module, prefix string, limit int) ([]Pair, error) {
	// Keys are UTF-8, so every key starting with prefix sorts below prefix
	// followed by the highest code point.
	rows, err := s.db.QueryContext(ctx,
		"SELECT key, value FROM kv WHERE module = ? AND key >= ? AND key < ? ORDER BY key LIMIT ?",
		module, prefix, prefix+"\U0010FFFF", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pairs []Pair
	for rows.Next() {
		var p Pair
		var value string
		if err := rows.Scan(&p.Key, &value); err != nil {
			return nil, err
		}
		p.Value = json.RawMessage(value)
		if strings.HasPrefix(p.Key, prefix) {
			pairs = append(pairs, p)
		}
	}
	return pairs, rows.Err()
}
