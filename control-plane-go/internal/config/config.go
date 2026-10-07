// Package config loads process configuration from environment variables.
// Names shared with the Node backend keep their meaning and defaults.
package config

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv    string // development | production (APP_ENV, falling back to NODE_ENV)
	LogFormat string // text | json

	APIPort           string
	DatabaseURL       string
	JWTSecret         string
	EnvEncryptionKey  []byte // 32 bytes, from base64 ENV_ENCRYPTION_KEY
	CORSOrigins       []string
	APIMetricsAddr    string
	WorkerMetricsAddr string

	TemporalHostPort  string
	TemporalNamespace string

	RedisURL           string
	ClickHouseURL      string
	ClickHouseUser     string
	ClickHousePassword string

	RegistryHost  string
	BuildWorkdir  string
	BuilderName   string
	BuilderMemory string
	BuilderCPUs   int

	ContainerMemory      string
	ContainerCPUs        string
	ContainerPids        int
	DefaultContainerPort int
	ExposeManagedPorts   bool // publish managed DB ports on 127.0.0.1 (dev convenience)
	PortRangeStart       int
	PortRangeEnd         int
	LegacyVolumeRoot     string // where the Node worker kept managed DB data
	ProbeImage           string
	HealthTimeout        time.Duration

	NginxPort         string
	NginxRoutesDir    string
	NginxContainer    string
	ServiceBaseDomain string
}

// Load reads configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	var invalid []string
	num := func(key string, def int) int {
		v := getenv(key)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			invalid = append(invalid, fmt.Sprintf("%s must be an integer, got %q", key, v))
		}
		return n
	}
	flag := func(key string, def bool) bool {
		v := getenv(key)
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			invalid = append(invalid, fmt.Sprintf("%s must be true or false, got %q", key, v))
		}
		return b
	}

	appEnv := str("APP_ENV", str("NODE_ENV", "development"))
	c := Config{
		AppEnv:    appEnv,
		LogFormat: str("LOG_FORMAT", "text"),

		APIPort:           str("API_PORT", "8080"),
		DatabaseURL:       getenv("DATABASE_URL"),
		JWTSecret:         getenv("JWT_SECRET"),
		CORSOrigins:       splitList(str("CORS_ORIGINS", "http://localhost:5173")),
		APIMetricsAddr:    str("API_METRICS_ADDR", "127.0.0.1:9090"),
		WorkerMetricsAddr: str("WORKER_METRICS_ADDR", "127.0.0.1:9091"),

		TemporalHostPort:  str("TEMPORAL_HOST_PORT", "localhost:7233"),
		TemporalNamespace: str("TEMPORAL_NAMESPACE", "default"),

		RedisURL:           str("REDIS_URL", "redis://localhost:6379"),
		ClickHouseURL:      str("CLICKHOUSE_URL", "http://localhost:8123"),
		ClickHouseUser:     str("CLICKHOUSE_USER", "default"),
		ClickHousePassword: getenv("CLICKHOUSE_PASSWORD"),

		RegistryHost:  str("REGISTRY_HOST", "localhost:5000"),
		BuildWorkdir:  str("BUILD_WORKDIR", "/tmp/cp-builds"),
		BuilderName:   str("BUILDER_NAME", "cp-builder"),
		BuilderMemory: str("BUILDER_MEMORY", "2g"),
		BuilderCPUs:   num("BUILDER_CPUS", 2),

		ContainerMemory:      str("CONTAINER_MEMORY", "512m"),
		ContainerCPUs:        str("CONTAINER_CPUS", "0.5"),
		ContainerPids:        num("CONTAINER_PIDS", 256),
		DefaultContainerPort: num("DEFAULT_CONTAINER_PORT", 3080),
		ExposeManagedPorts:   flag("EXPOSE_MANAGED_PORTS", appEnv == "development"),
		PortRangeStart:       num("DATA_PLANE_PORT_RANGE_START", 20000),
		PortRangeEnd:         num("DATA_PLANE_PORT_RANGE_END", 20100),
		LegacyVolumeRoot:     str("LEGACY_VOLUME_ROOT", "/tmp/cp-volumes"),
		ProbeImage:           str("PROBE_IMAGE", "busybox:1.36"),
		HealthTimeout:        time.Duration(num("HEALTH_TIMEOUT_SECONDS", 60)) * time.Second,

		NginxPort:         str("NGINX_PORT", "80"),
		NginxRoutesDir:    str("NGINX_ROUTES_DIR", "infra/nginx/conf.d/routes"),
		NginxContainer:    str("NGINX_CONTAINER", "cp_nginx"),
		ServiceBaseDomain: str("SERVICE_BASE_DOMAIN", "localhost"),
	}

	if raw := getenv("ENV_ENCRYPTION_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		switch {
		case err != nil:
			invalid = append(invalid, "ENV_ENCRYPTION_KEY must be base64")
		case len(key) != 32:
			invalid = append(invalid, fmt.Sprintf("ENV_ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key)))
		default:
			c.EnvEncryptionKey = key
		}
	}
	if c.DatabaseURL == "" {
		invalid = append(invalid, "missing required env: DATABASE_URL")
	}
	if len(invalid) > 0 {
		return Config{}, fmt.Errorf("config: %s", strings.Join(invalid, "; "))
	}
	return c, nil
}

func (c Config) IsDevelopment() bool { return c.AppEnv == "development" }

// RequireWorker checks the settings only the worker needs.
func (c Config) RequireWorker() error {
	if len(c.EnvEncryptionKey) != 32 {
		return fmt.Errorf("config: ENV_ENCRYPTION_KEY is required (generate with: openssl rand -base64 32)")
	}
	return nil
}

// RequireAPI checks the settings the API needs.
func (c Config) RequireAPI() error {
	if err := c.RequireWorker(); err != nil {
		return err
	}
	if c.JWTSecret == "" {
		return fmt.Errorf("config: JWT_SECRET is required")
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
