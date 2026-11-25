package database

import (
	"context"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var gormLogger = logger.New(
	gormWriter{},
	logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		ParameterizedQueries:      true,
		IgnoreRecordNotFoundError: true,
	},
)

// gormWriter reaches the standard logger when GORM writes, not when this package loads, so a redirect
// installed at startup (logger.RedirectStdLog) catches it. GORM reports nothing below Warn here.
type gormWriter struct{}

func (gormWriter) Printf(format string, args ...any) {
	log.Printf("warning: "+format, args...)
}

func gormConfig() *gorm.Config {
	return &gorm.Config{Logger: gormLogger, DisableAutomaticPing: true}
}

// PoolConfig holds connection-pool settings; a zero value keeps the driver default.
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

func configurePool(db *gorm.DB, pool PoolConfig) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	if pool.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	}
	if pool.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	}
	if pool.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(pool.ConnMaxLifetime)
	}
	return nil
}

func ping(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return err
	}
	return nil
}

func hasTLS(opts string, secureVals ...string) bool {
	for _, value := range secureVals {
		if strings.Contains(opts, value) {
			return true
		}
	}
	return false
}
