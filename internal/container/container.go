package container

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/httpclient"
	"github.com/supernurture/go-template/internal/middleware"
	"github.com/supernurture/go-template/pkg/database"
	"github.com/supernurture/go-template/pkg/logger"
	"github.com/supernurture/go-template/pkg/redis"
	"github.com/supernurture/go-template/pkg/tracing"
)

// Container holds shared dependencies, keyed by their config name.
type Container struct {
	Logger     *logger.Logger
	HTTPClient *httpclient.HTTPClient
	Postgres   map[string]*gorm.DB
	SQLServer  map[string]*gorm.DB
	Redis      map[string]*goredis.Client
	// TracerProvider is nil when tracing.enabled is false; the router and clients then add no instrumentation.
	TracerProvider trace.TracerProvider

	flushTimeout time.Duration // set by SplitShutdown; maxFlushTimeout until then
	shutdowns    []func() error
}

// NewContainer opens every configured dependency. Call Close when done.
func NewContainer(cfg *config.Config) (*Container, error) {
	log, err := newLogger(cfg)
	if err != nil {
		return nil, err
	}

	deps := &Container{
		Logger:    log,
		Postgres:  make(map[string]*gorm.DB, len(cfg.Databases.Postgres)),
		SQLServer: make(map[string]*gorm.DB, len(cfg.Databases.SQLServer)),
		Redis:     make(map[string]*goredis.Client, len(cfg.Redis)),

		shutdowns: []func() error{log.Close},
	}
	// Before anything opens, so the connection notices and GORM's warnings land in the JSON log too.
	restoreStdLog := log.RedirectStdLog()
	deps.shutdowns = append(deps.shutdowns, func() error { restoreStdLog(); return nil })

	// Tracing opens before anything it instruments, so Close flushes spans after those are closed.
	if err := deps.openTracing(cfg); err != nil {
		return nil, deps.abort(err)
	}
	deps.HTTPClient = httpclient.NewHTTPClient(cfg, log, deps.TracerProvider)

	if err := deps.open(cfg); err != nil {
		return nil, deps.abort(err)
	}

	return deps, nil
}

// Close unwinds every hook in reverse, continuing past failures. Calling it again is a no-op.
func (c *Container) Close() error {
	var errs []error
	for x := len(c.shutdowns) - 1; x >= 0; x-- {
		errs = append(errs, c.shutdowns[x]())
	}
	c.shutdowns = nil

	return errors.Join(errs...)
}

// Pings returns a readiness check per connection, keyed "<kind>/<name>".
func (c *Container) Pings() map[string]func(context.Context) error {
	checks := make(map[string]func(context.Context) error, len(c.Postgres)+len(c.SQLServer)+len(c.Redis))
	for name, conn := range c.Postgres {
		checks["postgres/"+name] = pingGorm(conn)
	}
	for name, conn := range c.SQLServer {
		checks["sql_server/"+name] = pingGorm(conn)
	}
	for name, client := range c.Redis {
		checks["redis/"+name] = func(ctx context.Context) error { return client.Ping(ctx).Err() }
	}
	return checks
}

// abort logs why the start failed while the logger is still open, so the reason reaches the JSON log a
// log agent reads, not only main's plain stderr line, then unwinds what opened.
func (c *Container) abort(err error) error {
	c.Logger.Error("start failed", map[string]any{"error": err.Error()})
	_ = c.Close()
	return err
}

var (
	newPostgres  = database.NewPostgres
	newSQLServer = database.NewSQLServer
	newRedis     = redis.New
)

func (c *Container) open(cfg *config.Config) error {
	for name, db := range cfg.Databases.Postgres {
		conn, err := newPostgres(db.Host, db.Port, db.User, db.Password, db.Database, db.Opts, database.PoolConfig{
			MaxOpenConns:    db.MaxOpenConns,
			MaxIdleConns:    db.MaxIdleConns,
			ConnMaxLifetime: db.ConnMaxLifetime,
		})
		if err != nil {
			return fmt.Errorf("open postgres %q: %w", name, err)
		}
		c.Postgres[name] = conn
		c.shutdowns = append(c.shutdowns, closeGorm(conn))
		if err := c.traceGorm(conn, "postgresql"); err != nil {
			return fmt.Errorf("trace postgres %q: %w", name, err)
		}
	}

	for name, db := range cfg.Databases.SQLServer {
		conn, err := newSQLServer(db.Host, db.Port, db.User, db.Password, db.Database, db.Opts, database.PoolConfig{
			MaxOpenConns:    db.MaxOpenConns,
			MaxIdleConns:    db.MaxIdleConns,
			ConnMaxLifetime: db.ConnMaxLifetime,
		})
		if err != nil {
			return fmt.Errorf("open sql server %q: %w", name, err)
		}
		c.SQLServer[name] = conn
		c.shutdowns = append(c.shutdowns, closeGorm(conn))
		if err := c.traceGorm(conn, "microsoft.sql_server"); err != nil {
			return fmt.Errorf("trace sql server %q: %w", name, err)
		}
	}

	for name, cache := range cfg.Redis {
		client, err := newRedis(
			cache.Host, cache.Port, cache.User, cache.Password, cache.DB, cache.TLS,
			redis.PoolConfig{
				PoolSize:        cache.PoolSize,
				MinIdleConns:    cache.MinIdleConns,
				ConnMaxLifetime: cache.ConnMaxLifetime,
			})
		if err != nil {
			return fmt.Errorf("open redis %q: %w", name, err)
		}
		c.Redis[name] = client
		c.shutdowns = append(c.shutdowns, client.Close)
		if err := c.traceRedis(client); err != nil {
			return fmt.Errorf("trace redis %q: %w", name, err)
		}
	}

	return nil
}

func newLogger(cfg *config.Config) (*logger.Logger, error) {
	log, err := logger.New(logger.Config{
		ServiceName: cfg.App.Name,
		Env:         cfg.App.Env,
		Path:        cfg.Logger.Path,
		Level:       cfg.Logger.Level,
		Console:     cfg.Logger.Console,
		DisableFile: cfg.Logger.DisableFile,
		Rotation: logger.RotationOptions{
			Daily:      cfg.Logger.RotationPattern == "daily",
			MaxSizeMB:  cfg.Logger.RotationSizeMB,
			MaxAgeDays: cfg.Logger.RetentionDays,
		},
		ContextFields: []logger.ContextFields{middleware.LogFields, tracing.LogFields},
	})
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}

	return log, nil
}

func pingGorm(conn *gorm.DB) func(context.Context) error {
	return func(ctx context.Context) error {
		sqlDB, err := conn.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
}

func closeGorm(conn *gorm.DB) func() error {
	return func() error {
		sqlDB, err := conn.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
}
