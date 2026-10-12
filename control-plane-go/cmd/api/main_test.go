package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"infrapilot/internal/api"
	"infrapilot/internal/config"
	"infrapilot/internal/logging"
	"infrapilot/internal/metrics"
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
		logStartup(logging.New(format, &buf), cfg, "127.0.0.1:8080")
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

// freeAddr returns a loopback address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// STAFF-1/SEC-4: /metrics keeps answering while in-flight requests drain, and
// stops only after srv.Shutdown has returned; open streams are cancelled first.
func TestShutdownKeepsMetricsDuringDrain(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	maddr := freeAddr(t)
	mr := startMetrics(maddr, metrics.NewAPI().Handler(), logger)
	defer mr.Stop()

	started, release := make(chan struct{}), make(chan struct{})
	srv := api.NewHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-started

	streamsCancelled := false
	done := make(chan error, 1)
	go func() {
		done <- shutdownAPI(srv, func() { streamsCancelled = true }, mr, 5*time.Second)
	}()

	// Shutdown is now waiting on the in-flight request; metrics must still answer.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get("http://" + maddr + "/metrics")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics unreachable during the drain: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before the request finished: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !streamsCancelled {
		t.Fatal("streams were not cancelled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+maddr+"/metrics", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("metrics still serving after shutdown completed")
	}
}

// STAFF-5: "api listening" is logged only after the bind succeeded.
func TestServeAPIBindFailureDoesNotAnnounce(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	announced := false
	_, err = serveAPI(api.NewHTTPServer(busy.Addr().String(), nil), func(net.Addr) { announced = true })
	if err == nil {
		t.Fatal("bind to a busy port must fail")
	}
	if announced {
		t.Fatal("announced a listener that never bound")
	}

	srv := api.NewHTTPServer("127.0.0.1:0", nil)
	var got net.Addr
	errc, err := serveAPI(srv, func(a net.Addr) { got = a })
	if err != nil || got == nil {
		t.Fatalf("err = %v, addr = %v", err, got)
	}
	_ = srv.Close()
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

// STAFF-5: a metrics bind failure is reported so run can end the process.
func TestStartMetricsReportsBindFailure(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	mr := startMetrics(busy.Addr().String(), metrics.NewAPI().Handler(), slog.New(slog.DiscardHandler))
	defer mr.Stop()
	select {
	case err := <-mr.Failed():
		if err == nil {
			t.Fatal("nil error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bind failure not reported")
	}
}
