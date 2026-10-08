package database

import (
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestPgQuote(t *testing.T) {
	t.Run("positive: wraps a plain value in quotes", func(t *testing.T) {
		if got := pgQuote("plain"); got != `'plain'` {
			t.Errorf("pgQuote(plain) = %q, want %q", got, `'plain'`)
		}
	})

	t.Run("negative: escapes quotes and backslashes", func(t *testing.T) {
		cases := map[string]string{
			`it's`:       `'it\'s'`,
			`back\slash`: `'back\\slash'`,
			`both\ '`:    `'both\\ \''`,
		}
		for in, want := range cases {
			if got := pgQuote(in); got != want {
				t.Errorf("pgQuote(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("negative: empty and whitespace values stay one quoted token", func(t *testing.T) {
		cases := map[string]string{"": `''`, "a b=c": `'a b=c'`}
		for in, want := range cases {
			if got := pgQuote(in); got != want {
				t.Errorf("pgQuote(%q) = %q, want %q", in, got, want)
			}
		}
	})
}

func stubGormOpen(t *testing.T, open func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error)) {
	t.Helper()
	orig := gormOpen
	gormOpen = open
	t.Cleanup(func() { gormOpen = orig })
}

func TestNewPostgres(t *testing.T) {
	open := func(opts string) error {
		_, err := NewPostgres("localhost", 2222, "user", "password", "database", opts, PoolConfig{MaxOpenConns: 2})
		return err
	}

	t.Run("positive: opens, pools and pings", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectPing()
		var dsn string
		stubGormOpen(t, func(d gorm.Dialector, opts ...gorm.Option) (*gorm.DB, error) {
			if cfg, ok := opts[0].(*gorm.Config); !ok || !cfg.DisableAutomaticPing {
				t.Error("gorm must not ping on open: its ping has no deadline, ours does")
			}
			dsn = d.Name()
			return db, nil
		})

		got, err := NewPostgres("localhost", 2222, "user", "password", "database", "sslmode=require", PoolConfig{})
		if err != nil {
			t.Fatalf("NewPostgres: %v", err)
		}
		if got != db || dsn != "postgres" {
			t.Errorf("NewPostgres = %v via %q, want the opened DB via postgres", got, dsn)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: open fails", func(t *testing.T) {
		stubGormOpen(t, func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return nil, errors.New("boom") })
		if err := open("sslmode=require"); err == nil {
			t.Error("expected open failure")
		}
	})

	t.Run("negative: pool cannot be configured", func(t *testing.T) {
		stubGormOpen(t, func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return brokenDB(), nil })
		if err := open("sslmode=require"); err == nil {
			t.Error("expected configurePool failure")
		}
	})

	t.Run("negative: ping fails", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectPing().WillReturnError(errors.New("boom"))
		stubGormOpen(t, func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return db, nil })
		if err := open("sslmode=require"); err == nil {
			t.Error("expected ping failure to abort")
		}
	})

	t.Run("negative: unreachable server", func(t *testing.T) {
		_, err := NewPostgres("127.0.0.2", 2, "user", "s3cret-pw", "database",
			"sslmode=disable connect_timeout=2", PoolConfig{})
		if err == nil {
			t.Fatal("expected connection error")
		}
		if strings.Contains(err.Error(), "s3cret-pw") {
			t.Errorf("error = %q, leaks the password", err)
		}
	})
}
