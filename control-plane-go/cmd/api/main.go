// Command api serves the HTTP API used by cp-ui.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"infrapilot/internal/api"
	"infrapilot/internal/auth"
	"infrapilot/internal/config"
	"infrapilot/internal/db"
	"infrapilot/internal/logging"
	"infrapilot/internal/logs"
	"infrapilot/internal/metrics"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/tickets"
)

const (
	ticketTTL       = 60 * time.Second
	shutdownTimeout = 10 * time.Second
)

func main() {
	_ = godotenv.Load()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("api exited", "err", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.LogFormat, os.Stdout)
	if err := run(cfg, logger); err != nil {
		logger.Error("api exited", "err", err)
		os.Exit(1)
	}
}

// listenAddr is the API bind address. With dev login on (APP_ENV=development)
// POST /dev/login mints tokens without credentials, so the API binds loopback
// only; otherwise it binds every interface.
func listenAddr(devLogin bool, port string) string {
	if devLogin {
		return "127.0.0.1:" + port
	}
	return ":" + port
}

// newHTTPServer builds the API server. WriteTimeout covers ordinary routes;
// the log stream extends its own write deadline per request.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// metricsRun is the metrics listener. It runs on its own context, not the
// signal context, so /metrics stays up while in-flight requests drain.
type metricsRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	failed chan error
}

func startMetrics(addr string, h http.Handler, logger *slog.Logger) *metricsRun {
	ctx, cancel := context.WithCancel(context.Background())
	mr := &metricsRun{cancel: cancel, done: make(chan struct{}), failed: make(chan error, 1)}
	go func() {
		defer close(mr.done)
		if err := metrics.Serve(ctx, addr, h, logger); err != nil {
			logger.Error("metrics server stopped", "err", err)
			mr.failed <- err
		}
	}()
	return mr
}

// Failed receives the error if the metrics listener could not bind or stopped
// on its own. The API treats that as fatal, as the worker does.
func (m *metricsRun) Failed() <-chan error { return m.failed }

// Stop ends the listener and waits for it. Safe to call more than once.
func (m *metricsRun) Stop() {
	m.cancel()
	<-m.done
}

// shutdownAPI ends open log streams, drains in-flight requests, and only then
// stops metrics, so the drain is observable.
func shutdownAPI(srv *http.Server, cancelStreams func(), mr *metricsRun, timeout time.Duration) error {
	cancelStreams()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := srv.Shutdown(ctx)
	mr.Stop()
	return err
}

// serveAPI binds srv.Addr, calls onListening with the bound address, then
// serves in the background. The returned channel receives Serve's result.
func serveAPI(srv *http.Server, onListening func(net.Addr)) (<-chan error, error) {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, err
	}
	onListening(ln.Addr())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	return errc, nil
}

// logStartup writes the one startup line. Config redacts its secrets; the
// encryption key appears only as its fingerprint.
func logStartup(logger *slog.Logger, cfg config.Config, addr string) {
	logger.Info("api listening", "addr", addr, "devLogin", cfg.IsDevelopment(),
		"envKeyFingerprint", secrets.Fingerprint(cfg.EnvEncryptionKey), "config", cfg)
}

func run(cfg config.Config, logger *slog.Logger) error {
	if err := cfg.RequireAPI(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	box, err := secrets.New(cfg.EnvEncryptionKey)
	if err != nil {
		return err
	}
	rdb, err := logs.NewRedis(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()
	ch, err := logs.NewClickHouse(cfg.ClickHouseURL, cfg.ClickHouseUser, cfg.ClickHousePassword)
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Init(ctx); err != nil {
		return err
	}
	tix, err := tickets.New(cfg.RedisURL, ticketTTL)
	if err != nil {
		return err
	}
	defer tix.Close()
	signer, err := auth.NewSigner(cfg.JWTSecret)
	if err != nil {
		return err
	}

	// streamCtx is not the request context: cancelling it ends open log
	// streams only, while ordinary in-flight requests keep the drain period.
	streamCtx, cancelStreams := context.WithCancel(context.Background())
	defer cancelStreams()

	m := metrics.NewAPI()
	mr := startMetrics(cfg.APIMetricsAddr, m.Handler(), logger)
	defer mr.Stop() // runs before the pool and client closes above

	handler := api.NewHandler(api.Deps{
		Store: store.New(pool), Redis: rdb, Signer: signer, Secrets: box,
		Logs: &logs.Aggregator{Hot: rdb, Warm: ch, Now: time.Now, Logger: logger}, Live: rdb, Tickets: tix,
		Metrics: m, Log: logger, CORSOrigins: cfg.CORSOrigins, DevLogin: cfg.IsDevelopment(),
		TrustedProxies: cfg.TrustedProxies, Stream: api.StreamConfig{Shutdown: streamCtx},
	})
	addr := listenAddr(cfg.IsDevelopment(), cfg.APIPort)
	if cfg.IsDevelopment() {
		logger.Warn("dev login is enabled: POST /dev/login needs no credentials, binding loopback only", "addr", addr)
	}
	srv := newHTTPServer(addr, handler)

	errc, err := serveAPI(srv, func(a net.Addr) { logStartup(logger, cfg, a.String()) })
	if err != nil {
		return err
	}

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-mr.Failed():
		cancelStreams()
		_ = srv.Close()
		return fmt.Errorf("metrics: %w", err)
	case <-ctx.Done():
	}
	logger.Info("api shutting down")
	return shutdownAPI(srv, cancelStreams, mr, shutdownTimeout)
}
