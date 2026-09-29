package database

import (
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestPgQuote(t *testing.T) {
	cases := map[string]string{
		"plain":      `'plain'`,
		`it's`:       `'it\'s'`,
		`back\slash`: `'back\\slash'`,
		`both\ '`:    `'both\\ \''`,
		"":           `''`,
	}
	for in, want := range cases {
		if got := pgQuote(in); got != want {
			t.Errorf("pgQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewPostgres(t *testing.T) {
	db, mock := mockDB(t)
	mock.ExpectPing()

	orig := gormOpen
	gormOpen = func(_ gorm.Dialector, opts ...gorm.Option) (*gorm.DB, error) {
		if cfg, ok := opts[0].(*gorm.Config); !ok || !cfg.DisableAutomaticPing {
			t.Error("gorm must not ping on open: its ping has no deadline, ours does")
		}
		return db, nil
	}
	t.Cleanup(func() { gormOpen = orig })

	got, err := NewPostgres(
		"localhost", 2222, "user", "password", "database", "sslmode=require", PoolConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	if got != db {
		t.Error("expected the opened DB back")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	mock.ExpectPing().WillReturnError(errors.New("boom"))
	if _, err := NewPostgres(
		"localhost", 2222, "user", "password", "database", "sslmode=require", PoolConfig{}); err == nil {
		t.Error("expected ping failure to abort")
	}

	gormOpen = func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return brokenDB(), nil }
	if _, err := NewPostgres(
		"localhost", 2222, "user", "password", "database", "sslmode=require", PoolConfig{}); err == nil {
		t.Error("expected configurePool failure")
	}

	gormOpen = func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return nil, errors.New("boom") }
	if _, err := NewPostgres(
		"localhost", 2222, "user", "password", "database", "sslmode=require", PoolConfig{}); err == nil {
		t.Error("expected open failure")
	}
}

func TestNewPostgresUnreachable(t *testing.T) {
	if _, err := NewPostgres(
		"127.0.0.2", 2, "user", "password", "database", "sslmode=disable connect_timeout=2", PoolConfig{}); err == nil {
		t.Fatal("expected connection error")
	}
}
