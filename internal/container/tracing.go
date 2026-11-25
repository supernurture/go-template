package container

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"gorm.io/gorm"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/pkg/database"
	"github.com/supernurture/go-template/pkg/tracing"
)

// The span flush on Close takes a quarter of server.shutdown_timeout, at most this; the rest drains requests.
const maxFlushTimeout = 2 * time.Second

var (
	newTracerProvider = tracing.New
	instrumentRedis   = redisotel.InstrumentTracing
)

// openTracing installs the provider globally as well, so otel.Tracer(...) in a module joins the same traces.
func (c *Container) openTracing(cfg *config.Config) error {
	if !cfg.Tracing.Enabled {
		return nil
	}
	provider, err := newTracerProvider(context.Background(), tracing.Config{
		ServiceName:    cfg.App.Name,
		ServiceVersion: cfg.App.Version,
		Env:            cfg.App.Env,
		Endpoint:       cfg.Tracing.Endpoint,
		SampleRatio:    cfg.Tracing.SampleRatio,
	})
	if err != nil {
		return fmt.Errorf("open tracing: %w", err)
	}

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(tracing.Propagator())
	// The SDK reports a failed export here; without it the error reaches the log only as a bare INFO line.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		c.Logger.Warn("opentelemetry error", map[string]any{"error": err.Error()})
	}))

	c.TracerProvider = provider
	// Spans are best effort, as at startup: a collector that is down at shutdown costs the last batch,
	// logged here, not a failed exit.
	c.shutdowns = append(c.shutdowns, func() error {
		timeout := c.flushTimeout
		if timeout <= 0 { // Close without SplitShutdown, as after a failed start
			timeout = maxFlushTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			c.Logger.Warn("spans not flushed on shutdown", map[string]any{"error": err.Error()})
		}
		return nil
	})
	return nil
}

// SplitShutdown divides the shutdown budget: it keeps part of total for Close to flush spans and
// returns the rest, for the server to drain requests in. Without tracing the whole budget drains.
func (c *Container) SplitShutdown(total time.Duration) (drain time.Duration) {
	if c.TracerProvider == nil {
		return total
	}
	c.flushTimeout = min(maxFlushTimeout, total/4)
	return total - c.flushTimeout
}

// traceGorm records each query with its placeholders, never the values bound to them.
func (c *Container) traceGorm(conn *gorm.DB, system string) error {
	if c.TracerProvider == nil {
		return nil
	}
	return conn.Use(database.NewTracingPlugin(c.TracerProvider, system))
}

// traceRedis names each span after its command. The arguments stay off it, since a cached value
// can be a token or personal data, and so does the caller lookup, which costs a stack walk per command.
func (c *Container) traceRedis(client *goredis.Client) error {
	if c.TracerProvider == nil {
		return nil
	}
	return instrumentRedis(client,
		redisotel.WithTracerProvider(c.TracerProvider),
		redisotel.WithDBStatement(false),
		redisotel.WithCallerEnabled(false),
		redisotel.WithCommandFilter(untracedCommand),
	)
}

// untracedCommand skips PING, which /ready sends on every probe outside any request span, so each
// would start a trace of its own; and keeps redisotel's default of skipping AUTH and HELLO with a password.
func untracedCommand(cmd goredis.Cmder) bool {
	return strings.EqualFold(cmd.Name(), "ping") || redisotel.DefaultCommandFilter(cmd)
}
