// Package config loads process configuration from environment variables.
// Names shared with the Node backend keep their meaning and defaults.
package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv    string // development | production (APP_ENV, falling back to NODE_ENV; unset means production)
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

	NginxPort string
	// NginxRoutesDir is resolved against the process working directory when
	// relative. The dev .env sets it explicitly; the worker logs the absolute path.
	NginxRoutesDir    string
	NginxContainer    string
	ServiceBaseDomain string
}

const maxHealthTimeoutSeconds = 600 // below the 11 minute WaitHealthy activity timeout in internal/workflows

// Docker size: a positive integer with an optional b/k/m/g unit, e.g. 512m.
var dockerSize = regexp.MustCompile(`^[1-9][0-9]*[bkmgBKMG]?$`)

// Placeholders that must never be used as a JWT secret outside development.
var placeholderSecrets = []string{"dev-secret-change-me", "generate-with-openssl-rand-base64-32"}

// Load reads configuration through getenv (os.Getenv in production).
// Every value except CLICKHOUSE_PASSWORD is trimmed of surrounding whitespace.
func Load(getenv func(string) string) (Config, error) {
	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	var invalid []string
	num := func(key string, def, lo, hi int) int {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			invalid = append(invalid, fmt.Sprintf("%s must be an integer from %d to %d, got %q", key, lo, hi, v))
		}
		return n
	}
	flag := func(key string, def bool) bool {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			invalid = append(invalid, fmt.Sprintf("%s must be true or false, got %q", key, v))
		}
		return b
	}
	oneOf := func(key, v string, allowed ...string) string {
		if !slices.Contains(allowed, v) {
			invalid = append(invalid, fmt.Sprintf("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), v))
		}
		return v
	}
	port := func(key, def string) string {
		v := str(key, def)
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			invalid = append(invalid, fmt.Sprintf("%s must be a port from 1 to 65535, got %q", key, v))
		}
		return v
	}
	size := func(key, def string) string {
		v := str(key, def)
		if !dockerSize.MatchString(v) {
			invalid = append(invalid, fmt.Sprintf("%s must be a positive size like 512m or 2g, got %q", key, v))
		}
		return v
	}
	cpus := func(key, def string) string {
		v := str(key, def)
		if f, err := strconv.ParseFloat(v, 64); err != nil || !(f > 0) || f > 1024 {
			invalid = append(invalid, fmt.Sprintf("%s must be a number of CPUs greater than 0 and at most 1024, got %q", key, v))
		}
		return v
	}

	appEnv := oneOf("APP_ENV (or NODE_ENV)", str("APP_ENV", str("NODE_ENV", "production")), "development", "production")
	c := Config{
		AppEnv:    appEnv,
		LogFormat: oneOf("LOG_FORMAT", str("LOG_FORMAT", "text"), "text", "json"),

		APIPort:           port("API_PORT", "8080"),
		DatabaseURL:       strings.TrimSpace(getenv("DATABASE_URL")),
		JWTSecret:         strings.TrimSpace(getenv("JWT_SECRET")),
		CORSOrigins:       splitList(str("CORS_ORIGINS", str("CORS_ORIGIN", "http://localhost:5173"))),
		APIMetricsAddr:    str("API_METRICS_ADDR", "127.0.0.1:9090"),
		WorkerMetricsAddr: str("WORKER_METRICS_ADDR", "127.0.0.1:9091"),

		TemporalHostPort:  str("TEMPORAL_HOST_PORT", "localhost:7233"),
		TemporalNamespace: str("TEMPORAL_NAMESPACE", "default"),

		RedisURL:           str("REDIS_URL", "redis://localhost:6379"),
		ClickHouseURL:      str("CLICKHOUSE_URL", "http://localhost:8123"),
		ClickHouseUser:     str("CLICKHOUSE_USER", "default"),
		ClickHousePassword: getenv("CLICKHOUSE_PASSWORD"), // not trimmed: spaces may be part of it

		RegistryHost:  str("REGISTRY_HOST", "localhost:5001"),
		BuildWorkdir:  str("BUILD_WORKDIR", "/tmp/cp-builds"),
		BuilderName:   str("BUILDER_NAME", "cp-builder"),
		BuilderMemory: size("BUILDER_MEMORY", "2g"),
		BuilderCPUs:   num("BUILDER_CPUS", 2, 1, 1024),

		ContainerMemory:      size("CONTAINER_MEMORY", "512m"),
		ContainerCPUs:        cpus("CONTAINER_CPUS", "0.5"),
		ContainerPids:        num("CONTAINER_PIDS", 256, 1, 1<<22),
		DefaultContainerPort: num("DEFAULT_CONTAINER_PORT", 3080, 1, 65535),
		ExposeManagedPorts:   flag("EXPOSE_MANAGED_PORTS", appEnv == "development"),
		PortRangeStart:       num("DATA_PLANE_PORT_RANGE_START", 20000, 1, 65535),
		PortRangeEnd:         num("DATA_PLANE_PORT_RANGE_END", 20100, 1, 65535),
		LegacyVolumeRoot:     str("LEGACY_VOLUME_ROOT", "/tmp/cp-volumes"),
		ProbeImage:           str("PROBE_IMAGE", "busybox:1.36"),
		HealthTimeout:        time.Duration(num("HEALTH_TIMEOUT_SECONDS", 60, 1, maxHealthTimeoutSeconds)) * time.Second,

		NginxPort:         port("NGINX_PORT", "80"),
		NginxRoutesDir:    str("NGINX_ROUTES_DIR", "infra/nginx/conf.d/routes"),
		NginxContainer:    str("NGINX_CONTAINER", "cp_nginx"),
		ServiceBaseDomain: str("SERVICE_BASE_DOMAIN", "localhost"),
	}

	if c.PortRangeStart > c.PortRangeEnd {
		invalid = append(invalid, fmt.Sprintf("DATA_PLANE_PORT_RANGE_START (%d) must not be greater than DATA_PLANE_PORT_RANGE_END (%d)", c.PortRangeStart, c.PortRangeEnd))
	}
	if c.ExposeManagedPorts && appEnv == "production" {
		invalid = append(invalid, "EXPOSE_MANAGED_PORTS=true is not allowed when APP_ENV=production")
	}
	if slices.Contains(c.CORSOrigins, "*") {
		invalid = append(invalid, "CORS_ORIGINS must list exact origins; * is not allowed")
	}
	if raw := strings.TrimSpace(getenv("ENV_ENCRYPTION_KEY")); raw != "" {
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
	if !c.IsDevelopment() {
		if slices.Contains(placeholderSecrets, c.JWTSecret) {
			return fmt.Errorf("config: JWT_SECRET is a placeholder; generate one with: openssl rand -base64 32")
		}
		if len(c.JWTSecret) < 32 {
			return fmt.Errorf("config: JWT_SECRET must be at least 32 bytes outside development, got %d", len(c.JWTSecret))
		}
	}
	return nil
}

// LogValue keeps secrets out of logs: it reports whether each secret is set,
// and URLs with any password replaced.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("appEnv", c.AppEnv),
		slog.String("logFormat", c.LogFormat),
		slog.String("apiPort", c.APIPort),
		slog.String("databaseURL", redactURL(c.DatabaseURL)),
		slog.Bool("jwtSecretSet", c.JWTSecret != ""),
		slog.Bool("envEncryptionKeySet", len(c.EnvEncryptionKey) > 0),
		slog.Any("corsOrigins", c.CORSOrigins),
		slog.String("temporalHostPort", c.TemporalHostPort),
		slog.String("temporalNamespace", c.TemporalNamespace),
		slog.String("redisURL", redactURL(c.RedisURL)),
		slog.String("clickHouseURL", redactURL(c.ClickHouseURL)),
		slog.String("clickHouseUser", c.ClickHouseUser),
		slog.Bool("clickHousePasswordSet", c.ClickHousePassword != ""),
		slog.String("registryHost", c.RegistryHost),
		slog.Bool("exposeManagedPorts", c.ExposeManagedPorts),
		slog.String("nginxRoutesDir", c.NginxRoutesDir),
		slog.String("serviceBaseDomain", c.ServiceBaseDomain),
	)
}

// String makes fmt print the same redacted view as LogValue.
func (c Config) String() string { return c.LogValue().String() }

func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparseable]"
	}
	// Mask query values (T26 SEC-3): ?password= and DSN sslpassword= must not
	// reach logs. Keys stay, order is kept, the fragment is dropped.
	if u.RawQuery != "" {
		pairs := strings.Split(u.RawQuery, "&")
		for i, p := range pairs {
			if k, _, ok := strings.Cut(p, "="); ok {
				pairs[i] = k + "=xxxxx"
			}
		}
		u.RawQuery = strings.Join(pairs, "&")
	}
	u.Fragment, u.RawFragment = "", ""
	return u.Redacted()
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
