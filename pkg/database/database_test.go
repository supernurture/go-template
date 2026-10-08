package database

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func mockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()

	sqlDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{Logger: gormLogger, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db, mock
}

func brokenDB() *gorm.DB {
	return &gorm.DB{Config: &gorm.Config{}}
}

func TestGormConfig(t *testing.T) {
	t.Run("positive: uses the package logger and skips gorm's deadline-less ping", func(t *testing.T) {
		cfg := gormConfig()
		if cfg.Logger != gormLogger || !cfg.DisableAutomaticPing {
			t.Errorf("gormConfig = %+v, want gormLogger and DisableAutomaticPing", cfg)
		}
	})

	t.Run("negative: every call returns a fresh config", func(t *testing.T) {
		first, second := gormConfig(), gormConfig()
		if first == second {
			t.Fatal("gormConfig returned a shared pointer; gorm.Open mutates it")
		}
		first.DisableAutomaticPing = false
		if !gormConfig().DisableAutomaticPing {
			t.Error("mutating one config leaked into the next")
		}
	})

	t.Run("negative: does not translate driver errors", func(t *testing.T) {
		if cfg := gormConfig(); cfg.TranslateError || cfg.SkipDefaultTransaction {
			t.Errorf("gormConfig = %+v, want gorm defaults for TranslateError and transactions", cfg)
		}
	})
}

func TestHasTLS(t *testing.T) {
	secure := []string{"sslmode=require", "sslmode=verify"}

	t.Run("positive: matches a secure option", func(t *testing.T) {
		for _, opts := range []string{"sslmode=require pool=2", "sslmode=verify-full"} {
			if !hasTLS(opts, secure...) {
				t.Errorf("hasTLS(%q) = false, want true", opts)
			}
		}
	})

	t.Run("negative: insecure or no secure values", func(t *testing.T) {
		if hasTLS("sslmode=disable", secure...) {
			t.Error("disable counted as TLS")
		}
		if hasTLS("random") {
			t.Error("no secure values counted as TLS")
		}
	})

	// The drivers parse these case-insensitively, so TLS that is on still gets the cleartext warning.
	t.Run("negative: matching is case-sensitive", func(t *testing.T) {
		if hasTLS("sslmode=REQUIRE", secure...) || hasTLS("Encrypt=True", "encrypt=true") {
			t.Error("upper-case option counted as TLS; update this test if that was fixed on purpose")
		}
	})
}

func TestConfigurePool(t *testing.T) {
	t.Run("positive: applies every positive setting", func(t *testing.T) {
		db, _ := mockDB(t)
		pool := PoolConfig{MaxOpenConns: 5, MaxIdleConns: 5, ConnMaxLifetime: time.Minute}
		if err := configurePool(db, pool); err != nil {
			t.Fatalf("configurePool: %v", err)
		}
		sqlDB, _ := db.DB()
		if got := sqlDB.Stats().MaxOpenConnections; got != 5 {
			t.Errorf("MaxOpenConnections = %d, want 5", got)
		}
	})

	t.Run("negative: zero or negative settings keep the current value", func(t *testing.T) {
		db, _ := mockDB(t)
		sqlDB, _ := db.DB()
		sqlDB.SetMaxOpenConns(5)

		for _, pool := range []PoolConfig{{}, {MaxOpenConns: -1, MaxIdleConns: -1, ConnMaxLifetime: -time.Second}} {
			if err := configurePool(db, pool); err != nil {
				t.Fatalf("configurePool(%+v): %v", pool, err)
			}
			if got := sqlDB.Stats().MaxOpenConnections; got != 5 {
				t.Errorf("PoolConfig %+v changed MaxOpenConnections to %d", pool, got)
			}
		}
	})

	t.Run("negative: a DB with no connection pool", func(t *testing.T) {
		if err := configurePool(brokenDB(), PoolConfig{}); err == nil {
			t.Error("configurePool = nil error, want the missing pool reported")
		}
	})
}

func TestPing(t *testing.T) {
	t.Run("positive: a live pool answers", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectPing()
		if err := ping(db); err != nil {
			t.Fatalf("ping: %v", err)
		}
	})

	t.Run("negative: a failed ping returns the error and closes the pool", func(t *testing.T) {
		db, mock := mockDB(t)
		wantErr := errors.New("boom")
		mock.ExpectPing().WillReturnError(wantErr)
		mock.ExpectClose()

		if err := ping(db); !errors.Is(err, wantErr) {
			t.Errorf("ping error = %v, want %v", err, wantErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("a failed ping must close the pool: %v", err)
		}
	})

	t.Run("negative: a DB with no connection pool", func(t *testing.T) {
		if err := ping(brokenDB()); err == nil {
			t.Error("ping = nil error, want the missing pool reported")
		}
	})
}
