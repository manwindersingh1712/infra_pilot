package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

// Every query below hides soft-deleted services ("deletedAt" IS NOT NULL).
const serviceCols = `"id", "projectId", "name", "serviceType"::text, "repoUrl", "branch", "createdAt", "positionX", "positionY"`

func scanService(row pgx.Row) (Service, error) {
	var v Service
	err := row.Scan(&v.ID, &v.ProjectID, &v.Name, &v.ServiceType, &v.RepoURL, &v.Branch,
		&v.CreatedAt.Time, &v.PositionX, &v.PositionY)
	return v, noRows(err)
}

type NewService struct {
	ProjectID   string
	Name        string
	ServiceType string
	RepoURL     *string
	Branch      string
}

func (s *Store) CreateService(ctx context.Context, n NewService) (Service, error) {
	return scanService(s.db.QueryRow(ctx,
		`INSERT INTO "Service" ("id", "projectId", "name", "serviceType", "repoUrl", "branch")
		 VALUES ($1, $2, $3, $4::"ServiceType", $5, $6) RETURNING `+serviceCols,
		cuid.New(), n.ProjectID, n.Name, n.ServiceType, n.RepoURL, n.Branch))
}

func (s *Store) GetService(ctx context.Context, id string) (Service, error) {
	return scanService(s.db.QueryRow(ctx,
		`SELECT `+serviceCols+` FROM "Service" WHERE "id" = $1 AND "deletedAt" IS NULL`, id))
}

// ListServices returns all services, or one project's when projectID is not
// empty, newest first. API handlers use ListServicesForOwner instead.
func (s *Store) ListServices(ctx context.Context, projectID string) ([]Service, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+serviceCols+` FROM "Service"
		 WHERE "deletedAt" IS NULL AND ($1 = '' OR "projectId" = $1) ORDER BY "createdAt" DESC`, projectID)
	return collect(rows, err, scanService)
}

// ListServicesForOwner returns the caller's services, optionally for one project.
func (s *Store) ListServicesForOwner(ctx context.Context, ownerUserID, projectID string) ([]Service, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+serviceCols+` FROM "Service"
		 WHERE "deletedAt" IS NULL AND ($2 = '' OR "projectId" = $2)
		   AND "projectId" IN (SELECT "id" FROM "Project" WHERE "ownerUserId" = $1)
		 ORDER BY "createdAt" DESC`, ownerUserID, projectID)
	return collect(rows, err, scanService)
}

func (s *Store) GetServiceForOwner(ctx context.Context, id, ownerUserID string) (Service, error) {
	return scanService(s.db.QueryRow(ctx,
		`SELECT `+serviceCols+` FROM "Service"
		 WHERE "id" = $1 AND "deletedAt" IS NULL
		   AND "projectId" IN (SELECT "id" FROM "Project" WHERE "ownerUserId" = $2)`,
		id, ownerUserID))
}

// SoftDeleteService hides the service; TeardownWorkflow removes its containers.
func (s *Store) SoftDeleteService(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Service" SET "deletedAt" = now() WHERE "id" = $1 AND "deletedAt" IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateServicePosition(ctx context.Context, id string, x, y float64) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Service" SET "positionX" = $2, "positionY" = $3 WHERE "id" = $1 AND "deletedAt" IS NULL`, id, x, y)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateServicePositionInProject ignores services outside projectID (no error), like Prisma's updateMany.
func (s *Store) UpdateServicePositionInProject(ctx context.Context, id, projectID string, x, y float64) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "Service" SET "positionX" = $3, "positionY" = $4
		 WHERE "id" = $1 AND "projectId" = $2 AND "deletedAt" IS NULL`,
		id, projectID, x, y)
	return err
}

// CanvasService is the canvas view of a service.
type CanvasService struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	ServiceType     string  `json:"serviceType"`
	PositionX       float64 `json:"positionX"`
	PositionY       float64 `json:"positionY"`
	DeploymentCount int     `json:"deploymentCount"`
}

func (s *Store) ListCanvasServices(ctx context.Context, projectID string) ([]CanvasService, error) {
	rows, err := s.db.Query(ctx,
		`SELECT s."id", s."name", s."serviceType"::text, s."positionX", s."positionY",
		        (SELECT count(*) FROM "Deployment" d WHERE d."serviceId" = s."id")::int
		 FROM "Service" s WHERE s."projectId" = $1 AND s."deletedAt" IS NULL
		 ORDER BY s."createdAt" ASC`, projectID)
	return collect(rows, err, func(row pgx.Row) (CanvasService, error) {
		var c CanvasService
		err := row.Scan(&c.ID, &c.Name, &c.ServiceType, &c.PositionX, &c.PositionY, &c.DeploymentCount)
		return c, err
	})
}
