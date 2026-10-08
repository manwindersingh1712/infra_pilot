// Package store is the Postgres data layer. Table and column names come from
// the schema Prisma created, so every identifier is double-quoted.
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	db   DBTX
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{db: pool, pool: pool} }

// InTx runs fn in a transaction. fn receives a Store bound to that transaction;
// returning an error rolls it back.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&Store{db: tx, pool: s.pool})
	})
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func noRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// collect scans every row with scan. It never returns a nil slice, so an
// empty result encodes as [] in JSON.
//
//nolint:unused // first callers (list queries) arrive in Plan 1 Task 7; drop this directive then.
func collect[T any](rows pgx.Rows, err error, scan func(pgx.Row) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
