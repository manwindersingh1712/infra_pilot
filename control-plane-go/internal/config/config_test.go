package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var validKey = base64.StdEncoding.EncodeToString(make([]byte, 32))

// mustLoad fails the test if Load returns an error.
func mustLoad(t *testing.T, m map[string]string) Config {
	t.Helper()
	c, err := Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestLoadAppliesDefaults(t *testing.T) {
	c := mustLoad(t, map[string]string{"DATABASE_URL": "postgres://x"})
	checks := []struct {
		name      string
		got, want any
	}{
		{"AppEnv", c.AppEnv, "production"},
		{"LogFormat", c.LogFormat, "text"},
		{"APIPort", c.APIPort, "8080"},
		{"APIMetricsAddr", c.APIMetricsAddr, "127.0.0.1:9090"},
		{"WorkerMetricsAddr", c.WorkerMetricsAddr, "127.0.0.1:9091"},
		{"TemporalHostPort", c.TemporalHostPort, "localhost:7233"},
		{"TemporalNamespace", c.TemporalNamespace, "default"},
		{"RedisURL", c.RedisURL, "redis://localhost:6379"},
		{"ClickHouseURL", c.ClickHouseURL, "http://localhost:8123"},
		{"ClickHouseUser", c.ClickHouseUser, "default"},
		{"RegistryHost", c.RegistryHost, "localhost:5000"},
		{"BuildWorkdir", c.BuildWorkdir, "/tmp/cp-builds"},
		{"BuilderName", c.BuilderName, "cp-builder"},
		{"BuilderMemory", c.BuilderMemory, "2g"},
		{"BuilderCPUs", c.BuilderCPUs, 2},
		{"ContainerMemory", c.ContainerMemory, "512m"},
		{"ContainerCPUs", c.ContainerCPUs, "0.5"},
		{"ContainerPids", c.ContainerPids, 256},
		{"DefaultContainerPort", c.DefaultContainerPort, 3080},
		{"ExposeManagedPorts", c.ExposeManagedPorts, false},
		{"PortRangeStart", c.PortRangeStart, 20000},
		{"PortRangeEnd", c.PortRangeEnd, 20100},
		{"LegacyVolumeRoot", c.LegacyVolumeRoot, "/tmp/cp-volumes"},
		{"ProbeImage", c.ProbeImage, "busybox:1.36"},
		{"HealthTimeout", c.HealthTimeout, 60 * time.Second},
		{"NginxPort", c.NginxPort, "80"},
		{"NginxRoutesDir", c.NginxRoutesDir, "infra/nginx/conf.d/routes"},
		{"NginxContainer", c.NginxContainer, "cp_nginx"},
		{"ServiceBaseDomain", c.ServiceBaseDomain, "localhost"},
		{"CORSOrigins", strings.Join(c.CORSOrigins, ","), "http://localhost:5173"},
		{"IsDevelopment", c.IsDevelopment(), false},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

// The activity timeout is a fixed 11 minutes, so the probe deadline is capped at 10.
func TestLoadAcceptsHealthTimeoutAtTheCap(t *testing.T) {
	if c := mustLoad(t, map[string]string{"DATABASE_URL": "postgres://x", "HEALTH_TIMEOUT_SECONDS": "600"}); c.HealthTimeout != 600*time.Second {
		t.Fatalf("HealthTimeout = %s", c.HealthTimeout)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	c := mustLoad(t, map[string]string{
		"DATABASE_URL": " postgres://x ", "NODE_ENV": "production", "LOG_FORMAT": "json",
		"REGISTRY_HOST": "localhost:5001", "CORS_ORIGINS": "https://a.test, https://b.test",
		"HEALTH_TIMEOUT_SECONDS": " 90 ", "ENV_ENCRYPTION_KEY": validKey + "\n",
		"JWT_SECRET": "  s  ", "CLICKHOUSE_PASSWORD": " pw ", "CONTAINER_PIDS": "100",
	})
	checks := []struct {
		name      string
		got, want any
	}{
		{"AppEnv", c.AppEnv, "production"},
		{"LogFormat", c.LogFormat, "json"},
		{"RegistryHost", c.RegistryHost, "localhost:5001"},
		{"ExposeManagedPorts", c.ExposeManagedPorts, false},
		{"HealthTimeout", c.HealthTimeout, 90 * time.Second},
		{"EnvEncryptionKey length", len(c.EnvEncryptionKey), 32},
		{"CORSOrigins", strings.Join(c.CORSOrigins, ","), "https://a.test,https://b.test"},
		{"DatabaseURL (trimmed)", c.DatabaseURL, "postgres://x"},
		{"JWTSecret (trimmed)", c.JWTSecret, "s"},
		{"ClickHousePassword (not trimmed)", c.ClickHousePassword, " pw "},
		{"ContainerPids", c.ContainerPids, 100},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

func TestAppEnv(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		wantDev     bool
		wantExposed bool
	}{
		{"unset means production", map[string]string{}, false, false},
		{"APP_ENV=development", map[string]string{"APP_ENV": "development"}, true, true},
		{"NODE_ENV=development (Node .env)", map[string]string{"NODE_ENV": "development"}, true, true},
		{"APP_ENV wins over NODE_ENV", map[string]string{"APP_ENV": "development", "NODE_ENV": "production"}, true, true},
		{"APP_ENV=production wins", map[string]string{"APP_ENV": "production", "NODE_ENV": "development"}, false, false},
		{"dev can turn exposure off", map[string]string{"APP_ENV": "development", "EXPOSE_MANAGED_PORTS": "false"}, true, false},
	}
	for _, tc := range cases {
		tc.env["DATABASE_URL"] = "x"
		c := mustLoad(t, tc.env)
		if c.IsDevelopment() != tc.wantDev || c.ExposeManagedPorts != tc.wantExposed {
			t.Errorf("%s: IsDevelopment=%v ExposeManagedPorts=%v, want %v %v",
				tc.name, c.IsDevelopment(), c.ExposeManagedPorts, tc.wantDev, tc.wantExposed)
		}
	}
}

func TestCORSOriginFallback(t *testing.T) {
	c := mustLoad(t, map[string]string{"DATABASE_URL": "x", "CORS_ORIGIN": "https://old.test"})
	if got := strings.Join(c.CORSOrigins, ","); got != "https://old.test" {
		t.Errorf("CORS_ORIGIN fallback: got %q", got)
	}
	c = mustLoad(t, map[string]string{"DATABASE_URL": "x", "CORS_ORIGIN": "https://old.test", "CORS_ORIGINS": "https://new.test"})
	if got := strings.Join(c.CORSOrigins, ","); got != "https://new.test" {
		t.Errorf("CORS_ORIGINS must win: got %q", got)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantMsg string
	}{
		{"missing DATABASE_URL", map[string]string{}, "missing required env: DATABASE_URL"},
		{"whitespace DATABASE_URL", map[string]string{"DATABASE_URL": "  "}, "missing required env: DATABASE_URL"},
		{"non-integer container port", map[string]string{"DEFAULT_CONTAINER_PORT": "abc"}, "DEFAULT_CONTAINER_PORT must be an integer"},
		{"container port 0", map[string]string{"DEFAULT_CONTAINER_PORT": "0"}, "DEFAULT_CONTAINER_PORT must be an integer"},
		{"container port 65536", map[string]string{"DEFAULT_CONTAINER_PORT": "65536"}, "DEFAULT_CONTAINER_PORT must be an integer"},
		{"range start 0", map[string]string{"DATA_PLANE_PORT_RANGE_START": "0"}, "DATA_PLANE_PORT_RANGE_START must be an integer"},
		{"range end 70000", map[string]string{"DATA_PLANE_PORT_RANGE_END": "70000"}, "DATA_PLANE_PORT_RANGE_END must be an integer"},
		{"range start > end", map[string]string{"DATA_PLANE_PORT_RANGE_START": "30000", "DATA_PLANE_PORT_RANGE_END": "20000"}, "must not be greater than DATA_PLANE_PORT_RANGE_END"},
		{"health timeout 0", map[string]string{"HEALTH_TIMEOUT_SECONDS": "0"}, "HEALTH_TIMEOUT_SECONDS must be an integer from 1 to 600"},
		{"health timeout -5", map[string]string{"HEALTH_TIMEOUT_SECONDS": "-5"}, "HEALTH_TIMEOUT_SECONDS must be an integer from 1 to 600"},
		{"health timeout too big", map[string]string{"HEALTH_TIMEOUT_SECONDS": "99999999999999"}, "HEALTH_TIMEOUT_SECONDS must be an integer from 1 to 600"},
		{"health timeout 601", map[string]string{"HEALTH_TIMEOUT_SECONDS": "601"}, "HEALTH_TIMEOUT_SECONDS must be an integer from 1 to 600"},
		{"pids 0 (unlimited)", map[string]string{"CONTAINER_PIDS": "0"}, "CONTAINER_PIDS must be an integer"},
		{"pids -1 (unlimited)", map[string]string{"CONTAINER_PIDS": "-1"}, "CONTAINER_PIDS must be an integer"},
		{"builder cpus 0", map[string]string{"BUILDER_CPUS": "0"}, "BUILDER_CPUS must be an integer"},
		{"container cpus 0 (unlimited)", map[string]string{"CONTAINER_CPUS": "0"}, "CONTAINER_CPUS must be a number of CPUs"},
		{"container cpus over 1024", map[string]string{"CONTAINER_CPUS": "2000"}, "CONTAINER_CPUS must be a number of CPUs greater than 0 and at most 1024"},
		{"container cpus text", map[string]string{"CONTAINER_CPUS": "half"}, "CONTAINER_CPUS must be a number of CPUs"},
		{"container memory 0 (unlimited)", map[string]string{"CONTAINER_MEMORY": "0"}, "CONTAINER_MEMORY must be a positive size"},
		{"container memory unit", map[string]string{"CONTAINER_MEMORY": "512mb"}, "CONTAINER_MEMORY must be a positive size"},
		{"builder memory", map[string]string{"BUILDER_MEMORY": "-1"}, "BUILDER_MEMORY must be a positive size"},
		{"api port text", map[string]string{"API_PORT": "http"}, "API_PORT must be a port"},
		{"nginx port 0", map[string]string{"NGINX_PORT": "0"}, "NGINX_PORT must be a port"},
		{"non-boolean expose", map[string]string{"EXPOSE_MANAGED_PORTS": "maybe"}, "EXPOSE_MANAGED_PORTS must be true or false"},
		{"expose in production", map[string]string{"APP_ENV": "production", "EXPOSE_MANAGED_PORTS": "true"}, "EXPOSE_MANAGED_PORTS=true is not allowed when APP_ENV=production"},
		{"expose with env unset", map[string]string{"EXPOSE_MANAGED_PORTS": "true"}, "EXPOSE_MANAGED_PORTS=true is not allowed when APP_ENV=production"},
		{"unknown APP_ENV", map[string]string{"APP_ENV": "prod"}, "APP_ENV (or NODE_ENV) must be one of development, production"},
		{"unknown NODE_ENV", map[string]string{"NODE_ENV": "test"}, "APP_ENV (or NODE_ENV) must be one of development, production"},
		{"unknown LOG_FORMAT", map[string]string{"LOG_FORMAT": "logfmt"}, "LOG_FORMAT must be one of text, json"},
		{"wildcard CORS", map[string]string{"CORS_ORIGINS": "https://a.test,*"}, "* is not allowed"},
		{"short encryption key", map[string]string{"ENV_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString([]byte("short"))}, "ENV_ENCRYPTION_KEY must decode to 32 bytes, got 5"},
		{"non-base64 encryption", map[string]string{"ENV_ENCRYPTION_KEY": "%%%"}, "ENV_ENCRYPTION_KEY must be base64"},
	}
	for _, tc := range cases {
		if _, ok := tc.env["DATABASE_URL"]; !ok && tc.name != "missing DATABASE_URL" {
			tc.env["DATABASE_URL"] = "x"
		}
		_, err := Load(env(tc.env))
		if err == nil {
			t.Errorf("%s: expected an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("%s: error %q does not contain %q", tc.name, err, tc.wantMsg)
		}
	}
}

func TestRequireWorker(t *testing.T) {
	c := mustLoad(t, map[string]string{"DATABASE_URL": "x"})
	if err := c.RequireWorker(); err == nil || !strings.Contains(err.Error(), "ENV_ENCRYPTION_KEY is required") {
		t.Fatalf("missing key: got %v", err)
	}
	c = mustLoad(t, map[string]string{"DATABASE_URL": "x", "ENV_ENCRYPTION_KEY": validKey})
	if err := c.RequireWorker(); err != nil {
		t.Fatal(err)
	}
}

func TestRequireAPI(t *testing.T) {
	strong := strings.Repeat("k", 32)
	cases := []struct {
		name    string
		env     map[string]string
		wantMsg string // "" means no error
	}{
		{"missing key", map[string]string{"JWT_SECRET": strong}, "ENV_ENCRYPTION_KEY is required"},
		{"missing secret", map[string]string{"ENV_ENCRYPTION_KEY": validKey}, "JWT_SECRET is required"},
		{"whitespace secret", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "   "}, "JWT_SECRET is required"},
		{"production short", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "s"}, "at least 32 bytes"},
		{"production dev placeholder", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "dev-secret-change-me"}, "placeholder"},
		{"production example placeholder", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "generate-with-openssl-rand-base64-32"}, "placeholder"},
		{"production strong", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": strong}, ""},
		{"development placeholder ok", map[string]string{"APP_ENV": "development", "ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "dev-secret-change-me"}, ""},
		{"development short ok", map[string]string{"APP_ENV": "development", "ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "s"}, ""},
	}
	for _, tc := range cases {
		tc.env["DATABASE_URL"] = "x"
		err := mustLoad(t, tc.env).RequireAPI()
		switch {
		case tc.wantMsg == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.wantMsg)):
			t.Errorf("%s: error %v does not contain %q", tc.name, err, tc.wantMsg)
		}
	}
}

func TestSecretsNeverPrinted(t *testing.T) {
	secrets := map[string]string{
		"jwt":        "jwt-secret-value-0123456789abcdefghij",
		"key":        validKey,
		"dbPassword": "dbpass123",
		"redis":      "redispass456",
		"clickhouse": "chpass789",
	}
	c := mustLoad(t, map[string]string{
		"DATABASE_URL":        "postgresql://cp:" + secrets["dbPassword"] + "@db:5432/cp",
		"REDIS_URL":           "redis://:" + secrets["redis"] + "@redis:6379",
		"JWT_SECRET":          secrets["jwt"],
		"ENV_ENCRYPTION_KEY":  secrets["key"],
		"CLICKHOUSE_PASSWORD": secrets["clickhouse"],
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("config", "cfg", c)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("config", "cfg", c)
	fmt.Fprintf(&buf, "%v %+v %s", c, c, c)
	out := buf.String()
	for name, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("%s secret leaked: %s", name, out)
		}
	}
	for _, want := range []string{"appEnv", "production", "jwtSecretSet", "db:5432"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %s", want, out)
		}
	}
}

func TestRedactURLMasksQueryValues(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"http://ch:8123/?password=SECRET&user=a", "http://ch:8123/?password=xxxxx&user=xxxxx"},
		{"postgresql://u:pw@db:5432/x?sslpassword=SECRET&sslmode=disable#frag", "postgresql://u:xxxxx@db:5432/x?sslpassword=xxxxx&sslmode=xxxxx"},
		{"redis://:pw@r:6379/0", "redis://:xxxxx@r:6379/0"},
		{"", ""},
		{"http://a b/%zz", "[unparseable]"},
	} {
		got := redactURL(c.in)
		if got != c.want {
			t.Errorf("redactURL(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "SECRET") {
			t.Errorf("secret leaked: %q", got)
		}
	}
}
