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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hadesshade/entra-id-applications-exporter/internal/config"
	"github.com/hadesshade/entra-id-applications-exporter/internal/exporter"
	"github.com/hadesshade/entra-id-applications-exporter/internal/graph"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	logger.Info("starting entra app exporter",
		"listen", cfg.ListenAddress, "cloud", cfg.Cloud, "tenant_id", cfg.TenantID, "config_file", cfg.ConfigFile,
	) // never log the client secret

	gc, err := graph.New(graph.Options{
		Cloud: cfg.Cloud, TenantID: cfg.TenantID, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
	})
	if err != nil {
		return err
	}

	// Warn only: don't crash-loop on a network blip.
	checkCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := gc.CheckAccess(checkCtx); err != nil {
		logger.Warn("could not acquire a Graph token at startup", "err", err)
	}
	cancel()

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		exporter.New(gc, exporter.Options{ScrapeTimeout: cfg.ScrapeTimeout, CacheMaxAge: cfg.CacheMaxAge, LatestCredentialsOnly: cfg.LatestCredentialsOnly}, logger),
	)

	mux := http.NewServeMux()
	mux.Handle(cfg.MetricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	srv := &http.Server{Addr: cfg.ListenAddress, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
