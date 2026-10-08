package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/pkg/database"
	"github.com/supernurture/go-template/pkg/redis"
)

func mockGorm(t *testing.T, monitorPings bool) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(monitorPings))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	db, err := gorm.Open(
		postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db, mock
}

func brokenGorm() *gorm.DB { return &gorm.DB{Config: &gorm.Config{}} }

// stubOpeners replaces the openers with fakes that record the pool settings they were given.
func stubOpeners(t *testing.T) (*database.PoolConfig, *redis.PoolConfig) {
	t.Helper()
	var dbPool database.PoolConfig
	var redisPool redis.PoolConfig

	openMock := func(_ string, _ int, _, _, _, _ string, pool database.PoolConfig) (*gorm.DB, error) {
		dbPool = pool
		db, mock := mockGorm(t, false)
		mock.ExpectClose()
		return db, nil
	}

	postgresOrig, sqlServerOrig, redisOrig := newPostgres, newSQLServer, newRedis
	newPostgres, newSQLServer = openMock, openMock
	newRedis = func(_ string, _ int, _, _ string, _ int, _ bool, pool redis.PoolConfig) (*goredis.Client, error) {
		redisPool = pool
		return goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"}), nil
	}
	t.Cleanup(func() { newPostgres, newSQLServer, newRedis = postgresOrig, sqlServerOrig, redisOrig })
	return &dbPool, &redisPool
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.App.Name = "test"
	cfg.Logger.Path = t.TempDir()
	return cfg
}

func everyDependency(cfg *config.Config) *config.Config {
	cfg.Databases.Postgres = map[string]config.Postgres{"primary": {MaxOpenConns: 7}}
	cfg.Databases.SQLServer = map[string]config.SQLServer{"legacy": {MaxOpenConns: 7}}
	cfg.Redis = map[string]config.Redis{"cache": {PoolSize: 9, ConnMaxLifetime: time.Minute}}
	return cfg
}

func TestNewContainer(t *testing.T) {
	t.Run("positive: builds the logger and HTTP client and opens every dependency", func(t *testing.T) {
		c, err := NewContainer(testConfig(t))
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}
		if c.Logger == nil || c.HTTPClient == nil || len(c.Postgres)+len(c.SQLServer)+len(c.Redis) != 0 {
			t.Errorf("container = %+v, want logger and HTTP client only", c)
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}

		stubOpeners(t)
		c, err = NewContainer(everyDependency(testConfig(t)))
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}
		if c.Postgres["primary"] == nil || c.SQLServer["legacy"] == nil || c.Redis["cache"] == nil {
			t.Errorf("not every dependency was stored: %+v", c)
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	t.Run("negative: the logger cannot be built", func(t *testing.T) {
		cfg := testConfig(t)
		blocked := filepath.Join(cfg.Logger.Path, cfg.App.Name)
		if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write %s: %v", blocked, err)
		}
		if _, err := NewContainer(cfg); err == nil || !strings.Contains(err.Error(), "build logger") {
			t.Fatalf("error = %v, want a logger failure", err)
		}
	})

	t.Run("negative: an unreachable dependency is named", func(t *testing.T) {
		tests := map[string]func(cfg *config.Config){
			`postgres "primary"`: func(cfg *config.Config) {
				cfg.Databases.Postgres = map[string]config.Postgres{
					"primary": {Host: "127.0.0.1", Port: 2, Opts: "connect_timeout=1"},
				}
			},
			`sql server "legacy"`: func(cfg *config.Config) {
				cfg.Databases.SQLServer = map[string]config.SQLServer{"legacy": {Host: "127.0.0.1", Port: 2}}
			},
			`redis "cache"`: func(cfg *config.Config) {
				cfg.Redis = map[string]config.Redis{"cache": {Host: "127.0.0.1", Port: 2}}
			},
		}
		for want, configure := range tests {
			t.Run(want, func(t *testing.T) {
				cfg := testConfig(t)
				configure(cfg)
				if _, err := NewContainer(cfg); err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want it to name %s", err, want)
				}
			})
		}
	})
}

