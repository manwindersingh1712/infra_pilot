// Package metrics exposes Prometheus metrics on a private address.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Worker holds the worker's metrics. A nil *Worker is valid and records nothing.
type Worker struct {
	reg         *prometheus.Registry
	deployments *prometheus.CounterVec
	build       prometheus.Histogram
	deploy      prometheus.Histogram
	outbox      *prometheus.GaugeVec
	outboxLag   prometheus.Gauge
	teardownErr *prometheus.CounterVec
}

func NewWorker() *Worker {
	reg := prometheus.NewRegistry()
	m := &Worker{
		reg: reg,
		deployments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cp_deployments_total", Help: "Finished deployments by outcome (deployed|failed).",
		}, []string{"outcome"}),
		build: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "cp_build_duration_seconds", Help: "Clone + build + push time.",
			Buckets: prometheus.ExponentialBuckets(5, 2, 9), // 5s .. ~21m
		}),
		deploy: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "cp_deploy_duration_seconds", Help: "Time from deployment creation to running.",
			Buckets: prometheus.ExponentialBuckets(5, 2, 10),
		}),
		outbox: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cp_outbox_events", Help: "Outbox rows by status.",
		}, []string{"status"}),
		outboxLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cp_outbox_oldest_pending_age_seconds", Help: "Age of the oldest pending or publishing outbox row; 0 when none.",
		}),
		teardownErr: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cp_teardown_activity_failures_total",
			Help: "Failed teardown activity attempts by step (cancel|teardown). Teardown retries without limit, so a count that keeps rising means a stuck teardown.",
		}, []string{"step"}),
	}
	reg.MustRegister(m.deployments, m.build, m.deploy, m.outbox, m.outboxLag, m.teardownErr,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Worker) ObserveBuild(d time.Duration) {
	if m != nil {
		m.build.Observe(d.Seconds())
	}
}

// Deployment outcomes accepted by DeploymentFinished.
const (
	OutcomeDeployed = "deployed"
	OutcomeFailed   = "failed"
)

// Teardown steps accepted by TeardownActivityFailed.
const (
	StepCancel   = "cancel"
	StepTeardown = "teardown"
)

// TeardownActivityFailed counts one failed attempt of a teardown activity.
// Any step other than StepCancel or StepTeardown is ignored, so the label set
// stays bounded.
func (m *Worker) TeardownActivityFailed(step string) {
	if m == nil || (step != StepCancel && step != StepTeardown) {
		return
	}
	m.teardownErr.WithLabelValues(step).Inc()
}

// DeploymentFinished counts an outcome; d is the total time since the
// deployment was created and is recorded only for successful deployments.
// Any outcome other than OutcomeDeployed or OutcomeFailed is ignored, so the
// label set stays bounded (SEC-2 T15).
func (m *Worker) DeploymentFinished(outcome string, d time.Duration) {
	if m == nil || (outcome != OutcomeDeployed && outcome != OutcomeFailed) {
		return
	}
	m.deployments.WithLabelValues(outcome).Inc()
	if outcome == OutcomeDeployed {
		m.deploy.Observe(d.Seconds())
	}
}

func (m *Worker) SetOutbox(counts map[string]int) {
	if m == nil {
		return
	}
	for _, status := range []string{"pending", "publishing", "published", "dead"} {
		m.outbox.WithLabelValues(status).Set(float64(counts[status]))
	}
}

// SetOutboxLag records the age of the oldest unpublished outbox row.
func (m *Worker) SetOutboxLag(d time.Duration) {
	if m != nil {
		m.outboxLag.Set(d.Seconds())
	}
}

func (m *Worker) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Server limits (T26 SEC-2): the endpoint is tiny and private, so keep them tight.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 8 << 10
)

// isLoopbackAddr reports whether addr (host:port) binds only the loopback
// interface. An empty host, 0.0.0.0 and :: are all-interfaces binds.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Serve exposes /metrics on addr until ctx ends. addr should be a private
// address such as 127.0.0.1:9091; a non-loopback address logs a warning.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h)
	srv := &http.Server{
		Handler: mux, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout,
		WriteTimeout: writeTimeout, IdleTimeout: idleTimeout, MaxHeaderBytes: maxHeaderBytes,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if !isLoopbackAddr(addr) {
		log.Warn("metrics endpoint is not bound to loopback; it has no authentication", "addr", addr)
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("metrics listening", "addr", ln.Addr().String())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
