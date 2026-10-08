package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const projectCols = `"id", "name", "ownerUserId", "createdAt"`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.OwnerUserID, &p.CreatedAt.Time)
	return p, noRows(err)
}

func (s *Store) CreateProject(ctx context.Context, name, ownerUserID string) (Project, error) {
	return scanProject(s.db.QueryRow(ctx,
		`INSERT INTO "Project" ("id", "name", "ownerUserId") VALUES ($1, $2, $3) RETURNING `+projectCols,
		cuid.New(), name, ownerUserID))
}

func (s *Store) ListProjects(ctx context.Context, ownerUserID string) ([]Project, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+projectCols+` FROM "Project" WHERE "ownerUserId" = $1 ORDER BY "createdAt" DESC`, ownerUserID)
	return collect(rows, err, scanProject)
}

func (s *Store) GetProjectForOwner(ctx context.Context, id, ownerUserID string) (Project, error) {
	return scanProject(s.db.QueryRow(ctx,
		`SELECT `+projectCols+` FROM "Project" WHERE "id" = $1 AND "ownerUserId" = $2`, id, ownerUserID))
}
