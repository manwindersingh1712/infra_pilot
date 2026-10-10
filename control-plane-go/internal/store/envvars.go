package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const envCols = `"id", "serviceId", "key", "value", "createdAt"`

func scanEnvVar(row pgx.Row) (EnvVar, error) {
	var e EnvVar
	err := row.Scan(&e.ID, &e.ServiceID, &e.Key, &e.Value, &e.CreatedAt.Time)
	return e, noRows(err)
}

func (s *Store) ListEnvVars(ctx context.Context, serviceID string) ([]EnvVar, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+envCols+` FROM "EnvVar" WHERE "serviceId" = $1 ORDER BY "key" ASC`, serviceID)
	return collect(rows, err, scanEnvVar)
}

func (s *Store) UpsertEnvVar(ctx context.Context, serviceID, key, value string) (EnvVar, error) {
	return scanEnvVar(s.db.QueryRow(ctx,
		`INSERT INTO "EnvVar" ("id", "serviceId", "key", "value") VALUES ($1, $2, $3, $4)
		 ON CONFLICT ("serviceId", "key") DO UPDATE SET "value" = EXCLUDED."value"
		 RETURNING `+envCols,
		cuid.New(), serviceID, key, value))
}

// InsertEnvVarIfAbsent stores the value only when the key is unset and reports
// whether it did. An existing value is never replaced.
func (s *Store) InsertEnvVarIfAbsent(ctx context.Context, serviceID, key, value string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`INSERT INTO "EnvVar" ("id", "serviceId", "key", "value") VALUES ($1, $2, $3, $4)
		 ON CONFLICT ("serviceId", "key") DO NOTHING`,
		cuid.New(), serviceID, key, value)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReplaceEnvVarIfEquals swaps the stored value for newValue only while it is
// still exactly oldValue (compare-and-swap) and reports whether it did. Racing
// callers that observed the same old value cannot both win.
func (s *Store) ReplaceEnvVarIfEquals(ctx context.Context, serviceID, key, oldValue, newValue string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE "EnvVar" SET "value" = $4 WHERE "serviceId" = $1 AND "key" = $2 AND "value" = $3`,
		serviceID, key, oldValue, newValue)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) DeleteEnvVar(ctx context.Context, serviceID, key string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM "EnvVar" WHERE "serviceId" = $1 AND "key" = $2`, serviceID, key)
	return err
}

// EnvMap returns a service's env vars as key → value for docker run.
func (s *Store) EnvMap(ctx context.Context, serviceID string) (map[string]string, error) {
	vars, err := s.ListEnvVars(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Key] = v.Value
	}
	return m, nil
}
