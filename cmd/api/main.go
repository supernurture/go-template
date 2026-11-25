package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/supernurture/go-template/internal/api/server"
	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/container"
	"github.com/supernurture/go-template/pkg/metrics"
)

const (
	readHeaderTimeout = 5 * time.Second
	writeSlack        = 5 * time.Second

	readTimeout = 15 * time.Second
	idleTimeout = 60 * time.Second

	// A scrape is a few hundred series; slower than this is a stuck agent.
	metricsWriteTimeout = 10 * time.Second

	// Under docker stop's 10s grace period.
	defaultShutdownTimeout = 8 * time.Second
)

var (
	exit      = os.Exit
	listen    = net.Listen
	newRouter = server.NewRouter
	closeDeps = func(deps *container.Container) error { return deps.Close() }
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}
}

func run(ctx context.Context) (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	deps, err := container.NewContainer(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeDeps(deps); closeErr != nil && err == nil {
			err = fmt.Errorf("close dependencies: %w", closeErr)
		}
	}()

	router, err := newRouter(cfg, deps)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      cfg.Server.Timeout + writeSlack,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}

	serveErrCh := make(chan error, 2)
	// Up before the API and closed after its drain, so the last scrapes still see it.
	if deps.Metrics != nil {
		metricsSrv, err := serveMetrics(cfg.Metrics.Port, deps, serveErrCh)
		if err != nil {
			return err
		}
		defer func() { _ = metricsSrv.Close() }()
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	listener, err := listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	go func() {
		if serveErr := srv.Serve(listener); !errors.Is(serveErr, http.ErrServerClosed) {
			serveErrCh <- serveErr
		}
	}()
	deps.Logger.Info("server listening", map[string]any{"addr": listener.Addr().String()})

	select {
	case serveErr := <-serveErrCh:
		return fmt.Errorf("serve: %w", serveErr)
	case <-ctx.Done():
	}

	shutdownTimeout := cfg.Server.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultShutdownTimeout
	}
	// The span flush on Close comes out of the same budget, so the whole exit stays within it.
	drainTimeout := deps.SplitShutdown(shutdownTimeout)
	deps.Logger.Info("shutting down", map[string]any{
		"timeout": shutdownTimeout.String(), "drain_timeout": drainTimeout.String(),
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
		return fmt.Errorf("shutdown server: %w", shutdownErr)
	}

	return nil
}

// serveMetrics serves GET /metrics on its own port until the returned server closes.
func serveMetrics(port int, deps *container.Container, errCh chan<- error) (*http.Server, error) {
	addr := fmt.Sprintf(":%d", port)
	listener, err := listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen metrics %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler(deps.Metrics))
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      metricsWriteTimeout,
		IdleTimeout:       idleTimeout,
	}
	go func() {
		if serveErr := srv.Serve(listener); !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("metrics: %w", serveErr)
		}
	}()
	deps.Logger.Info("metrics listening", map[string]any{"addr": listener.Addr().String()})
	return srv, nil
}
