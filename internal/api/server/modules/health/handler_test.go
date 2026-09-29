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

func newTestLogger(t *testing.T) (*logger.Logger, string) {
	t.Helper()
	dir := t.TempDir()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: dir})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log, dir
}

func TestGetHealth(t *testing.T) {
	log, _ := newTestLogger(t)
	response, err := NewHandler(nil, log).GetHealth(context.Background(), healthcontract.GetHealthRequestObject{})
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	got, ok := response.(healthcontract.GetHealth200JSONResponse)
	if !ok {
		t.Fatalf("response = %T, want a 200", response)
	}
	if got.Condition != "Healthy" {
		t.Errorf("condition = %q, want %q", got.Condition, "Healthy")
	}
}

func TestGetReady(t *testing.T) {
	up := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("connection refused") }

	t.Run("every check passes", func(t *testing.T) {
		log, _ := newTestLogger(t)
		checks := map[string]func(context.Context) error{"postgres/main": up, "redis/cache": up}
		response, err := NewHandler(checks, log).GetReady(context.Background(), healthcontract.GetReadyRequestObject{})
		if err != nil {
			t.Fatalf("GetReady: %v", err)
		}
		if got, ok := response.(healthcontract.GetReady200JSONResponse); !ok || got.Condition != "Ready" {
			t.Errorf("response = %#v, want a 200 Ready", response)
		}
	})

	t.Run("one check fails", func(t *testing.T) {
		log, dir := newTestLogger(t)
		checks := map[string]func(context.Context) error{"postgres/main": up, "redis/cache": down}
		response, err := NewHandler(checks, log).GetReady(context.Background(), healthcontract.GetReadyRequestObject{})
		if err != nil {
			t.Fatalf("GetReady: %v", err)
		}
		if got, ok := response.(healthcontract.GetReady503JSONResponse); !ok || got.Condition != "NotReady" {
			t.Errorf("response = %#v, want a 503 NotReady", response)
		}

		_ = log.Close()
		files, _ := filepath.Glob(filepath.Join(dir, "test", "*.log"))
		if len(files) != 1 {
			t.Fatalf("log files = %v, want one", files)
		}
		written, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		if !strings.Contains(string(written), `"dependency":"redis/cache"`) {
			t.Errorf("log does not name the failing dependency:\n%s", written)
		}
	})
}

// A hanging check must not stall the answer or get a healthy dependency blamed.
func TestGetReadyBoundsEachCheck(t *testing.T) {
	orig := checkTimeout
	checkTimeout = 50 * time.Millisecond
	t.Cleanup(func() { checkTimeout = orig })

	hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	healthy := func(ctx context.Context) error { return ctx.Err() }
	checks := map[string]func(context.Context) error{"redis/cache": hang, "postgres/main": healthy}

	log, dir := newTestLogger(t)
	start := time.Now()
	response, err := NewHandler(checks, log).GetReady(context.Background(), healthcontract.GetReadyRequestObject{})
	if err != nil {
		t.Fatalf("GetReady: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("GetReady took %v, want it bounded by checkTimeout", elapsed)
	}
	if _, ok := response.(healthcontract.GetReady503JSONResponse); !ok {
		t.Errorf("response = %#v, want a 503", response)
	}

	_ = log.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "test", "*.log"))
	if len(files) != 1 {
		t.Fatalf("log files = %v, want one", files)
	}
	written, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(written), `"dependency":"redis/cache"`) {
		t.Errorf("log does not name the hanging dependency:\n%s", written)
	}
	if strings.Contains(string(written), `"dependency":"postgres/main"`) {
		t.Errorf("the healthy dependency was reported as failing:\n%s", written)
	}
}
