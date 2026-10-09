package health

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	healthcontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/health"
	"github.com/supernurture/go-template/pkg/logger"
)

type checks = map[string]func(context.Context) error

func up(context.Context) error   { return nil }
func down(context.Context) error { return errors.New("connection refused") }

func newTestLogger(t *testing.T) (*logger.Logger, func() string) {
	t.Helper()
	dir := t.TempDir()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: dir})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	return log, func() string {
		_ = log.Close()
		files, _ := filepath.Glob(filepath.Join(dir, "test", "*.log"))
		if len(files) != 1 {
			t.Fatalf("log files = %v, want one", files)
		}
		written, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		return string(written)
	}
}

func ready(t *testing.T, h *Handler) healthcontract.GetReadyResponseObject {
	t.Helper()
	response, err := h.GetReady(context.Background(), healthcontract.GetReadyRequestObject{})
	if err != nil {
		t.Fatalf("GetReady: %v", err)
	}
	return response
}

func TestNewHandler(t *testing.T) {
	t.Run("positive: keeps its checks and logger", func(t *testing.T) {
		log, _ := newTestLogger(t)
		if h := NewHandler(checks{"redis/cache": up}, log); len(h.checks) != 1 || h.log != log {
			t.Errorf("handler = %+v, want the given checks and logger", h)
		}
	})

	// No dependencies means nothing can be down, so readiness is trivially true.
	t.Run("negative: no checks reports ready", func(t *testing.T) {
		got, ok := ready(t, NewHandler(nil, nil)).(healthcontract.GetReady200JSONResponse)
		if !ok || got.Condition != "Ready" {
			t.Errorf("response = %#v, want a 200 Ready", got)
		}
	})

	t.Run("negative: a nil logger panics once a check fails", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("GetReady with a nil logger and a failing check did not panic")
			}
		}()
		ready(t, NewHandler(checks{"redis/cache": down}, nil))
	})
}

func TestGetHealth(t *testing.T) {
	health := func(h *Handler, ctx context.Context) healthcontract.GetHealthResponseObject {
		response, err := h.GetHealth(ctx, healthcontract.GetHealthRequestObject{})
		if err != nil {
			t.Fatalf("GetHealth: %v", err)
		}
		return response
	}
	wantHealthy := func(t *testing.T, response healthcontract.GetHealthResponseObject) {
		t.Helper()
		if got, ok := response.(healthcontract.GetHealth200JSONResponse); !ok || got.Condition != "Healthy" {
			t.Errorf("response = %#v, want a 200 Healthy", response)
		}
	}

	t.Run("positive: answers Healthy", func(t *testing.T) {
		log, _ := newTestLogger(t)
		wantHealthy(t, health(NewHandler(nil, log), context.Background()))
	})

	t.Run("negative: liveness ignores failing readiness checks", func(t *testing.T) {
		wantHealthy(t, health(NewHandler(checks{"postgres/main": down}, nil), context.Background()))
	})

	t.Run("negative: liveness ignores a cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		wantHealthy(t, health(NewHandler(nil, nil), ctx))
	})
}

func TestGetReady(t *testing.T) {
	t.Run("positive: every check passes", func(t *testing.T) {
		log, _ := newTestLogger(t)
		response := ready(t, NewHandler(checks{"postgres/main": up, "redis/cache": up}, log))
		if got, ok := response.(healthcontract.GetReady200JSONResponse); !ok || got.Condition != "Ready" {
			t.Errorf("response = %#v, want a 200 Ready", response)
		}
	})

	t.Run("negative: one check fails and is named in the log", func(t *testing.T) {
		log, logged := newTestLogger(t)
		response := ready(t, NewHandler(checks{"postgres/main": up, "redis/cache": down}, log))
		if got, ok := response.(healthcontract.GetReady503JSONResponse); !ok || got.Condition != "NotReady" {
			t.Errorf("response = %#v, want a 503 NotReady", response)
		}
		if written := logged(); !strings.Contains(written, `"dependency":"redis/cache"`) {
			t.Errorf("log does not name the failing dependency:\n%s", written)
		}
	})

	// The check runs in its own goroutine, out of Recovery's reach: unrecovered, this would kill the process.
	t.Run("negative: a panicking check is reported, not fatal", func(t *testing.T) {
		log, logged := newTestLogger(t)
		boom := func(context.Context) error { panic("nil pool") }
		response := ready(t, NewHandler(checks{"postgres/main": up, "sql_server/legacy": boom}, log))
		if _, ok := response.(healthcontract.GetReady503JSONResponse); !ok {
			t.Errorf("response = %#v, want a 503", response)
		}
		written := logged()
		if !strings.Contains(written, `"dependency":"sql_server/legacy"`) || !strings.Contains(written, "nil pool") {
			t.Errorf("log does not name the panicking dependency and its cause:\n%s", written)
		}
	})

	// A hanging check must not stall the answer or get a healthy dependency blamed.
	t.Run("negative: a hanging check is bounded by checkTimeout", func(t *testing.T) {
		orig := checkTimeout
		checkTimeout = 50 * time.Millisecond
		t.Cleanup(func() { checkTimeout = orig })

		hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
		healthy := func(ctx context.Context) error { return ctx.Err() }
		log, logged := newTestLogger(t)

		start := time.Now()
		response := ready(t, NewHandler(checks{"redis/cache": hang, "postgres/main": healthy}, log))
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("GetReady took %v, want it bounded by checkTimeout", elapsed)
		}
		if _, ok := response.(healthcontract.GetReady503JSONResponse); !ok {
			t.Errorf("response = %#v, want a 503", response)
		}
		written := logged()
		blamed := strings.Contains(written, `"dependency":"redis/cache"`)
		if !blamed || strings.Contains(written, `"dependency":"postgres/main"`) {
			t.Errorf("log must blame only the hanging dependency:\n%s", written)
		}
	})
}
