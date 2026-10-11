package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lucsky/cuid"
)

const deploymentCols = `"id", "serviceId", "commitSha", "image", "status"::text, "containerId", "hostPort", "containerPort", "runtimeUrl", "volumePath", "createdAt", "updatedAt", "stoppedAt"`

func scanDeployment(row pgx.Row) (Deployment, error) {
	var d Deployment
	var stopped *time.Time
	err := row.Scan(&d.ID, &d.ServiceID, &d.CommitSha, &d.Image, &d.Status, &d.ContainerID,
		&d.HostPort, &d.ContainerPort, &d.RuntimeURL, &d.VolumePath, &d.CreatedAt.Time, &d.UpdatedAt.Time, &stopped)
	if stopped != nil {
		d.StoppedAt = &Timestamp{*stopped}
	}
	return d, noRows(err)
}

// CreateDeployment inserts a queued deployment. "updatedAt" has no DB default
// (Prisma set it in app code), so it is set explicitly.
func (s *Store) CreateDeployment(ctx context.Context, serviceID, commitSha string) (Deployment, error) {
	return scanDeployment(s.db.QueryRow(ctx,
		`INSERT INTO "Deployment" ("id", "serviceId", "commitSha", "status", "updatedAt")
		 VALUES ($1, $2, $3, 'queued', now()) RETURNING `+deploymentCols,
		cuid.New(), serviceID, commitSha))
}

func (s *Store) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	return scanDeployment(s.db.QueryRow(ctx, `SELECT `+deploymentCols+` FROM "Deployment" WHERE "id" = $1`, id))
}

func (s *Store) DeploymentExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "Deployment" WHERE "id" = $1)`, id).Scan(&ok)
	return ok, err
}

// ListDeployments returns the 50 newest deployments, optionally for one service.
func (s *Store) ListDeployments(ctx context.Context, serviceID string) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+deploymentCols+` FROM "Deployment" WHERE ($1 = '' OR "serviceId" = $1)
		 ORDER BY "createdAt" DESC LIMIT 50`, serviceID)
	return collect(rows, err, scanDeployment)
}

// ClaimDeployment moves a deployment from one status to another. Only one
// concurrent caller gets true.
func (s *Store) ClaimDeployment(ctx context.Context, id, from, to string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "status" = $3::"DeploymentStatus", "updatedAt" = now()
		 WHERE "id" = $1 AND "status" = $2::"DeploymentStatus"`, id, from, to)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimResult is the outcome of ClaimDeploy.
type ClaimResult int

const (
	ClaimLost ClaimResult = iota // the row is gone or not queued: nothing to do
	ClaimWon                     // the row is now (or already was) deploying
	ClaimBusy                    // another deployment of the service is deploying: retry later
)

// deployingStaleAfter bounds how long a deploying row can block its service
// (a run killed without cleanup). It exceeds the longest legitimate deploy.
const deployingStaleAfter = "1 hour"

