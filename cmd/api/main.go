package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yonathandj/go-template/internal/api/server"
	"github.com/Yonathandj/go-template/internal/config"
	"github.com/Yonathandj/go-template/pkg/logger"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	lumber, err := logger.Load(logger.Config(configuration.Logger))
	if err != nil {
		log.Fatal(err)
	}

	postgreDBs := make(map[string]server.DatabaseConfig, len(configuration.Databases.Postgre))
	for name, cfg := range configuration.Databases.Postgre {
		postgreDBs[name] = server.DatabaseConfig(cfg)
	}

	svc, err := server.NewService(server.Config{
		Databases: server.DatabasesConfig{
			Postgre: postgreDBs,
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	router := server.RouterInit(lumber, svc)

	addr := fmt.Sprintf(":%d", configuration.Server.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       configuration.Server.Timeout + 5*time.Second,
		WriteTimeout:      configuration.Server.Timeout + 5*time.Second,
		IdleTimeout:       30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer stop()

	serverErr := make(chan error, 1)

	go func() {
		lumber.Info("starting http server", "addr", addr)

		if err := httpServer.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}

		close(serverErr)
	}()

	select {
	case <-ctx.Done():
		lumber.Info("shutdown signal received")

	case err := <-serverErr:
		if err != nil {
			lumber.Error("http server failed", "error", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)
	defer cancel()

	lumber.Info("shutting down server")

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		lumber.Error(
			"failed to gracefully shutdown http server",
			"error", err,
		)
	}

	if err := svc.Shutdown(); err != nil {
		lumber.Error(
			"failed to gracefully shutdown service",
			"error", err,
		)
	}

	lumber.Info("service shut down gracefully")
}
