package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go-decentralized/internal/store"
)

// kvStore keeps pairs in the pairs table, values as JSON.
type kvStore struct{ db *sql.DB }

func (s kvStore) Get(ctx context.Context, ns, key string) (store.Pair, error) {
	p := store.Pair{Key: key}
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT version, value FROM pairs WHERE ns = ? AND key = ?", ns, key).Scan(&p.Version, &value)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Pair{}, store.ErrNotFound
	}
	p.Value = json.RawMessage(value)
	return p, err
}

func (s kvStore) Put(ctx context.Context, ns, key string, value json.RawMessage, ifVersion int64) (int64, error) {
	return putPair(ctx, s.db, ns, key, value, ifVersion)
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

func (s kvStore) Delete(ctx context.Context, ns, key string, ifVersion int64) error {
	return deletePair(ctx, s.db, ns, key, ifVersion)
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

func (s kvStore) List(ctx context.Context, ns, prefix string, limit int) ([]store.Pair, error) {
	// Keys are UTF-8, so every key starting with prefix sorts below prefix
	// followed by the highest code point.
	rows, err := s.db.QueryContext(ctx,
		"SELECT key, version, value FROM pairs WHERE ns = ? AND key >= ? AND key < ? ORDER BY key LIMIT ?",
		ns, prefix, prefix+"\U0010FFFF", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pairs []store.Pair
	for rows.Next() {
		var p store.Pair
		var value string
		if err := rows.Scan(&p.Key, &p.Version, &value); err != nil {
			return nil, err
		}
		p.Value = json.RawMessage(value)
		if strings.HasPrefix(p.Key, prefix) {
			pairs = append(pairs, p)
		}
	}
	return pairs, rows.Err()
}

func (s kvStore) Batch(ctx context.Context, ns string, ops []store.KVOp) ([]int64, error) {
	versions := make([]int64, len(ops))
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
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
