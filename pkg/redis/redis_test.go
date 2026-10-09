package redis

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestNew(t *testing.T) {
	t.Run("positive: connects and serves commands", func(t *testing.T) {
		server := miniredis.RunT(t)
		port, err := strconv.Atoi(server.Port())
		if err != nil {
			t.Fatalf("miniredis port %q: %v", server.Port(), err)
		}

		client, err := New(server.Host(), port, "", "", 0, false, PoolConfig{PoolSize: 2})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })

		if err := client.Set(context.Background(), "key", "value", 0).Err(); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if got, _ := server.Get("key"); got != "value" {
			t.Errorf("stored value = %q, want %q", got, "value")
		}
	})

	t.Run("negative: unreachable server", func(t *testing.T) {
		if client, err := New("127.0.0.1", 2, "", "", 0, false, PoolConfig{}); err == nil || client != nil {
			t.Fatalf("New = (%v, %v), want (nil, connection error)", client, err)
		}
	})

	t.Run("negative: wrong password", func(t *testing.T) {
		server := miniredis.RunT(t)
		server.RequireAuth("right")
		port, _ := strconv.Atoi(server.Port())

		if _, err := New(server.Host(), port, "", "wrong", 0, false, PoolConfig{}); err == nil {
			t.Fatal("New = nil error, want the auth failure from the ping")
		}
	})
}

func TestOpts(t *testing.T) {
	t.Run("positive: carries every setting and verifies TLS against the host", func(t *testing.T) {
		pool := PoolConfig{PoolSize: 8, MinIdleConns: 2, ConnMaxLifetime: time.Minute}
		opts := opts("cache.example.com", 6480, "user", "password", 4, true, pool)

		if got, want := opts.Addr, "cache.example.com:6480"; got != want {
			t.Errorf("Addr = %q, want %q", got, want)
		}
		if opts.DB != 4 || opts.Username != "user" || opts.Password != "password" {
			t.Errorf("db/credentials = %d %q/%q, want 4 user/password", opts.DB, opts.Username, opts.Password)
		}
		if opts.PoolSize != pool.PoolSize || opts.MinIdleConns != pool.MinIdleConns ||
			opts.ConnMaxLifetime != pool.ConnMaxLifetime {
			t.Errorf("pool settings not carried over: %+v", opts)
		}
		if opts.TLSConfig == nil || opts.TLSConfig.ServerName != "cache.example.com" {
			t.Fatalf("TLSConfig = %+v, want one verifying the host", opts.TLSConfig)
		}
	})

	t.Run("negative: no TLS config when TLS is off", func(t *testing.T) {
		if opts := opts("localhost", 6480, "", "", 0, false, PoolConfig{}); opts.TLSConfig != nil {
			t.Error("TLSConfig set even though TLS was not requested")
		}
	})

	// Nothing here validates input; config.Load is the only guard.
	t.Run("negative: empty host and invalid port pass through unchecked", func(t *testing.T) {
		opts := opts("", -1, "", "", -3, true, PoolConfig{PoolSize: -5})
		if opts.Addr != ":-1" || opts.DB != -3 || opts.PoolSize != -5 {
			t.Errorf("opts = %+v, want the bad values unchanged", opts)
		}
		if opts.TLSConfig.ServerName != "" {
			t.Errorf("ServerName = %q, want empty", opts.TLSConfig.ServerName)
		}
	})
}
