package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"infrapilot/internal/config"
	"infrapilot/internal/logging"
)

func TestStartupLogHasNoSecrets(t *testing.T) {
	const jwt = "jwt-secret-value-that-must-not-appear-0123456789"
	key := bytes.Repeat([]byte{0x5a}, 32)
	keyB64 := base64.StdEncoding.EncodeToString(key)
	env := map[string]string{
		"DATABASE_URL": "postgres://cp:dbpassword-xyz@localhost:5432/cp", "JWT_SECRET": jwt,
		"ENV_ENCRYPTION_KEY": keyB64, "CLICKHOUSE_PASSWORD": "chpass-xyz",
		"REDIS_URL": "redis://:redispass-xyz@localhost:6380", "APP_ENV": "development",
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		var buf bytes.Buffer
		logStartup(logging.New(format, &buf), cfg)
		out := buf.String()
		for _, secret := range []string{jwt, keyB64, "dbpassword-xyz", "chpass-xyz", "redispass-xyz", string(key)} {
			if strings.Contains(out, secret) {
				t.Fatalf("%s startup log leaks %q:\n%s", format, secret, out)
			}
		}
		if !strings.Contains(out, "envKeyFingerprint") || !strings.Contains(out, "devLogin") {
			t.Fatalf("%s startup log lacks the fingerprint or devLogin:\n%s", format, out)
		}
	}
}

func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(":8081", nil)
	if srv.Addr != ":8081" {
		t.Fatal(srv.Addr)
	}
	for name, got := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout, "ReadTimeout": srv.ReadTimeout,
		"IdleTimeout": srv.IdleTimeout, "WriteTimeout": srv.WriteTimeout,
	} {
		if got <= 0 {
			t.Fatalf("%s is not set", name)
		}
	}
	if srv.MaxHeaderBytes != 64<<10 {
		t.Fatalf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
	if srv.WriteTimeout != 30*time.Second || srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatal("timeouts differ from the plan")
	}
	if srv.BaseContext != nil {
		t.Fatal("BaseContext must stay unset: SIGTERM must not cancel in-flight requests")
	}
}

func TestListenAddr(t *testing.T) {
	cases := []struct {
		devLogin bool
		port     string
		want     string
	}{
		{true, "8080", "127.0.0.1:8080"},
		{false, "8080", ":8080"},
	}
	for _, c := range cases {
		if got := listenAddr(c.devLogin, c.port); got != c.want {
			t.Errorf("listenAddr(%v, %q) = %q, want %q", c.devLogin, c.port, got, c.want)
		}
	}
}
