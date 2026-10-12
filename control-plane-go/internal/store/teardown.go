package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ServiceAnyState is a service row read without the soft-delete filter. It is
// for TeardownWorkflow only (the delete is committed before teardown runs);
// every request path keeps using GetService / GetServiceForOwner, which hide
// deleted services. It is deliberately not JSON-tagged: it never leaves the
// worker.
type ServiceAnyState struct {
	Service
	DeletedAt *time.Time
}

// GetServiceAnyState returns the service whether or not it is soft-deleted.
func (s *Store) GetServiceAnyState(ctx context.Context, id string) (ServiceAnyState, error) {
	var v ServiceAnyState
	err := s.db.QueryRow(ctx,
		`SELECT `+serviceCols+`, "deletedAt" FROM "Service" WHERE "id" = $1`, id).
		Scan(&v.ID, &v.ProjectID, &v.Name, &v.ServiceType, &v.RepoURL, &v.Branch,
			&v.CreatedAt.Time, &v.PositionX, &v.PositionY, &v.DeletedAt)
	return v, noRows(err)
}

// FailOpenDeployments marks the service's queued, building and deploying
// deployments failed and returns their ids. Deployed and failed rows are left
// alone. Safe to repeat.
func (s *Store) FailOpenDeployments(ctx context.Context, serviceID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`UPDATE "Deployment" SET "status" = 'failed', "updatedAt" = now()
		 WHERE "serviceId" = $1 AND "status" IN ('queued', 'building', 'deploying')
		 RETURNING "id"`, serviceID)
	return collect(rows, err, func(row pgx.Row) (string, error) {
		var id string
		err := row.Scan(&id)
		return id, err
	})
}

// ScrubDeletedService removes what a soft delete leaves behind: the service's
// env vars (ciphertext, including generated managed-DB passwords), its
// connections, and its repo URL (which may embed a token). It refuses a
// service that is not soft-deleted (ErrNotFound), so a misrouted event can
// never strip a live service. Idempotent.
func (s *Store) ScrubDeletedService(ctx context.Context, serviceID string) error {
	return s.InTx(ctx, func(tx *Store) error {
		var one int
		if err := tx.db.QueryRow(ctx,
			`SELECT 1 FROM "Service" WHERE "id" = $1 AND "deletedAt" IS NOT NULL FOR UPDATE`, serviceID).Scan(&one); err != nil {
			return noRows(err)
		}
		if _, err := tx.db.Exec(ctx, `DELETE FROM "EnvVar" WHERE "serviceId" = $1`, serviceID); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx,
			`DELETE FROM "ServiceConnection" WHERE "sourceId" = $1 OR "targetId" = $1`, serviceID); err != nil {
			return err
		}
		_, err := tx.db.Exec(ctx, `UPDATE "Service" SET "repoUrl" = NULL WHERE "id" = $1`, serviceID)
		return err
	})
}

// ListOpenDeploymentIDs returns the service's queued, building and deploying
// deployments: the ones whose DeployWorkflow may still be running.
func (s *Store) ListOpenDeploymentIDs(ctx context.Context, serviceID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT "id" FROM "Deployment"
		 WHERE "serviceId" = $1 AND "status" IN ('queued', 'building', 'deploying') ORDER BY "createdAt"`, serviceID)
	return collect(rows, err, func(row pgx.Row) (string, error) {
		var id string
		err := row.Scan(&id)
		return id, err
	})
}
