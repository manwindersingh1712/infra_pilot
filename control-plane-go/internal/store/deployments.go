package store

import (
	"context"

	"github.com/lucsky/cuid"
)

func (s *Store) CreateDeployment(ctx context.Context, serviceID, commitSha string) (string, error) {
	id := cuid.New()
	_, err := s.db.Exec(ctx,
		`INSERT INTO "Deployment" ("id", "serviceId", "commitSha", "status", "updatedAt")
		 VALUES ($1, $2, $3, 'queued', now())`, id, serviceID, commitSha)
	return id, err
}
