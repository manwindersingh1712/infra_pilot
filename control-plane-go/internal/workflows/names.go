// Package workflows holds the Temporal workflows that build and run
// deployments, their activities, and the starter used by the outbox.
package workflows

import "infrapilot/internal/store"

const TaskQueue = "deployments"

func DeployWorkflowID(deploymentID string) string { return "deploy-" + deploymentID }
func ContainerName(deploymentID string) string    { return "cp-" + deploymentID }
func ServiceAlias(serviceID string) string        { return "svc-" + serviceID }
func NetworkName(projectID string) string         { return "cp-proj-" + projectID }
func VolumeName(serviceID string) string          { return "cp-vol-" + serviceID }

// ManagedImage describes a managed database service.
type ManagedImage struct {
	Image       string
	Port        int
	PasswordEnv string            // env var holding the generated password
	Env         map[string]string // defaults; user env vars override them
	Cmd         []string
	DataDir     string   // where the image keeps its data; the named volume mounts here
	Tmpfs       []string // image-declared VOLUME paths we do not persist (no anonymous volumes)
	CapAdd      []string // capabilities added back after --cap-drop ALL, verified against the real image
}

var Managed = map[string]ManagedImage{
	store.TypeRedis: {
		Image: "redis:7-alpine", Port: 6379, PasswordEnv: "REDIS_PASSWORD",
		// The password is expanded inside the container, so it never appears
		// in `docker inspect` output or the host's process list.
		Cmd:     []string{"sh", "-c", `exec redis-server --appendonly yes --dir /data --requirepass "$REDIS_PASSWORD"`},
		DataDir: "/data",
		// The command runs as root (sh, not redis-server, so the image entrypoint
		// does not drop privileges); without DAC_OVERRIDE root cannot write the
		// redis-owned volume once all capabilities are dropped.
		CapAdd: []string{"DAC_OVERRIDE"},
	},
	store.TypeMongoDB: { // #nosec G101 -- PasswordEnv is an env var name, not a credential
		Image: "mongo:7", Port: 27017, PasswordEnv: "MONGO_INITDB_ROOT_PASSWORD",
		Env:     map[string]string{"MONGO_INITDB_ROOT_USERNAME": "admin"},
		DataDir: "/data/db",
		Tmpfs:   []string{"/data/configdb"},
		// The entrypoint chowns the data dir and drops to mongodb with gosu;
		// DAC_READ_SEARCH lets root read journal files on an existing volume.
		CapAdd: []string{"CHOWN", "SETUID", "SETGID", "DAC_READ_SEARCH"},
	},
}
