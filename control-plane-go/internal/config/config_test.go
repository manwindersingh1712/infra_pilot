package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var validKey = base64.StdEncoding.EncodeToString(make([]byte, 32))

func TestLoadAppliesDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"AppEnv", c.AppEnv, "development"},
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
		{"ExposeManagedPorts", c.ExposeManagedPorts, true},
		{"PortRangeStart", c.PortRangeStart, 20000},
		{"PortRangeEnd", c.PortRangeEnd, 20100},
		{"LegacyVolumeRoot", c.LegacyVolumeRoot, "/tmp/cp-volumes"},
		{"ProbeImage", c.ProbeImage, "busybox:1.36"},
		{"HealthTimeout", c.HealthTimeout, 60 * time.Second},
		{"NginxPort", c.NginxPort, "80"},
		{"NginxRoutesDir", c.NginxRoutesDir, "infra/nginx/conf.d/routes"},
		{"NginxContainer", c.NginxContainer, "cp_nginx"},
		{"ServiceBaseDomain", c.ServiceBaseDomain, "localhost"},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
	if strings.Join(c.CORSOrigins, ",") != "http://localhost:5173" {
		t.Errorf("CORSOrigins = %v", c.CORSOrigins)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"DATABASE_URL": "postgres://x", "NODE_ENV": "production", "LOG_FORMAT": "json",
		"REGISTRY_HOST": "localhost:5001", "CORS_ORIGINS": "https://a.test, https://b.test",
		"HEALTH_TIMEOUT_SECONDS": "90", "ENV_ENCRYPTION_KEY": validKey,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AppEnv != "production" || c.LogFormat != "json" || c.RegistryHost != "localhost:5001" ||
		c.ExposeManagedPorts || c.HealthTimeout != 90*time.Second || len(c.EnvEncryptionKey) != 32 ||
		strings.Join(c.CORSOrigins, ",") != "https://a.test,https://b.test" {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestAppEnvPrefersAppEnvOverNodeEnv(t *testing.T) {
	c, _ := Load(env(map[string]string{"DATABASE_URL": "x", "APP_ENV": "development", "NODE_ENV": "production"}))
	if !c.IsDevelopment() {
		t.Fatal("APP_ENV should win over NODE_ENV")
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"missing DATABASE_URL":  {},
		"non-integer port":      {"DATABASE_URL": "x", "DEFAULT_CONTAINER_PORT": "abc"},
		"non-boolean expose":    {"DATABASE_URL": "x", "EXPOSE_MANAGED_PORTS": "maybe"},
		"short encryption key":  {"DATABASE_URL": "x", "ENV_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString([]byte("short"))},
		"non-base64 encryption": {"DATABASE_URL": "x", "ENV_ENCRYPTION_KEY": "%%%"},
	}
	for name, m := range cases {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRequireWorkerAndAPI(t *testing.T) {
	c, _ := Load(env(map[string]string{"DATABASE_URL": "x"}))
	if c.RequireWorker() == nil || c.RequireAPI() == nil {
		t.Fatal("missing ENV_ENCRYPTION_KEY must fail")
	}
	c, _ = Load(env(map[string]string{"DATABASE_URL": "x", "ENV_ENCRYPTION_KEY": validKey}))
	if err := c.RequireWorker(); err != nil {
		t.Fatal(err)
	}
	if c.RequireAPI() == nil {
		t.Fatal("API also needs JWT_SECRET")
	}
	c.JWTSecret = "s"
	if err := c.RequireAPI(); err != nil {
		t.Fatal(err)
	}
}
