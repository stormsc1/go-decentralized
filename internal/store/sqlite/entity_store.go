package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"go-decentralized/internal/store"
)

// entityStore keeps records in the records table, as JSON, with an index on
// the expression for each indexed field.
type entityStore struct{ db *sql.DB }

// operators maps query conditions to SQL.
var operators = map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}

// column is how SQL reaches field: the id column, or a field of the record.
// Fields must match store.Field, as they become part of the SQL.
func column(field string) (string, error) {
	if field == "id" {
		return "id", nil
	}
	if !store.Field.MatchString(field) {
		return "", fmt.Errorf("invalid field %q", field)
	}
	return "json_extract(record, '$." + field + "')", nil
}

func (s entityStore) Index(ctx context.Context, ns, entity, field string) error {
	col, err := column(field)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(ns + "\x00" + entity + "\x00" + field))
	name := "idx_" + hex.EncodeToString(sum[:8])
	_, err = s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+name+" ON records (ns, entity, "+col+")")
	return err
}

func (s entityStore) Put(ctx context.Context, ns, entity, id string, record json.RawMessage, ifVersion int64) (int64, error) {
	return putRecord(ctx, s.db, ns, entity, id, record, ifVersion)
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

func (s entityStore) Get(ctx context.Context, ns, entity, id string) (store.Record, error) {
	r := store.Record{ID: id}
	var record string
	err := s.db.QueryRowContext(ctx, "SELECT version, record FROM records WHERE ns = ? AND entity = ? AND id = ?",
		ns, entity, id).Scan(&r.Version, &record)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Record{}, store.ErrNotFound
	}
	r.Data = json.RawMessage(record)
	return r, err
}

func (s entityStore) Delete(ctx context.Context, ns, entity, id string, ifVersion int64) error {
	return deleteRecord(ctx, s.db, ns, entity, id, ifVersion)
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

func (s entityStore) Query(ctx context.Context, ns, entity string, q store.Query) ([]store.Record, error) {
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

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []store.Record
	for rows.Next() {
		var r store.Record
		var record string
		if err := rows.Scan(&r.ID, &r.Version, &record); err != nil {
			return nil, err
		}
		r.Data = json.RawMessage(record)
		records = append(records, r)
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

func (s entityStore) Batch(ctx context.Context, ns string, ops []store.EntityOp) ([]int64, error) {
	versions := make([]int64, len(ops))
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
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
