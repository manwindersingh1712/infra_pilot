package metrics

import (
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
