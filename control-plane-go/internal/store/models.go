package store

import "time"

// Timestamp marshals like JavaScript's Date.toISOString(), which is how Prisma
// returned dates: millisecond precision, UTC, "Z" suffix.
type Timestamp struct{ time.Time }

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.UTC().Format("2006-01-02T15:04:05.000Z07:00") + `"`), nil
}

const (
	StatusQueued    = "queued"
	StatusBuilding  = "building"
	StatusDeploying = "deploying"
	StatusDeployed  = "deployed"
	StatusFailed    = "failed"
)

const (
	TypeDocker  = "docker"
	TypeNodejs  = "nodejs"
	TypeNextjs  = "nextjs"
	TypeReact   = "react"
	TypeMongoDB = "mongodb"
	TypeRedis   = "redis"
)

// IsManaged reports whether the service type runs a stock image and skips the build.
func IsManaged(serviceType string) bool {
	return serviceType == TypeMongoDB || serviceType == TypeRedis
}

// NeedsRepo reports whether the service type is built from a git repository.
func NeedsRepo(serviceType string) bool {
	switch serviceType {
	case TypeDocker, TypeNodejs, TypeNextjs, TypeReact:
		return true
	}
	return false
}

func ValidServiceType(t string) bool { return NeedsRepo(t) || IsManaged(t) }

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Password  string    `json:"-"`
	Name      *string   `json:"name"`
	CreatedAt Timestamp `json:"createdAt"`
	UpdatedAt Timestamp `json:"updatedAt"`
}

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerUserID string    `json:"ownerUserId"`
	CreatedAt   Timestamp `json:"createdAt"`
}

type Service struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Name        string    `json:"name"`
	ServiceType string    `json:"serviceType"`
	RepoURL     *string   `json:"repoUrl"`
	Branch      string    `json:"branch"`
	CreatedAt   Timestamp `json:"createdAt"`
	PositionX   float64   `json:"positionX"`
	PositionY   float64   `json:"positionY"`
}

type EnvVar struct {
	ID        string    `json:"id"`
	ServiceID string    `json:"serviceId"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt Timestamp `json:"createdAt"`
}

type Deployment struct {
	ID            string     `json:"id"`
	ServiceID     string     `json:"serviceId"`
	CommitSha     string     `json:"commitSha"`
	Image         *string    `json:"image"`
	Status        string     `json:"status"`
	ContainerID   *string    `json:"containerId"`
	HostPort      *int       `json:"hostPort"`
	ContainerPort *int       `json:"containerPort"`
	RuntimeURL    *string    `json:"runtimeUrl"`
	VolumePath    *string    `json:"volumePath"`
	CreatedAt     Timestamp  `json:"createdAt"`
	UpdatedAt     Timestamp  `json:"updatedAt"`
	StoppedAt     *Timestamp `json:"stoppedAt"`
}

type ServiceConnection struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"sourceId"`
	TargetID  string    `json:"targetId"`
	Label     *string   `json:"label"`
	CreatedAt Timestamp `json:"createdAt"`
}

// OutboxEvent is a claimed row waiting to be published.
type OutboxEvent struct {
	ID       string
	Type     string
	Payload  []byte
	Attempts int
}
