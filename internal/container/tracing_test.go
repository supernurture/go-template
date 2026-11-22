package container

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
	"gorm.io/gorm"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/pkg/tracing"
)

// restoreGlobals puts otel's globals back after a test that enabled tracing installs its own.
func restoreGlobals(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { log.Print(err) }))
	})
}

// keptExporter is tracetest's InMemoryExporter minus the reset on Shutdown, so a test can see what Close flushed.
type keptExporter struct {
	mu       sync.Mutex
	names    []string
	shutdown error // what Shutdown returns, standing in for a collector that is down
	hang     bool  // Shutdown waits for its deadline, as an export to a collector that never answers
}

func (e *keptExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.names = append(e.names, spanNames(spans)...)
	return nil
}

func (e *keptExporter) Shutdown(ctx context.Context) error {
	if e.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	return e.shutdown
}

func (e *keptExporter) exported() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.names...)
}

// stubTracing makes NewContainer build a provider that batches into memory, and records the config it got.
func stubTracing(t *testing.T) (*keptExporter, *tracing.Config) {
	t.Helper()
	restoreGlobals(t)
	exporter := &keptExporter{}
	got := &tracing.Config{}
	orig := newTracerProvider
	newTracerProvider = func(_ context.Context, cfg tracing.Config) (*sdktrace.TracerProvider, error) {
		*got = cfg
		return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter)), nil
	}
	t.Cleanup(func() { newTracerProvider = orig })
	return exporter, got
}

func tracingConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.App.Version, cfg.App.Env = "1.2.3", "staging"
	cfg.Tracing = config.Tracing{Enabled: true, Endpoint: "http://alloy:4318", SampleRatio: 0.5}
	return cfg
}

