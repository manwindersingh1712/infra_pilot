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

// CreateConnectionForOwner connects two live services of ONE project owned by
// ownerUserID, in a single statement (so a concurrent soft delete cannot slip
// between the check and the insert). It returns ErrNotFound when either end is
// missing, soft-deleted, owned by someone else, in a different project, or when
// source == target, without saying which; ErrConflict when the pair exists.
func (s *Store) CreateConnectionForOwner(ctx context.Context, sourceID, targetID, ownerUserID string, label *string) (ServiceConnection, error) {
	c, err := scanConnection(s.db.QueryRow(ctx,
		`INSERT INTO "ServiceConnection" ("id", "sourceId", "targetId", "label")
		 SELECT $1::text, a."id", b."id", $4::text
		 FROM "Service" a
		 JOIN "Service" b ON b."projectId" = a."projectId"
		 JOIN "Project" p ON p."id" = a."projectId"
		 WHERE a."id" = $2 AND b."id" = $3 AND a."id" <> b."id"
		   AND a."deletedAt" IS NULL AND b."deletedAt" IS NULL AND p."ownerUserId" = $5
		 RETURNING `+connCols, cuid.New(), sourceID, targetID, label, ownerUserID))
	switch {
	case isUniqueViolation(err):
		return ServiceConnection{}, ErrConflict
	case isForeignKeyViolation(err): // a service was hard-deleted mid-statement
		return ServiceConnection{}, ErrNotFound
	}
	return c, err
}

// aliveIn matches live (not soft-deleted) services of project $1.
const aliveIn = `(SELECT "id" FROM "Service" WHERE "projectId" = $1 AND "deletedAt" IS NULL)`

// ListProjectConnections returns connections whose two ends are both live
// services of the project. Legacy rows may link other projects or tenants; they
// are never returned, whichever end is in the project.
func (s *Store) ListProjectConnections(ctx context.Context, projectID string) ([]ServiceConnection, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+connCols+` FROM "ServiceConnection"
		 WHERE "sourceId" IN `+aliveIn+` AND "targetId" IN `+aliveIn+`
		 ORDER BY "createdAt" ASC`, projectID)
	return collect(rows, err, scanConnection)
}

// GetConnectionForOwner finds a connection whose two services are both live
// and owned by ownerUserID.
func (s *Store) GetConnectionForOwner(ctx context.Context, id, ownerUserID string) (ServiceConnection, error) {
	const owned = `(SELECT s."id" FROM "Service" s JOIN "Project" p ON p."id" = s."projectId"
		   WHERE p."ownerUserId" = $2 AND s."deletedAt" IS NULL)`
	return scanConnection(s.db.QueryRow(ctx,
		`SELECT `+connCols+` FROM "ServiceConnection"
		 WHERE "id" = $1 AND "sourceId" IN `+owned+` AND "targetId" IN `+owned,
		id, ownerUserID))
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM "ServiceConnection" WHERE "id" = $1`, id)
	return err
}

// ListSourceEdges returns [source, target] pairs whose two ends are both live
// services of the project.
func (s *Store) ListSourceEdges(ctx context.Context, projectID string) ([][2]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT "sourceId", "targetId" FROM "ServiceConnection"
		 WHERE "sourceId" IN `+aliveIn+` AND "targetId" IN `+aliveIn+`
		 ORDER BY "createdAt" ASC`, projectID)
	return collect(rows, err, func(row pgx.Row) ([2]string, error) {
		var e [2]string
		err := row.Scan(&e[0], &e[1])
		return e, err
	})
}
