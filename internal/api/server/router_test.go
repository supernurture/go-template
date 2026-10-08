package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/container"
	"github.com/supernurture/go-template/pkg/logger"
	"github.com/supernurture/go-template/pkg/redis"
)

func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Server.Mode = gin.TestMode
	cfg.Server.Timeout = time.Second
	return cfg
}

func newTestDeps(t *testing.T) *container.Container {
	deps, _ := newLoggedDeps(t)
	return deps
}

// newLoggedDeps returns deps whose logger can be flushed and read back.
func newLoggedDeps(t *testing.T) (*container.Container, func() string) {
	t.Helper()
	dir := t.TempDir()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: dir})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	return &container.Container{Logger: log}, func() string {
		_ = log.Close()
		files, _ := filepath.Glob(filepath.Join(dir, "test", "*.log"))
		if len(files) == 0 {
			return ""
		}
		written, _ := os.ReadFile(files[0])
		return string(written)
	}
}

func withRedis(t *testing.T, deps *container.Container) *container.Container {
	t.Helper()
	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatalf("miniredis port %q: %v", server.Port(), err)
	}
	client, err := redis.New(server.Host(), port, "", "", 0, false, redis.PoolConfig{})
	if err != nil {
		t.Fatalf("redis.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	deps.Redis = map[string]*goredis.Client{"example": client}
	return deps
}

func withPostgres(t *testing.T, deps *container.Container) (*container.Container, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(
		postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	deps.Postgres = map[string]*gorm.DB{"example": db}
	return deps, mock
}

func fullDeps(t *testing.T) (*container.Container, sqlmock.Sqlmock) {
	t.Helper()
	return withPostgres(t, withRedis(t, newTestDeps(t)))
}

func newTestRouter(t *testing.T, cfg *config.Config, deps *container.Container) *gin.Engine {
	t.Helper()
	router, err := NewRouter(cfg, deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return router
}

func get(t *testing.T, router *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func postNote(t *testing.T, cfg *config.Config, body string) *httptest.ResponseRecorder {
	t.Helper()
	deps, _ := fullDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/example/notes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	newTestRouter(t, cfg, deps).ServeHTTP(rec, req)
	return rec
}

func wantResponse(t *testing.T, rec *httptest.ResponseRecorder, status int, bodyPrefix string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d", rec.Code, status)
	}
	if got := strings.TrimSpace(rec.Body.String()); !strings.HasPrefix(got, bodyPrefix) {
		t.Errorf("body = %q, want it to start with %q", got, bodyPrefix)
	}
}

// testContext returns a gin context over a recorder, for calling the error handlers directly.
func testContext(ctx context.Context) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	return c, rec
}

func TestNewRouter(t *testing.T) {
	t.Run("positive: serves health through the middleware chain", func(t *testing.T) {
		rec := get(t, newTestRouter(t, testConfig(), newTestDeps(t)), "/health")
		wantResponse(t, rec, http.StatusOK, `{"condition":"Healthy"}`)
		if rec.Header().Get("X-Request-ID") == "" {
			t.Error("X-Request-ID header missing, middleware chain did not run")
		}
	})

	t.Run("negative: an invalid trusted proxy", func(t *testing.T) {
		cfg := testConfig()
		cfg.Server.TrustedProxies = []string{"not-an-ip"}
		if _, err := NewRouter(cfg, newTestDeps(t)); err == nil {
			t.Fatal("NewRouter accepted an invalid trusted proxy")
		}
	})

	// gin.SetMode panics instead of returning an error; config.Load is the only guard.
	t.Run("negative: an unknown gin mode panics", func(t *testing.T) {
		cfg := testConfig()
		cfg.Server.Mode = "bogus"
		defer func() {
			if recover() == nil {
				t.Error("NewRouter with an unknown mode did not panic")
			}
			gin.SetMode(gin.TestMode)
		}()
		_, _ = NewRouter(cfg, newTestDeps(t))
	})
}

func TestRegister(t *testing.T) {
	t.Run("positive: mounts health, readiness and the example module", func(t *testing.T) {
		deps, mock := fullDeps(t)
		router := newTestRouter(t, testConfig(), deps)

		for _, want := range []string{`{"visits":1}`, `{"visits":2}`} {
			wantResponse(t, get(t, router, "/example/visits"), http.StatusOK, want)
		}

		created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
		mock.ExpectQuery(`SELECT \* FROM "example_notes"`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "title", "body", "created_at"}).
				AddRow(int64(1), "First note", "the body", created))
		if rec := get(t, router, "/example/notes"); rec.Code != http.StatusOK ||
			!strings.Contains(rec.Body.String(), `"title":"First note"`) {
			t.Errorf("notes = %d %q, want the stored note", rec.Code, rec.Body.String())
		}
		wantResponse(t, get(t, router, "/ready"), http.StatusOK, `{"condition":"Ready"}`)
	})

	t.Run("negative: the example module is skipped and logged without every dependency", func(t *testing.T) {
		setups := map[string]func(*testing.T, *container.Container){
			"nothing configured": func(*testing.T, *container.Container) {},
			"only redis":         func(t *testing.T, deps *container.Container) { withRedis(t, deps) },
			"only postgres":      func(t *testing.T, deps *container.Container) { withPostgres(t, deps) },
		}
		for name, setup := range setups {
			t.Run(name, func(t *testing.T) {
				deps, logged := newLoggedDeps(t)
				setup(t, deps)
				router := newTestRouter(t, testConfig(), deps)
				for _, path := range []string{"/example/visits", "/example/notes"} {
					if rec := get(t, router, path); rec.Code != http.StatusNotFound {
						t.Errorf("%s: status = %d, want 404", path, rec.Code)
					}
				}
				if !strings.Contains(logged(), "module not mounted") {
					t.Error("skipping the module was not logged")
				}
			})
		}
	})

	t.Run("negative: readiness turns 503 when a dependency goes down", func(t *testing.T) {
		deps := withRedis(t, newTestDeps(t))
		router := newTestRouter(t, testConfig(), deps)
		_ = deps.Redis["example"].Close()
		wantResponse(t, get(t, router, "/ready"), http.StatusServiceUnavailable, `{"condition":"NotReady"}`)
	})
}