// ClaimDeploy is the service-scoped queued -> deploying claim. One transaction
// locks the service row with FOR NO KEY UPDATE, which serialises the claims of
// one service (other services are unaffected) but, unlike FOR UPDATE, does not
// block the FOR KEY SHARE that Deployment inserts take on the Service foreign
// key. A row already deploying is this deployment's own
// retry or resumed run and wins again. Otherwise a queued row wins unless
// another deployment of the service is deploying and was updated within the
// last hour.
func (s *Store) ClaimDeploy(ctx context.Context, id string) (ClaimResult, error) {
	res := ClaimLost
	err := s.InTx(ctx, func(tx *Store) error {
		var serviceID string
		if err := tx.db.QueryRow(ctx, `SELECT "serviceId" FROM "Deployment" WHERE "id" = $1`, id).Scan(&serviceID); err != nil {
			if noRows(err) == ErrNotFound {
				return nil
			}
			return err
		}
		if _, err := tx.db.Exec(ctx, `SELECT 1 FROM "Service" WHERE "id" = $1 FOR NO KEY UPDATE`, serviceID); err != nil {
			return err
		}
		var status string
		if err := tx.db.QueryRow(ctx, `SELECT "status"::text FROM "Deployment" WHERE "id" = $1`, id).Scan(&status); err != nil {
			return err
		}
		switch status {
		case StatusDeploying:
			res = ClaimWon
			return nil
		case StatusQueued:
		default:
			return nil
		}
		var busy bool
		if err := tx.db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM "Deployment" WHERE "serviceId" = $1 AND "id" <> $2
			   AND "status" = 'deploying' AND "updatedAt" > now() - $3::interval)`,
			serviceID, id, deployingStaleAfter).Scan(&busy); err != nil {
			return err
		}
		if busy {
			res = ClaimBusy
			return nil
		}
		if _, err := tx.db.Exec(ctx,
			`UPDATE "Deployment" SET "status" = 'deploying', "updatedAt" = now() WHERE "id" = $1`, id); err != nil {
			return err
		}
		res = ClaimWon
		return nil
	})
	if err != nil {
		return ClaimLost, err
	}
	return res, nil
}

func (s *Store) SetDeploymentStatus(ctx context.Context, id, status string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "status" = $2::"DeploymentStatus", "updatedAt" = now() WHERE "id" = $1`,
		id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkBuilt stores the pushed image and returns the deployment to queued for
// the deploy step (the same transition the Node worker used).
func (s *Store) MarkBuilt(ctx context.Context, id, image string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "image" = $2, "status" = 'queued', "updatedAt" = now() WHERE "id" = $1`,
		id, image)
	return rowRequired(tag, err)
}

// MarkFailed records a failed deployment unless it already reached a final
// state (deployed or failed). It returns true only when a row changed; a
// missing row and a final row both give (false, nil).
func (s *Store) MarkFailed(ctx context.Context, id string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "status" = 'failed', "updatedAt" = now()
		 WHERE "id" = $1 AND "status" NOT IN ('deployed', 'failed')`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// rowRequired maps an UPDATE that matched no row to ErrNotFound.
func rowRequired(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type DeployedInfo struct {
	ContainerID   string
	HostPort      *int // only for managed DBs exposed on 127.0.0.1 in dev
	ContainerPort int
	RuntimeURL    *string // nil for managed DBs (no HTTP route)
	VolumePath    *string // managed DB volume name; nil keeps the stored value
}

func (s *Store) MarkDeployed(ctx context.Context, id string, info DeployedInfo) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Deployment"
		 SET "containerId" = $2, "hostPort" = $3, "containerPort" = $4, "runtimeUrl" = $5,
		     "volumePath" = COALESCE($6, "volumePath"), "status" = 'deployed', "updatedAt" = now()
		 WHERE "id" = $1`,
		id, info.ContainerID, info.HostPort, info.ContainerPort, info.RuntimeURL, info.VolumePath)
	return rowRequired(tag, err)
}

// UsedHostPorts lists host ports held by deployments whose container still runs.
func (s *Store) UsedHostPorts(ctx context.Context) ([]int, error) {
	rows, err := s.db.Query(ctx,
		`SELECT "hostPort" FROM "Deployment" WHERE "hostPort" IS NOT NULL AND "stoppedAt" IS NULL ORDER BY "hostPort"`)
	return collect(rows, err, func(row pgx.Row) (int, error) {
		var p int
		err := row.Scan(&p)
		return p, err
	})
}

// MarkStopped records that these deployments' containers were removed.
func (s *Store) MarkStopped(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "stoppedAt" = now(), "updatedAt" = now()
		 WHERE "id" = ANY($1) AND "stoppedAt" IS NULL`, ids)
	return err
}

// ListLiveDeployments returns a service's deployments that still own a
// container, except exceptID.
func (s *Store) ListLiveDeployments(ctx context.Context, serviceID, exceptID string) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+deploymentCols+` FROM "Deployment"
		 WHERE "serviceId" = $1 AND "id" <> $2 AND "containerId" IS NOT NULL AND "stoppedAt" IS NULL
		 ORDER BY "createdAt"`, serviceID, exceptID)
	return collect(rows, err, scanDeployment)
}

// ListRunningDeployments returns deployed, not-stopped deployments with a
// container: the ones whose logs should be streamed.
func (s *Store) ListRunningDeployments(ctx context.Context) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+deploymentCols+` FROM "Deployment"
		 WHERE "status" = 'deployed' AND "containerId" IS NOT NULL AND "stoppedAt" IS NULL
		 ORDER BY "createdAt"`)
	return collect(rows, err, scanDeployment)
}

// Job is everything the deploy workflow needs about one deployment.
type Job struct {
	DeploymentID string
	Status       string
	CommitSha    string
	Image        *string
	VolumePath   *string
	ServiceID    string
	ServiceType  string
	RepoURL      *string
	Branch       string
	ProjectID    string
}

func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	var j Job
	err := s.db.QueryRow(ctx,
		`SELECT d."id", d."status"::text, d."commitSha", d."image", d."volumePath",
		        s."id", s."serviceType"::text, s."repoUrl", s."branch", s."projectId"
		 FROM "Deployment" d JOIN "Service" s ON s."id" = d."serviceId" WHERE d."id" = $1`, id).
		Scan(&j.DeploymentID, &j.Status, &j.CommitSha, &j.Image, &j.VolumePath,
			&j.ServiceID, &j.ServiceType, &j.RepoURL, &j.Branch, &j.ProjectID)
	return j, noRows(err)
}

// ownedByCaller limits deployments to live services in projects owned by $2.
const ownedByCaller = `"serviceId" IN (
	SELECT s."id" FROM "Service" s JOIN "Project" p ON p."id" = s."projectId"
	WHERE p."ownerUserId" = $2 AND s."deletedAt" IS NULL)`

func (s *Store) GetDeploymentForOwner(ctx context.Context, id, ownerUserID string) (Deployment, error) {
	return scanDeployment(s.db.QueryRow(ctx,
		`SELECT `+deploymentCols+` FROM "Deployment" WHERE "id" = $1 AND `+ownedByCaller, id, ownerUserID))
}

// ListDeploymentsForOwner returns the caller's 50 newest deployments, optionally for one service.
func (s *Store) ListDeploymentsForOwner(ctx context.Context, ownerUserID, serviceID string) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+deploymentCols+` FROM "Deployment"
		 WHERE ($1 = '' OR "serviceId" = $1) AND `+ownedByCaller+`
		 ORDER BY "createdAt" DESC LIMIT 50`, serviceID, ownerUserID)
	return collect(rows, err, scanDeployment)
}
