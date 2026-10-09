// Package metrics exposes Prometheus metrics on a private address.
package metrics

import (
	"context"
	"errors"
	"log/slog"
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
	}
	reg.MustRegister(m.deployments, m.build, m.deploy, m.outbox, m.outboxLag,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Worker) ObserveBuild(d time.Duration) {
	if m != nil {
		m.build.Observe(d.Seconds())
	}
}

// DeploymentFinished counts an outcome; d is the total time since the
// deployment was created and is recorded only for successful deployments.
func (m *Worker) DeploymentFinished(outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.deployments.WithLabelValues(outcome).Inc()
	if outcome == "deployed" {
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

// Serve exposes /metrics on addr until ctx ends. addr should be a private
// address such as 127.0.0.1:9091.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("metrics listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
