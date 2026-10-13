package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"infrapilot/internal/config"
	"infrapilot/internal/db"
	"infrapilot/internal/logging"
	"infrapilot/internal/testutil"
)

// syncBuffer is a log sink that run's goroutines and the test can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const (
	testJWT = "run-test-jwt-secret-value-0123456789abcdef"
	testKey = "cnVuLXRlc3QtZW5jcnlwdGlvbi1rZXktMzItYnl0ZXM=" // 32 bytes
)

func loadCfg(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// run must refuse to start, before it touches any backend, when a secret is
// missing. No Docker needed.
func TestRunRefusesMissingSecrets(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"no ENV_ENCRYPTION_KEY":   {map[string]string{"JWT_SECRET": testJWT}, "ENV_ENCRYPTION_KEY"},
		"no JWT_SECRET":           {map[string]string{"ENV_ENCRYPTION_KEY": key}, "JWT_SECRET"},
		"placeholder JWT":         {map[string]string{"ENV_ENCRYPTION_KEY": key, "JWT_SECRET": "dev-secret-change-me", "APP_ENV": "production", "CORS_ORIGINS": "https://ui.test"}, "placeholder"},
		"short JWT in production": {map[string]string{"ENV_ENCRYPTION_KEY": key, "JWT_SECRET": "short", "APP_ENV": "production", "CORS_ORIGINS": "https://ui.test"}, "at least 32"},
	}
	for name, c := range cases {
		env := map[string]string{"DATABASE_URL": "postgres://cp:pw@127.0.0.1:1/x", "API_METRICS_ADDR": "127.0.0.1:0", "CORS_ORIGINS": "https://ui.test"}
		for k, v := range c.env {
			env[k] = v
		}
		err := run(loadCfg(t, env), logging.New("text", io.Discard))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: run = %v, want an error naming %q", name, err, c.want)
		}
	}
}

// A bad DATABASE_URL fails run fast, and the error does not echo the password.
func TestRunRefusesBadDatabaseURL(t *testing.T) {
	for name, dsn := range map[string]string{
		"unparsable":  "postgres://cp:hunter2pw@db host/x%zz",
		"unreachable": "postgres://cp:hunter2pw@127.0.0.1:1/x?connect_timeout=2",
	} {
		cfg := loadCfg(t, map[string]string{
			"DATABASE_URL": dsn, "JWT_SECRET": testJWT, "ENV_ENCRYPTION_KEY": testKey, "APP_ENV": "development",
		})
		start := time.Now()
		err := run(cfg, logging.New("text", io.Discard))
		if err == nil {
			t.Errorf("%s: run succeeded", name)
			continue
		}
		if strings.Contains(err.Error(), "hunter2pw") {
			t.Errorf("%s: error leaks the password: %v", name, err)
		}
		if d := time.Since(start); d > 30*time.Second {
			t.Errorf("%s: took %v to fail", name, d)
		}
	}
}