// recording returns a container that traces into a synchronous recorder, for the instrumentation tests.
func recording() (*Container, *tracetest.SpanRecorder) {
	recorder := tracetest.NewSpanRecorder()
	c := emptyContainer()
	c.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	return c, recorder
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

func attributes(span sdktrace.ReadOnlySpan) string {
	var out []string
	for _, kv := range span.Attributes() {
		out = append(out, string(kv.Key)+"="+kv.Value.String())
	}
	return strings.Join(out, " ")
}

func TestOpenTracing(t *testing.T) {
	t.Run("positive: disabled leaves the provider nil and the globals alone", func(t *testing.T) {
		restoreGlobals(t)
		before := otel.GetTracerProvider()
		c, err := NewContainer(testConfig(t))
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}
		defer c.Close()
		if c.TracerProvider != nil || otel.GetTracerProvider() != before || len(c.shutdowns) != 1 {
			t.Errorf("provider = %v, hooks = %d; want no provider, the global untouched, the logger's hook only",
				c.TracerProvider, len(c.shutdowns))
		}
	})

	t.Run("positive: enabled installs the provider, the propagator, and a flush on Close", func(t *testing.T) {
		exporter, got := stubTracing(t)
		c, err := NewContainer(tracingConfig(t))
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}

		want := tracing.Config{ServiceName: "test", ServiceVersion: "1.2.3", Env: "staging",
			Endpoint: "http://alloy:4318", SampleRatio: 0.5}
		if *got != want {
			t.Errorf("tracing config = %+v, want %+v", *got, want)
		}
		if c.TracerProvider == nil || otel.GetTracerProvider() != c.TracerProvider {
			t.Fatal("the container's provider is not the global one")
		}

		ctx, span := otel.Tracer("test").Start(context.Background(), "buffered")
		header := propagation.HeaderCarrier{}
		otel.GetTextMapPropagator().Inject(ctx, header)
		span.End()
		if header.Get("traceparent") == "" {
			t.Error("the global propagator does not write traceparent")
		}

		// The batcher still holds the span; Close must flush it before the process exits.
		if names := exporter.exported(); len(names) != 0 {
			t.Fatalf("exported before Close = %v, want nothing yet", names)
		}
		if err := c.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if names := exporter.exported(); len(names) != 1 || names[0] != "buffered" {
			t.Errorf("exported on Close = %v, want [buffered]", names)
		}
	})

	t.Run("positive: an SDK error lands in the JSON log", func(t *testing.T) {
		stubTracing(t)
		cfg := tracingConfig(t)
		c, err := NewContainer(cfg)
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}
		otel.Handle(errors.New("export refused"))
		_ = c.Close()

		files, _ := filepath.Glob(filepath.Join(cfg.Logger.Path, cfg.App.Name, "app-*.log"))
		if len(files) != 1 {
			t.Fatalf("log files = %v, want one", files)
		}
		written, _ := os.ReadFile(files[0])
		line := string(written)
		for _, want := range []string{`"level":"WARN"`, `"message":"opentelemetry error"`, `"error":"export refused"`} {
			if !strings.Contains(line, want) {
				t.Errorf("log = %s, want %s: the SDK error as a warning", written, want)
			}
		}
	})

	t.Run("negative: a flush that fails is a warning, not a failed Close", func(t *testing.T) {
		exporter, _ := stubTracing(t)
		exporter.shutdown = errors.New("collector unreachable")
		cfg := tracingConfig(t)
		c, err := NewContainer(cfg)
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close = %v, want nil: lost spans must not fail the exit", err)
		}

		files, _ := filepath.Glob(filepath.Join(cfg.Logger.Path, cfg.App.Name, "app-*.log"))
		written, _ := os.ReadFile(files[0])
		if !strings.Contains(string(written), `"message":"spans not flushed on shutdown"`) ||
			!strings.Contains(string(written), "collector unreachable") {
			t.Errorf("log = %s, want the failed flush logged", written)
		}
	})

	t.Run("negative: a collector that never answers holds Close for the reserved flush only", func(t *testing.T) {
		for name, split := range map[string]bool{"after SplitShutdown": true, "without it": false} {
			t.Run(name, func(t *testing.T) {
				exporter, _ := stubTracing(t)
				exporter.hang = true
				c, err := NewContainer(tracingConfig(t))
				if err != nil {
					t.Fatalf("NewContainer: %v", err)
				}
				want := maxFlushTimeout
				if split {
					c.SplitShutdown(400 * time.Millisecond)
					want = 100 * time.Millisecond
				}
				start := time.Now()
				_ = c.Close()
				if took := time.Since(start); took < want || took > want+time.Second {
					t.Errorf("Close took %v, want about %v", took, want)
				}
			})
		}
	})

	t.Run("negative: a provider that cannot be built fails NewContainer", func(t *testing.T) {
		restoreGlobals(t)
		cfg := tracingConfig(t)
		cfg.Tracing.Endpoint = "alloy:4318" // passes the url tag, not the scheme check
		if _, err := NewContainer(cfg); err == nil || !strings.Contains(err.Error(), "open tracing") {
			t.Fatalf("error = %v, want the tracing failure", err)
		}
	})
}

func TestTraceGorm(t *testing.T) {
	t.Run("positive: a query becomes a span without its bound values", func(t *testing.T) {
		c, recorder := recording()
		db, mock := mockGorm(t, false)
		if err := c.traceGorm(db, "postgresql"); err != nil {
			t.Fatalf("traceGorm: %v", err)
		}
		mock.ExpectQuery("SELECT").WithArgs("hunter2").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))

		var n int
		if err := db.Raw("SELECT count(*) FROM users WHERE password = ?", "hunter2").Scan(&n).Error; err != nil {
			t.Fatalf("query: %v", err)
		}
		spans := recorder.Ended()
		if len(spans) != 1 {
			t.Fatalf("spans = %v, want one", spanNames(spans))
		}
		attrs := attributes(spans[0])
		if !strings.Contains(attrs, "postgresql") || !strings.Contains(attrs, "SELECT count(*) FROM users") {
			t.Errorf("attributes = %s, want the db system and the statement", attrs)
		}
		if strings.Contains(attrs, "hunter2") {
			t.Errorf("attributes = %s, want the bound value left out", attrs)
		}
	})

	t.Run("negative: disabled registers no plugin", func(t *testing.T) {
		db, _ := mockGorm(t, false)
		if err := emptyContainer().traceGorm(db, "postgresql"); err != nil || len(db.Plugins) != 0 {
			t.Errorf("traceGorm = %v, plugins = %v; want nothing registered", err, db.Plugins)
		}
	})

	t.Run("negative: a plugin already registered is an error", func(t *testing.T) {
		c, _ := recording()
		db, _ := mockGorm(t, false)
		if err := c.traceGorm(db, "postgresql"); err != nil {
			t.Fatalf("first traceGorm: %v", err)
		}
		if err := c.traceGorm(db, "postgresql"); !errors.Is(err, gorm.ErrRegistered) {
			t.Errorf("second traceGorm = %v, want gorm.ErrRegistered", err)
		}
	})
}