func TestClose(t *testing.T) {
	recorder := func(order *[]string, name string, err error) func() error {
		return func() error { *order = append(*order, name); return err }
	}

	t.Run("positive: unwinds every hook in reverse", func(t *testing.T) {
		var order []string
		c := &Container{shutdowns: []func() error{
			recorder(&order, "logger", nil), recorder(&order, "postgres", nil), recorder(&order, "redis", nil),
		}}
		if err := c.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := strings.Join(order, ","); got != "redis,postgres,logger" {
			t.Errorf("shutdown order = %q, want redis,postgres,logger", got)
		}
	})

	t.Run("negative: a failing hook does not stop the rest", func(t *testing.T) {
		var order []string
		c := &Container{shutdowns: []func() error{
			recorder(&order, "logger", nil), recorder(&order, "postgres", errors.New("boom")),
			recorder(&order, "redis", errors.New("bang")),
		}}
		err := c.Close()
		if len(order) != 3 || err == nil ||
			!strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "bang") {
			t.Errorf("ran %v, error = %v; want every hook run and both failures joined", order, err)
		}
	})

	// Hooks are kept after Close, so a second Close runs them again and Redis reports it is closed.
	t.Run("negative: closing twice is a no-op", func(t *testing.T) {
		stubOpeners(t)
		cfg := testConfig(t)
		cfg.Redis = map[string]config.Redis{"cache": {}}
		c, err := NewContainer(cfg)
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}
		if err := c.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		if err := c.Close(); err != nil {
			t.Errorf("second Close = %v, want nil", err)
		}

		var runs int
		c = &Container{shutdowns: []func() error{func() error { runs++; return nil }}}
		_, _ = c.Close(), c.Close()
		if runs != 1 {
			t.Errorf("hook ran %d times, want once", runs)
		}
	})
}

func TestPings(t *testing.T) {
	t.Run("positive: one passing check per connection", func(t *testing.T) {
		up, mock := mockGorm(t, true)
		mock.ExpectPing()
		checks := (&Container{Postgres: map[string]*gorm.DB{"main": up}}).Pings()
		if len(checks) != 1 || checks["postgres/main"] == nil {
			t.Fatalf("checks = %v, want only postgres/main", checks)
		}
		if err := checks["postgres/main"](context.Background()); err != nil {
			t.Errorf("postgres/main: %v", err)
		}
	})

	t.Run("negative: broken or unreachable connections fail their check", func(t *testing.T) {
		down := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
		t.Cleanup(func() { _ = down.Close() })
		checks := (&Container{
			SQLServer: map[string]*gorm.DB{"legacy": brokenGorm()},
			Redis:     map[string]*goredis.Client{"cache": down},
		}).Pings()
		for _, name := range []string{"sql_server/legacy", "redis/cache"} {
			if check, ok := checks[name]; !ok || check(context.Background()) == nil {
				t.Errorf("%s: want a failing check", name)
			}
		}
	})

	t.Run("negative: no connections, no checks", func(t *testing.T) {
		if checks := (&Container{}).Pings(); len(checks) != 0 {
			t.Errorf("checks = %v, want none", checks)
		}
	})
}

