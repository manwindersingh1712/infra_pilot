package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

// MigrateTo exposes migrateTo to the db_test integration tests.
var MigrateTo = migrateTo

// MigrationLockID exposes the migration advisory lock key, so tests can hold
// the lock from another connection.
const MigrationLockID = migrationLockID

// MigrateDownTo rolls migrations back to version (exclusive of lower ones).
func MigrateDownTo(ctx context.Context, pool *pgxpool.Pool, version int64) error {
	return migrateLocked(ctx, pool, func(p *goose.Provider) error {
		_, err := p.DownTo(ctx, version)
		return err
	})
}
