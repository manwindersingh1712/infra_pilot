package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// API holds HTTP metrics. A nil *API is valid and records nothing.
type API struct {
	reg      *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func NewAPI() *API {
	reg := prometheus.NewRegistry()
	m := &API{
		reg: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cp_http_requests_total", Help: "HTTP requests by route pattern, method and status.",
		}, []string{"route", "method", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "cp_http_request_duration_seconds", Help: "HTTP request duration by route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
	}
	reg.MustRegister(m.requests, m.duration,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Observe records one request. route is the mux pattern (never the raw path,
// which would explode label cardinality with IDs).
func (m *API) Observe(route, method string, status int, d time.Duration) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(route, normaliseMethod(method), strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(route).Observe(d.Seconds())
}

// normaliseMethod bounds the method label: Go's server accepts any token as a
// method, so a raw r.Method would let a client mint unbounded series.
func normaliseMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions:
		return method
	}
	return "OTHER"
}

func (m *API) Handler() http.Handler { return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}) }
