// Command api serves the HTTP API used by cp-ui.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// checkDevLogin refuses POST /dev/login outside development. cfg.IsDevelopment
// is the only source of the flag today (unset APP_ENV means production), so
// this guards against a later wiring change.
func checkDevLogin(devLogin bool, appEnv string) error {
	if devLogin && appEnv != "development" {
		return fmt.Errorf("config: dev login is enabled but APP_ENV is %q", appEnv)
	}
	return nil
}

// newHTTPServer builds the API server. WriteTimeout covers ordinary routes;
// the log stream extends its own write deadline per request.
func newHTTPServer(port string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + port,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// logStartup writes the one startup line. Config redacts its secrets; the
// encryption key appears only as its fingerprint.
func logStartup(logger *slog.Logger, cfg config.Config) {
	logger.Info("api listening", "port", cfg.APIPort, "devLogin", cfg.IsDevelopment(),
		"envKeyFingerprint", secrets.Fingerprint(cfg.EnvEncryptionKey), "config", cfg)
}

func run(cfg config.Config, logger *slog.Logger) error {
	if err := cfg.RequireAPI(); err != nil {
		return err
	}
	if err := checkDevLogin(cfg.IsDevelopment(), cfg.AppEnv); err != nil {
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
	metricsCtx, stopMetrics := context.WithCancel(ctx)
	metricsDone := make(chan struct{})
	go func() {
		defer close(metricsDone)
		if err := metrics.Serve(metricsCtx, cfg.APIMetricsAddr, m.Handler(), logger); err != nil {
			logger.Error("metrics server stopped", "err", err)
		}
	}()
	defer func() { // runs before the pool and client closes above
		stopMetrics()
		<-metricsDone
	}()

	handler := api.NewHandler(api.Deps{
		Store: store.New(pool), Redis: rdb, Signer: signer, Secrets: box,
		Logs: &logs.Aggregator{Hot: rdb, Warm: ch, Now: time.Now, Logger: logger}, Live: rdb, Tickets: tix,
		Metrics: m, Log: logger, CORSOrigins: cfg.CORSOrigins, DevLogin: cfg.IsDevelopment(),
		TrustedProxies: cfg.TrustedProxies, Stream: api.StreamConfig{Shutdown: streamCtx},
	})
	srv := newHTTPServer(cfg.APIPort, handler)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logStartup(logger, cfg)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	logger.Info("api shutting down")
	cancelStreams()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	stop() // then the deferred metrics stop and wait, then the pool and clients close
	return err
}
