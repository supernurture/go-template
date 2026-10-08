package database

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestSQLServerDSN(t *testing.T) {
	t.Run("positive: builds the URL with options appended", func(t *testing.T) {
		cases := map[string]string{
			"": "sqlserver://user:s4cr4t@localhost:2222?database=go",
			"encrypt=true&connection+timeout=40": "sqlserver://user:s4cr4t@localhost:2222?database=go" +
				"&encrypt=true&connection+timeout=40",
		}
		for opts, want := range cases {
			if got := sqlServerDSN("localhost", 2222, "user", "s4cr4t", "go", opts); got != want {
				t.Errorf("sqlServerDSN(opts=%q) = %q, want %q", opts, got, want)
			}
		}
	})

	t.Run("negative: URL-hostile credentials round-trip", func(t *testing.T) {
		const user, password, database = `admin@corp.com`, "p@ss w/rd?&:", "my db"

		dsn := sqlServerDSN("localhost", 2222, user, password, database, "")
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", dsn, err)
		}
		gotPassword, _ := parsed.User.Password()
		if parsed.User.Username() != user || gotPassword != password {
			t.Errorf("credentials = %q / %q, want %q / %q", parsed.User.Username(), gotPassword, user, password)
		}
		if got := parsed.Query().Get("database"); got != database {
			t.Errorf("database = %q, want %q", got, database)
		}
	})

	// opts is appended raw, so it can repeat a key the DSN already set.
	t.Run("negative: opts can shadow the database parameter", func(t *testing.T) {
		parsed, err := url.Parse(sqlServerDSN("localhost", 2222, "user", "pw", "go", "database=other"))
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if got := parsed.Query()["database"]; len(got) != 2 {
			t.Errorf("database values = %v, want both the configured and the opts one", got)
		}
	})
}

func TestNewSQLServer(t *testing.T) {
	open := func() error {
		_, err := NewSQLServer("localhost", 2222, "user", "password", "database", "encrypt=true", PoolConfig{})
		return err
	}

	t.Run("positive: opens, pools and pings", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectPing()
		var dialect string
		stubGormOpen(t, func(d gorm.Dialector, opts ...gorm.Option) (*gorm.DB, error) {
			if cfg, ok := opts[0].(*gorm.Config); !ok || !cfg.DisableAutomaticPing {
				t.Error("gorm must not ping on open: its ping has no deadline, ours does")
			}
			dialect = d.Name()
			return db, nil
		})

		got, err := NewSQLServer(
			"localhost", 2222, "user", "password", "database", "encrypt=strict", PoolConfig{MaxIdleConns: 2})
		if err != nil {
			t.Fatalf("NewSQLServer: %v", err)
		}
		if got != db || dialect != "sqlserver" {
			t.Errorf("NewSQLServer = %v via %q, want the opened DB via sqlserver", got, dialect)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: open fails", func(t *testing.T) {
		stubGormOpen(t, func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return nil, errors.New("boom") })
		if err := open(); err == nil {
			t.Error("expected open failure")
		}
	})

	t.Run("negative: pool cannot be configured", func(t *testing.T) {
		stubGormOpen(t, func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return brokenDB(), nil })
		if err := open(); err == nil {
			t.Error("expected configurePool failure")
		}
	})

	t.Run("negative: ping fails", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectPing().WillReturnError(errors.New("boom"))
		stubGormOpen(t, func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return db, nil })
		if err := open(); err == nil {
			t.Error("expected ping failure to abort")
		}
	})

	t.Run("negative: unreachable server", func(t *testing.T) {
		_, err := NewSQLServer("127.0.0.2", 2, "user", "s3cret-pw", "database", "dial+timeout=2", PoolConfig{})
		if err == nil {
			t.Fatal("expected connection error")
		}
		if strings.Contains(err.Error(), "s3cret-pw") {
			t.Errorf("error = %q, leaks the password", err)
		}
	})
}