func TestInvalidParam(t *testing.T) {
	t.Run("positive: answers with the given status and records the cause", func(t *testing.T) {
		c, rec := testContext(context.Background())
		invalidParam(c, errors.New("Invalid format for parameter limit"), http.StatusBadRequest)
		wantResponse(t, rec, http.StatusBadRequest, `{"message":"Invalid format for parameter limit"}`)
		if len(c.Errors) != 1 {
			t.Errorf("c.Errors = %v, want the cause recorded for AccessLog", c.Errors)
		}
	})

	t.Run("negative: an unparsable query parameter through the router", func(t *testing.T) {
		deps, _ := fullDeps(t)
		rec := get(t, newTestRouter(t, testConfig(), deps), "/example/notes?limit=abc")
		wantResponse(t, rec, http.StatusBadRequest, `{"message":"Invalid format for parameter limit: `)
	})

	t.Run("negative: the error text reaches the client unfiltered", func(t *testing.T) {
		c, rec := testContext(context.Background())
		invalidParam(c, errors.New("pq: secret internal detail"), http.StatusBadRequest)
		if !strings.Contains(rec.Body.String(), "secret internal detail") {
			t.Errorf("body = %q; update this test now that invalidParam filters its message", rec.Body.String())
		}
	})
}

func TestBadRequest(t *testing.T) {
	t.Run("positive: a decode error is a 400 carrying the decoder message", func(t *testing.T) {
		wantResponse(t, postNote(t, testConfig(), `{"title":`), http.StatusBadRequest, `{"message":"unexpected EOF"}`)
	})

	t.Run("negative: an empty or oversized body", func(t *testing.T) {
		wantResponse(t, postNote(t, testConfig(), ""), http.StatusBadRequest, `{"message":"a JSON body is required"}`)

		cfg := testConfig()
		cfg.Server.MaxBodyBytes = 16
		wantResponse(t, postNote(t, cfg, `{"title":"`+strings.Repeat("x", 64)+`"}`),
			http.StatusRequestEntityTooLarge, `{"message":"request body too large"}`)

		c, rec := testContext(context.Background())
		badRequest(c, io.EOF)
		wantResponse(t, rec, http.StatusBadRequest, `{"message":"a JSON body is required"}`)
	})

	// The decoder's message names Go types, which says more about the server than the caller needs.
	t.Run("negative: a wrongly typed field leaks Go type names", func(t *testing.T) {
		rec := postNote(t, testConfig(), `{"title":5}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Go struct field") {
			t.Errorf("response = %d %q, want a 400 quoting the Go decoder", rec.Code, rec.Body.String())
		}
	})
}

func TestInternalError(t *testing.T) {
	const generic = `{"message":"internal server error"}`

	t.Run("positive: a generic 500 with the cause kept for the log", func(t *testing.T) {
		c, rec := testContext(context.Background())
		internalError(c, errors.New("pq: secret internal detail"))
		wantResponse(t, rec, http.StatusInternalServerError, generic)
		if len(c.Errors) != 1 || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("errors = %v, body = %q; want the cause logged, not sent", c.Errors, rec.Body.String())
		}

		deps, mock := fullDeps(t)
		mock.ExpectQuery(`SELECT \* FROM "example_notes"`).WillReturnError(errors.New("pq: secret internal detail"))
		rec = get(t, newTestRouter(t, testConfig(), deps), "/example/notes")
		if got := rec.Body.String(); rec.Code != http.StatusInternalServerError || got != generic {
			t.Errorf("response = %d %q, want the generic 500", rec.Code, got)
		}
	})

	t.Run("negative: a response already started gets no second body", func(t *testing.T) {
		c, rec := testContext(context.Background())
		c.String(http.StatusOK, "partial")
		internalError(c, errors.New("write failed"))
		if got := rec.Body.String(); got != "partial" {
			t.Errorf("body = %q, want only the partial response", got)
		}
	})

	t.Run("negative: past the deadline it leaves the answer to Timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c, rec := testContext(ctx)
		internalError(c, context.Canceled)
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want none", rec.Body.String())
		}

		deps, mock := fullDeps(t)
		cfg := testConfig()
		cfg.Server.Timeout = 50 * time.Millisecond
		mock.ExpectQuery(`SELECT \* FROM "example_notes"`).
			WillDelayFor(time.Second).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		if rec := get(t, newTestRouter(t, cfg, deps), "/example/notes"); rec.Code != http.StatusGatewayTimeout {
			t.Errorf("status = %d, want 504", rec.Code)
		}
	})
}
