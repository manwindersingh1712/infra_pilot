package metrics

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkerMetricsAreExposed(t *testing.T) {
	m := NewWorker()
	m.ObserveBuild(2 * time.Second)
	m.DeploymentFinished("deployed", 30*time.Second)
	m.SetOutbox(map[string]int{"pending": 3, "dead": 1})

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`cp_deployments_total{outcome="deployed"} 1`,
		`cp_build_duration_seconds_count 1`,
		`cp_deploy_duration_seconds_count 1`,
		`cp_outbox_events{status="pending"} 3`,
		`cp_outbox_events{status="dead"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestNilWorkerIsSafe(t *testing.T) {
	var m *Worker
	m.ObserveBuild(time.Second)
	m.DeploymentFinished("failed", time.Second)
	m.SetOutbox(nil)
}

func TestOutboxLagIsExposed(t *testing.T) {
	m := NewWorker()
	m.SetOutboxLag(42 * time.Second)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if want := "cp_outbox_oldest_pending_age_seconds 42"; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("missing %q", want)
	}

	var nilWorker *Worker
	nilWorker.SetOutboxLag(time.Second)
}

func TestUnknownOutcomeIsIgnored(t *testing.T) {
	m := NewWorker()
	m.DeploymentFinished("whatever-"+time.Now().String(), time.Second)
	m.DeploymentFinished(OutcomeFailed, 0)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if strings.Contains(body, "whatever") || !strings.Contains(body, `cp_deployments_total{outcome="failed"} 1`) {
		t.Fatalf("body:\n%s", body)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:9091": true, "localhost:9091": true, "[::1]:9091": true,
		"0.0.0.0:9091": false, "[::]:9091": false, ":9091": false, "10.0.0.5:9091": false,
		"example.com:9091": false, "garbage": false,
	} {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestServeBindFailureReturnsErrorWithoutSuccessLine(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if err := Serve(context.Background(), l.Addr().String(), NewWorker().Handler(), log); err == nil {
		t.Fatal("Serve on a busy port must fail")
	}
	if strings.Contains(buf.String(), "metrics listening") {
		t.Fatalf("success line printed on bind failure: %s", buf.String())
	}
}

func TestServeWarnsOnNonLoopback(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, "0.0.0.0:0", NewWorker().Handler(), log) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "metrics listening") {
		t.Fatalf("log = %s", out)
	}
}
