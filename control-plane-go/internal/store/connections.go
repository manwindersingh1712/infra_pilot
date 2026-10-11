package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const connCols = `"id", "sourceId", "targetId", "label", "createdAt"`

func scanConnection(row pgx.Row) (ServiceConnection, error) {
	var c ServiceConnection
	err := row.Scan(&c.ID, &c.SourceID, &c.TargetID, &c.Label, &c.CreatedAt.Time)
	return c, noRows(err)
}

// CreateConnection returns ErrConflict when the pair already exists.
func (s *Store) CreateConnection(ctx context.Context, sourceID, targetID string, label *string) (ServiceConnection, error) {
	c, err := scanConnection(s.db.QueryRow(ctx,
		`INSERT INTO "ServiceConnection" ("id", "sourceId", "targetId", "label") VALUES ($1, $2, $3, $4)
		 RETURNING `+connCols, cuid.New(), sourceID, targetID, label))
	if isUniqueViolation(err) {
		return ServiceConnection{}, ErrConflict
	}
	return c, err
}

// ListProjectConnections returns connections touching any service of the project.
func (s *Store) ListProjectConnections(ctx context.Context, projectID string) ([]ServiceConnection, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+connCols+` FROM "ServiceConnection"
		 WHERE "sourceId" IN (SELECT "id" FROM "Service" WHERE "projectId" = $1)
		    OR "targetId" IN (SELECT "id" FROM "Service" WHERE "projectId" = $1)
		 ORDER BY "createdAt" ASC`, projectID)
	return collect(rows, err, scanConnection)
}

func (s *Store) GetConnectionForOwner(ctx context.Context, id, ownerUserID string) (ServiceConnection, error) {
	return scanConnection(s.db.QueryRow(ctx,
		`SELECT `+connCols+` FROM "ServiceConnection"
		 WHERE "id" = $1 AND "sourceId" IN (
		   SELECT s."id" FROM "Service" s JOIN "Project" p ON p."id" = s."projectId" WHERE p."ownerUserId" = $2)`,
		id, ownerUserID))
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM "ServiceConnection" WHERE "id" = $1`, id)
	return err
}

// ListSourceEdges returns [source, target] pairs whose source is in the project.
func (s *Store) ListSourceEdges(ctx context.Context, projectID string) ([][2]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT "sourceId", "targetId" FROM "ServiceConnection"
		 WHERE "sourceId" IN (SELECT "id" FROM "Service" WHERE "projectId" = $1)
		 ORDER BY "createdAt" ASC`, projectID)
	return collect(rows, err, func(row pgx.Row) ([2]string, error) {
		var e [2]string
		err := row.Scan(&e[0], &e[1])
		return e, err
	})
}