func get(url string) (int, string, error) {
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

func waitFor(t *testing.T, what string, within time.Duration, errc <-chan error, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		select {
		case err := <-errc:
			t.Fatalf("run ended while waiting for %s: %v", what, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The full cycle against real Postgres, Redis and ClickHouse: refuse a busy
// port without leaking the metrics listener, then start, answer healthz,
// readyz (200, then 503 when Redis is gone), dev login and an authenticated
// call, and shut down cleanly on SIGTERM.
func TestRunLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pgURL, stopPG, err := testutil.StartEmptyPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stopPG()
	pool, err := db.Connect(ctx, pgURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	pool.Close()

	rc, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.Terminate(context.Background()) })
	redisURL, _ := rc.ConnectionString(ctx)

	cc, err := tcclickhouse.Run(ctx, "clickhouse/clickhouse-server:24.8-alpine",
		tcclickhouse.WithUsername("test"), tcclickhouse.WithPassword("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Terminate(context.Background()) })
	chHost, _ := cc.Host(ctx)
	chPort, err := cc.MappedPort(ctx, "8123/tcp")
	if err != nil {
		t.Fatal(err)
	}

	apiAddr, metricsAddr := freeAddr(t), freeAddr(t)
	_, apiPort, _ := net.SplitHostPort(apiAddr)
	cfg := loadCfg(t, map[string]string{
		// NODE_ENV (not APP_ENV) on purpose: the fallback must warn.
		"NODE_ENV": "development", "DATABASE_URL": pgURL, "JWT_SECRET": testJWT, "ENV_ENCRYPTION_KEY": testKey,
		"REDIS_URL": redisURL, "CLICKHOUSE_URL": "http://" + chHost + ":" + chPort.Port(),
		"CLICKHOUSE_USER": "test", "CLICKHOUSE_PASSWORD": "test",
		"API_PORT": apiPort, "API_METRICS_ADDR": metricsAddr,
	})

	t.Run("busy port is refused and nothing leaks", func(t *testing.T) {
		busy, err := net.Listen("tcp", apiAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		err = run(cfg, logging.New("text", io.Discard))
		if err == nil || !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("run = %v, want address already in use", err)
		}
		// run's deferred cleanup stopped the metrics listener.
		ln, err := net.Listen("tcp", metricsAddr)
		if err != nil {
			t.Fatalf("metrics listener still bound after run failed: %v", err)
		}
		ln.Close()
	})

	t.Run("serves, reports readiness, shuts down on SIGTERM", func(t *testing.T) {
		logs := &syncBuffer{}
		errc := make(chan error, 1)
		go func() { errc <- run(cfg, logging.New("text", logs)) }()

		base := "http://" + apiAddr
		waitFor(t, "healthz", 60*time.Second, errc, func() bool {
			code, _, err := get(base + "/healthz")
			return err == nil && code == 200
		})
		if code, body, err := get(base + "/readyz"); err != nil || code != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("readyz = %d %q %v", code, body, err)
		}
		if code, body, err := get("http://" + metricsAddr + "/metrics"); err != nil || code != 200 || !strings.Contains(body, "cp_http_requests_total") {
			t.Fatalf("metrics = %d %v", code, err)
		}

		// Dev login against the real database, then an authenticated call.
		resp, err := http.Post(base+"/dev/login", "application/json", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		var login struct{ Token string }
		err = json.NewDecoder(resp.Body).Decode(&login)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || login.Token == "" {
			t.Fatalf("dev login = %d %+v %v", resp.StatusCode, login, err)
		}
		req, _ := http.NewRequest(http.MethodGet, base+"/projects", nil)
		req.Header.Set("Authorization", "Bearer "+login.Token)
		pr, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		pr.Body.Close()
		if pr.StatusCode != 200 {
			t.Fatalf("GET /projects = %d", pr.StatusCode)
		}

		// Redis gone: not ready, but still alive.
		if err := rc.Terminate(ctx); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "readyz 503 after Redis stopped", 60*time.Second, errc, func() bool {
			code, _, err := get(base + "/readyz")
			return err == nil && code == http.StatusServiceUnavailable
		})
		if code, _, err := get(base + "/healthz"); err != nil || code != 200 {
			t.Fatalf("healthz after Redis loss = %d %v", code, err)
		}

		if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-errc:
			if err != nil {
				t.Fatalf("run after SIGTERM: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("run did not return after SIGTERM")
		}
		if _, _, err := get(base + "/healthz"); err == nil {
			t.Fatal("API still answering after shutdown")
		}
		if _, _, err := get("http://" + metricsAddr + "/metrics"); err == nil {
			t.Fatal("metrics still answering after shutdown")
		}

		out := logs.String()
		for _, want := range []string{"api listening", "api shutting down", "dev login is enabled", "envKeyFingerprint"} {
			if !strings.Contains(out, want) {
				t.Errorf("log lacks %q:\n%s", want, out)
			}
		}
		warned := false
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "NODE_ENV fallback") && strings.Contains(line, "source=NODE_ENV") {
				warned = true
			}
		}
		if !warned {
			t.Errorf("no WARN naming the NODE_ENV fallback:\n%s", out)
		}
		if !strings.Contains(out, "addr="+apiAddr) {
			t.Errorf("log lacks the bound address %s:\n%s", apiAddr, out)
		}
		for _, leak := range []string{testJWT, testKey, login.Token, "cp:cp@", "password=test"} {
			if strings.Contains(out, leak) {
				t.Errorf("log leaks %q:\n%s", leak, out)
			}
		}
	})
}