func TestOpen(t *testing.T) {
	t.Run("positive: opens every kind with its pool settings and registers a close hook", func(t *testing.T) {
		dbPool, redisPool := stubOpeners(t)
		c := emptyContainer()
		if err := c.open(everyDependency(testConfig(t))); err != nil {
			t.Fatalf("open: %v", err)
		}
		if dbPool.MaxOpenConns != 7 || redisPool.PoolSize != 9 || redisPool.ConnMaxLifetime != time.Minute {
			t.Errorf("pools = %+v / %+v, want the configured settings passed through", *dbPool, *redisPool)
		}
		if len(c.shutdowns) != 3 {
			t.Errorf("shutdown hooks = %d, want 3", len(c.shutdowns))
		}
		_ = c.Close()
	})

	t.Run("negative: an opener failure is wrapped with the dependency name", func(t *testing.T) {
		stubOpeners(t)
		boom := errors.New("boom")
		newSQLServer = func(string, int, string, string, string, string, database.PoolConfig) (*gorm.DB, error) {
			return nil, boom
		}
		c := emptyContainer()
		err := c.open(everyDependency(testConfig(t)))
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), `sql server "legacy"`) {
			t.Errorf("open = %v, want the wrapped sql server failure", err)
		}
		// Postgres opened first, so its hook must be registered for NewContainer to close it.
		if len(c.shutdowns) != 1 || c.Redis["cache"] != nil {
			t.Errorf("hooks = %d, redis = %v; want postgres' hook kept, redis never opened",
				len(c.shutdowns), c.Redis)
		}
		_ = c.Close()
	})

	t.Run("negative: nil maps panic on the first dependency", func(t *testing.T) {
		stubOpeners(t)
		defer func() {
			if recover() == nil {
				t.Error("open on a zero Container did not panic; NewContainer is the only safe constructor")
			}
		}()
		_ = (&Container{}).open(everyDependency(testConfig(t)))
	})
}

func TestNewLogger(t *testing.T) {
	t.Run("positive: builds a logger from config", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.Logger.RotationPattern, cfg.Logger.RetentionDays = "daily", 3
		log, err := newLogger(cfg)
		if err != nil {
			t.Fatalf("newLogger: %v", err)
		}
		_ = log.Close()
		if _, err := os.Stat(filepath.Join(cfg.Logger.Path, cfg.App.Name)); err != nil {
			t.Errorf("log directory missing: %v", err)
		}
	})

	t.Run("negative: file disabled without console", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.Logger.DisableFile = true
		if _, err := newLogger(cfg); err == nil || !strings.Contains(err.Error(), "no output") {
			t.Fatalf("error = %v, want the logger to refuse having no output", err)
		}
	})

	t.Run("negative: log directory blocked", func(t *testing.T) {
		cfg := testConfig(t)
		if err := os.WriteFile(filepath.Join(cfg.Logger.Path, cfg.App.Name), nil, 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		if _, err := newLogger(cfg); err == nil || !strings.Contains(err.Error(), "build logger") {
			t.Fatalf("error = %v, want a wrapped logger failure", err)
		}
	})
}

func TestPingGorm(t *testing.T) {
	t.Run("positive: a live pool answers", func(t *testing.T) {
		db, mock := mockGorm(t, true)
		mock.ExpectPing()
		if err := pingGorm(db)(context.Background()); err != nil {
			t.Errorf("pingGorm: %v", err)
		}
	})

	t.Run("negative: no connection pool", func(t *testing.T) {
		if err := pingGorm(brokenGorm())(context.Background()); err == nil {
			t.Error("pingGorm = nil error, want the missing pool reported")
		}
	})

	t.Run("negative: the ping fails", func(t *testing.T) {
		db, mock := mockGorm(t, true)
		mock.ExpectPing().WillReturnError(errors.New("connection refused"))
		if err := pingGorm(db)(context.Background()); err == nil {
			t.Error("pingGorm = nil error, want the ping failure")
		}
	})
}

func TestCloseGorm(t *testing.T) {
	t.Run("positive: closes the pool", func(t *testing.T) {
		db, mock := mockGorm(t, false)
		mock.ExpectClose()
		if err := closeGorm(db)(); err != nil {
			t.Errorf("closeGorm: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: no connection pool", func(t *testing.T) {
		if err := closeGorm(brokenGorm())(); err == nil {
			t.Error("closeGorm = nil error, want the missing pool reported")
		}
	})

	t.Run("negative: the driver fails to close", func(t *testing.T) {
		db, mock := mockGorm(t, false)
		mock.ExpectClose().WillReturnError(errors.New("busy"))
		if err := closeGorm(db)(); err == nil {
			t.Error("closeGorm = nil error, want the close failure")
		}
	})
}

func emptyContainer() *Container {
	return &Container{
		Postgres: map[string]*gorm.DB{}, SQLServer: map[string]*gorm.DB{}, Redis: map[string]*goredis.Client{},
	}
}