func TestTraceRedis(t *testing.T) {
	newClient := func(t *testing.T) *goredis.Client {
		t.Helper()
		server := miniredis.RunT(t)
		client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		return client
	}

	t.Run("positive: a command becomes a span named after it, without its arguments", func(t *testing.T) {
		c, recorder := recording()
		client := newClient(t)
		if err := c.traceRedis(client); err != nil {
			t.Fatalf("traceRedis: %v", err)
		}
		if err := client.Set(context.Background(), "session", "secret-token", 0).Err(); err != nil {
			t.Fatalf("SET: %v", err)
		}

		var set sdktrace.ReadOnlySpan
		for _, span := range recorder.Ended() {
			if span.Name() == "set" {
				set = span
			}
		}
		if set == nil {
			t.Fatalf("spans = %v, want one named set", spanNames(recorder.Ended()))
		}
		if attrs := attributes(set); strings.Contains(attrs, "secret-token") || strings.Contains(attrs, "code.") {
			t.Errorf("attributes = %s, want neither the arguments nor the caller", attrs)
		}
	})

	// /ready pings outside any request span, so a traced PING would start a trace per probe.
	t.Run("negative: PING and AUTH make no span", func(t *testing.T) {
		c, recorder := recording()
		client := newClient(t)
		if err := c.traceRedis(client); err != nil {
			t.Fatalf("traceRedis: %v", err)
		}
		_ = client.Ping(context.Background()).Err()
		_ = client.Do(context.Background(), "auth", "user", "secret-password").Err()
		for _, span := range recorder.Ended() {
			if span.Name() == "ping" || span.Name() == "auth" {
				t.Errorf("span %q recorded, want PING and AUTH skipped", span.Name())
			}
		}
	})

	t.Run("negative: disabled adds no hook", func(t *testing.T) {
		calls := 0
		orig := instrumentRedis
		instrumentRedis = func(goredis.UniversalClient, ...redisotel.TracingOption) error { calls++; return nil }
		t.Cleanup(func() { instrumentRedis = orig })
		if err := emptyContainer().traceRedis(newClient(t)); err != nil || calls != 0 {
			t.Errorf("traceRedis = %v after %d instrument calls, want neither", err, calls)
		}
	})
}

func TestSplitShutdown(t *testing.T) {
	t.Run("positive: with tracing, a quarter of the budget flushes, capped at 2s", func(t *testing.T) {
		tests := []struct{ total, drain, flush time.Duration }{
			{8 * time.Second, 6 * time.Second, 2 * time.Second}, // the default: still under docker stop's 10s
			{time.Second, 750 * time.Millisecond, 250 * time.Millisecond},
			{30 * time.Second, 28 * time.Second, 2 * time.Second},
		}
		for _, test := range tests {
			c, _ := recording()
			if drain := c.SplitShutdown(test.total); drain != test.drain || c.flushTimeout != test.flush {
				t.Errorf("SplitShutdown(%v) = drain %v, flush %v; want %v and %v",
					test.total, drain, c.flushTimeout, test.drain, test.flush)
			}
			if drain := test.drain + c.flushTimeout; drain != test.total {
				t.Errorf("drain + flush = %v, want the whole budget %v", drain, test.total)
			}
		}
	})

	t.Run("negative: without tracing the whole budget drains", func(t *testing.T) {
		c := emptyContainer()
		if drain := c.SplitShutdown(8 * time.Second); drain != 8*time.Second || c.flushTimeout != 0 {
			t.Errorf("SplitShutdown = %v, flush %v; want 8s and none reserved", drain, c.flushTimeout)
		}
	})
}
