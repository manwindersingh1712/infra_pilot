// Command worker runs the Temporal deploy worker, the outbox starter, the log
// flush loop and the metrics endpoint.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	"infrapilot/internal/config"
	"infrapilot/internal/db"
	"infrapilot/internal/logging"
	"infrapilot/internal/logs"
	"infrapilot/internal/metrics"
	"infrapilot/internal/runtime"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/worker/build"
	"infrapilot/internal/worker/outbox"
	"infrapilot/internal/workflows"
)

const (
	flushInterval        = 5 * time.Minute
	outboxMetricInterval = 15 * time.Second
	probePullTimeout     = 2 * time.Minute
)

func main() {
	_ = godotenv.Load()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.LogFormat, os.Stdout)
	if err := run(cfg, logger); err != nil {
		logger.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	if err := cfg.RequireWorker(); err != nil {
		return err
	}
	if err := prepareEnv(); err != nil {
		return err
	}
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(sigCtx)

	gitOut, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		cancel()
		return err
	}
	gitVersion, err := checkGitVersion(string(gitOut))
	if err != nil {
		cancel()
		return err
	}
	logger.Info("git found", "version", gitVersion)

	limits, err := runtime.NewLimits(cfg.ContainerMemory, cfg.ContainerCPUs, cfg.ContainerPids)
	if err != nil {
		cancel()
		return err
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		cancel()
		return err
	}
	defer pool.Close()
	st := store.New(pool)

	// cfg.EnvEncryptionKey was base64-decoded and length-checked by config.Load.
	box, err := secrets.New(cfg.EnvEncryptionKey)
	if err != nil {
		cancel()
		return err
	}

	rdb, err := logs.NewRedis(cfg.RedisURL)
	if err != nil {
		cancel()
		return err
	}
	defer rdb.Close()
	ch, err := logs.NewClickHouse(cfg.ClickHouseURL, cfg.ClickHouseUser, cfg.ClickHousePassword)
	if err != nil {
		cancel()
		return err
	}
	defer ch.Close()
	if err := ch.Init(ctx); err != nil {
		cancel()
		return err
	}
	agg := &logs.Aggregator{Hot: rdb, Warm: ch, Now: time.Now, Logger: logger} // Logger: Task 14 STAFF-1 fix round (per-stream flush errors)

	tc, err := client.Dial(client.Options{
		HostPort: cfg.TemporalHostPort, Namespace: cfg.TemporalNamespace,
		Logger: tlog.NewStructuredLogger(logger),
	})
	if err != nil {
		cancel()
		return err
	}
	// Defers run last-in first-out: cancel the root context first so the
	// loops stop issuing Temporal calls, then close the client (T25 note).
	defer func() {
		cancel()
		tc.Close()
	}()

	m := metrics.NewWorker()
	runner := runtime.ExecRunner{}
	docker := runtime.Docker{R: runner, Limits: limits, Nginx: cfg.NginxContainer, ProbeImage: cfg.ProbeImage}

	// The probe image runs health checks; pull it now so the first deployment
	// does not wait on (or fail because of) a registry. A failure is not fatal.
	pullCtx, pullCancel := context.WithTimeout(ctx, probePullTimeout)
	if err := docker.Pull(pullCtx, cfg.ProbeImage); err != nil {
		logger.Warn("probe image pull failed; health probes need it locally", "image", cfg.ProbeImage, "err", err)
	}
	pullCancel()

	routesDir := resolveRoutesDir(cfg.NginxRoutesDir, logger)

	streamer := runtime.NewStreamer(ctx, rdb, logger)
	acts, err := newActivities(activityDeps{
		Store:   st,
		Secrets: box,
		Docker:  docker,
		Router: runtime.Nginx{R: runner, RoutesDir: routesDir, Container: cfg.NginxContainer,
			BaseDomain: cfg.ServiceBaseDomain, Port: cfg.NginxPort},
		Builder:  build.Builder{R: runner, Name: cfg.BuilderName, Memory: cfg.BuilderMemory, CPUs: cfg.BuilderCPUs, Log: logger},
		Logs:     streamer,
		Temporal: tc,
		Metrics:  m,
		Log:      logger,
		Cfg: workflows.ActivityConfig{
			RegistryHost: cfg.RegistryHost, BuildWorkdir: cfg.BuildWorkdir,
			DefaultContainerPort: cfg.DefaultContainerPort,
			PortStart:            cfg.PortRangeStart, PortEnd: cfg.PortRangeEnd,
			ExposeManagedPorts: cfg.ExposeManagedPorts, LegacyVolumeRoot: cfg.LegacyVolumeRoot,
			HealthTimeout: cfg.HealthTimeout,
		},
	})
	if err != nil {
		return err
	}

	w := worker.New(tc, workflows.TaskQueue, worker.Options{})
	w.RegisterWorkflow(workflows.DeployWorkflow)
	w.RegisterWorkflow(workflows.TeardownWorkflow)
	w.RegisterActivity(acts)
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()

	publisher := outbox.New(st, workflows.Starter{Client: tc, Log: logger}, logger)
	sup := newSupervisor(logger, cancel)
	// Resume runs under the supervisor: a hung Docker daemon must not delay the
	// outbox starter or "worker up" (T26 STAFF-1).
	if running, err := st.ListRunningDeployments(ctx); err != nil {
		logger.Warn("resume: cannot list running deployments", "err", err)
	} else {
		list := resumeList(running)
		sup.Start(ctx, "resume", resumeLoop(ctx, logger, func() int { return streamer.Resume(ctx, list, docker.IsRunning) }))
	}
	sup.Start(ctx, "outbox starter", func() error { publisher.Run(ctx); return nil })
	sup.Start(ctx, "log flush", func() error { every(ctx, flushInterval, func() { flush(ctx, agg, logger) }); return nil })
	sup.Start(ctx, "outbox metrics", func() error {
		every(ctx, outboxMetricInterval, func() {
			if counts, err := st.CountOutboxByStatus(ctx); err != nil {
				logger.Warn("outbox metrics: count failed", "err", err)
			} else {
				m.SetOutbox(counts)
			}
			if age, err := st.OldestUnpublishedOutboxAge(ctx); err != nil {
				logger.Warn("outbox metrics: lag failed", "err", err)
			} else {
				m.SetOutboxLag(age)
			}
		})
		return nil
	})
	sup.Start(ctx, "metrics server", func() error { return metrics.Serve(ctx, cfg.WorkerMetricsAddr, m.Handler(), logger) })

	logger.Info("worker up", "taskQueue", workflows.TaskQueue, "temporal", cfg.TemporalHostPort, "routesDir", routesDir, "config", cfg)
	<-ctx.Done()
	logger.Info("worker shutting down")
	sup.Wait()
	return nil
}

func flush(ctx context.Context, agg *logs.Aggregator, logger *slog.Logger) {
	processed, errs := agg.Flush(ctx)
	logger.Info("flushed logs", "processed", processed, "errors", errs)
}

// every runs fn on each tick until ctx ends.
func every(ctx context.Context, d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}
