package server

import (
	"context"
	"encoding/json"
	"io"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/supernurture/go-template/internal/container"
	"github.com/supernurture/go-template/internal/middleware"
	"github.com/supernurture/go-template/pkg/logger"
	"github.com/supernurture/go-template/pkg/tracing"
)

// tracedDeps is fullDeps with tracing on and Redis on redisotel's defaults; container.traceRedis's
// options are tested in internal/container.
func tracedDeps(t *testing.T) (*container.Container, *tracetest.SpanRecorder, func() []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	hooks := []logger.ContextFields{middleware.LogFields, tracing.LogFields}
	log, err := logger.New(logger.Config{ServiceName: "traced", Path: dir, ContextFields: hooks})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	recorder := tracetest.NewSpanRecorder()
	deps := &container.Container{
		Logger:         log,
		TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)),
	}
	deps, _ = withPostgres(t, withRedis(t, deps))
	if err := redisotel.InstrumentTracing(deps.Redis["example"],
		redisotel.WithTracerProvider(deps.TracerProvider)); err != nil {
		t.Fatalf("InstrumentTracing: %v", err)
	}

	return deps, recorder, func() []map[string]any {
		_ = log.Close()
		files, _ := filepath.Glob(filepath.Join(dir, "traced", "*.log"))
		if len(files) != 1 {
			t.Fatalf("log files = %v, want one", files)
		}
		written, _ := os.ReadFile(files[0])
		var entries []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(written)), "\n") {
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatalf("log line is not JSON: %v\n%s", err, line)
			}
			entries = append(entries, entry)
		}
		return entries
	}
}

func accessLine(t *testing.T, entries []map[string]any) map[string]any {
	t.Helper()
	for _, entry := range entries {
		if entry["message"] == "request" {
			return entry
		}
	}
	t.Fatalf("no access-log line in %v", entries)
	return nil
}

func serverSpan(t *testing.T, recorder *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.SpanKind() == trace.SpanKindServer {
			return span
		}
	}
	t.Fatalf("no server span among %d ended", len(recorder.Ended()))
	return nil
}

func TestTracing(t *testing.T) {
	t.Run("positive: a request is a server span, and its Redis call and log lines join it", func(t *testing.T) {
		deps, recorder, entries := tracedDeps(t)
		rec := get(t, newTestRouter(t, testConfig(), deps), "/example/visits")
		wantResponse(t, rec, http.StatusOK, `{"visits":1}`)

		server := serverSpan(t, recorder)
		if server.Name() != "GET /example/visits" {
			t.Errorf("server span = %q, want it named after the route", server.Name())
		}
		var incr sdktrace.ReadOnlySpan
		for _, span := range recorder.Ended() {
			if span.Name() == "incr" {
				incr = span
			}
		}
		if incr == nil || incr.Parent().SpanID() != server.SpanContext().SpanID() {
			t.Errorf("incr span = %v, want a child of the server span: the handler's ctx must reach Redis", incr)
		}

		line := accessLine(t, entries())
		if line["trace_id"] != server.SpanContext().TraceID().String() ||
			line["span_id"] != server.SpanContext().SpanID().String() || line["trace_flags"] != "01" {
			t.Errorf("access line = %v, want it linked to the server span", line)
		}
		if line["request_id"] != rec.Header().Get("X-Request-ID") {
			t.Errorf("access line request_id = %v, want the request ID still there", line["request_id"])
		}
	})

	t.Run("positive: an inbound traceparent is continued, not replaced", func(t *testing.T) {
		deps, recorder, entries := tracedDeps(t)
		req := httptest.NewRequest(http.MethodGet, "/example/visits", nil)
		req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
		newTestRouter(t, testConfig(), deps).ServeHTTP(httptest.NewRecorder(), req)

		server := serverSpan(t, recorder)
		if got := server.SpanContext().TraceID().String(); got != "0af7651916cd43dd8448eb211c80319c" {
			t.Errorf("trace_id = %s, want the caller's", got)
		}
		if got := server.Parent().SpanID().String(); got != "b7ad6b7169203331" {
			t.Errorf("parent span = %s, want the caller's span", got)
		}
		if line := accessLine(t, entries()); line["trace_id"] != "0af7651916cd43dd8448eb211c80319c" {
			t.Errorf("access line trace_id = %v, want the caller's", line["trace_id"])
		}
	})

	// The HTTP conventions leave a 4xx server span unset: the caller erred.
	t.Run("negative: a 4xx leaves the span unset and logs why it was refused", func(t *testing.T) {
		tests := []struct{ name, method, path, body, reason string }{
			{"unparsable parameter", http.MethodGet, "/example/notes?limit=abc", "", "Invalid format for parameter"},
			{"invalid parameter", http.MethodGet, "/example/notes?limit=0", "", "limit must be between 1 and 100"},
			{"undecodable body", http.MethodPost, "/example/notes", `{"title":`, "unexpected EOF"},
			{"empty body", http.MethodPost, "/example/notes", "", "EOF"},
			{"invalid body", http.MethodPost, "/example/notes", `{"title":" "}`, "title is required"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				deps, recorder, entries := tracedDeps(t)
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
				req.Header.Set("Content-Type", "application/json")
				newTestRouter(t, testConfig(), deps).ServeHTTP(rec, req)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400", rec.Code)
				}

				if status := serverSpan(t, recorder).Status(); status.Code != codes.Unset {
					t.Errorf("span status = %+v, want unset for a 4xx", status)
				}
				line := accessLine(t, entries())
				if reason, _ := line["reason"].(string); line["level"] != "WARN" ||
					!strings.HasPrefix(reason, test.reason) || line["errors"] != nil {
					t.Errorf("access line = %v, want a WARN with reason %q and no errors", line, test.reason)
				}
			})
		}
	})

	t.Run("negative: a 5xx marks the span as an error and logs the cause", func(t *testing.T) {
		deps, recorder, entries := tracedDeps(t)
		_ = deps.Redis["example"].Close()
		rec := get(t, newTestRouter(t, testConfig(), deps), "/example/visits")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if status := serverSpan(t, recorder).Status(); status.Code != codes.Error {
			t.Errorf("span status = %+v, want error for a 5xx", status)
		}
		if line := accessLine(t, entries()); line["level"] != "ERROR" || line["errors"] == nil {
			t.Errorf("access line = %v, want an ERROR with the cause", line)
		}
	})

	t.Run("negative: probes are not traced", func(t *testing.T) {
		for _, path := range []string{"/health", "/ready"} {
			t.Run(path, func(t *testing.T) {
				deps, recorder, entries := tracedDeps(t)
				get(t, newTestRouter(t, testConfig(), deps), path)
				// Skipping /ready's PING is container.traceRedis's job; only the server span counts here.
				for _, span := range recorder.Ended() {
					if span.SpanKind() == trace.SpanKindServer {
						t.Errorf("server span %q recorded, want none for a probe", span.Name())
					}
				}
				if line := accessLine(t, entries()); line["trace_id"] != nil {
					t.Errorf("access line = %v, want no trace_id", line)
				}
			})
		}
	})

	t.Run("negative: without a provider no span and no trace_id", func(t *testing.T) {
		deps, _, entries := tracedDeps(t)
		deps.TracerProvider = nil
		get(t, newTestRouter(t, testConfig(), deps), "/example/visits")
		if line := accessLine(t, entries()); line["trace_id"] != nil || line["request_id"] == nil {
			t.Errorf("access line = %v, want request_id and no trace_id", line)
		}
	})
}

// Pin for stable numbers: taskset -c 0-7 go test -run '^$' -bench Tracing -count 10 ./internal/api/server/
// Logs go to a discarded stderr; the file, which hides tracing's cost, is BenchmarkLogFile.

// fakeRedis answers INCR in-process, under the instrumentation, so no network hop is timed.
type fakeRedis struct{}

func (fakeRedis) DialHook(next goredis.DialHook) goredis.DialHook { return next }

func (fakeRedis) ProcessHook(goredis.ProcessHook) goredis.ProcessHook {
	var visits atomic.Int64
	return func(_ context.Context, cmd goredis.Cmder) error {
		if incr, ok := cmd.(*goredis.IntCmd); ok {
			incr.SetVal(visits.Add(1))
		}
		return nil
	}
}

func (fakeRedis) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}

var tracingVariants = []struct {
	name  string
	ratio float64 // below 0: tracing.enabled is false
}{{"off", -1}, {"sample-10pct", 0.1}, {"sample-100pct", 1}}

// benchRouter wires the router as container.NewContainer does and returns the collector's byte count;
// equal bytes per request in Parallel and Serial mean no span was dropped.
func benchRouter(b *testing.B, ratio float64, logFile bool) (http.Handler, func() int64) {
	b.Helper()
	// pkg/redis and pkg/database print via the standard logger, which would split benchmark lines.
	previous := stdlog.Writer()
	stdlog.SetOutput(io.Discard)
	b.Cleanup(func() { stdlog.SetOutput(previous) })

	if !logFile {
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			b.Fatal(err)
		}
		stderr := os.Stderr
		os.Stderr = devNull // logger.New takes os.Stderr as it finds it
		b.Cleanup(func() { os.Stderr = stderr; _ = devNull.Close() })
	}
	log, err := logger.New(logger.Config{ServiceName: "bench", Path: b.TempDir(),
		Console: !logFile, DisableFile: !logFile,
		ContextFields: []logger.ContextFields{middleware.LogFields, tracing.LogFields}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = log.Close() })

	client := goredis.NewClient(&goredis.Options{Addr: "unused:6379"})
	b.Cleanup(func() { _ = client.Close() })
	deps := &container.Container{Logger: log, Redis: map[string]*goredis.Client{"example": client}}

	var received atomic.Int64
	flush := func() int64 { return received.Load() }
	if ratio >= 0 {
		collector := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			n, _ := io.Copy(io.Discard, r.Body)
			received.Add(n)
		}))
		provider, err := tracing.New(context.Background(),
			tracing.Config{ServiceName: "bench", Endpoint: collector.URL, SampleRatio: ratio})
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(collector.Close)
		flush = func() int64 { _ = provider.Shutdown(context.Background()); return received.Load() }
		deps.TracerProvider = provider
		// container.traceRedis's options minus its PING filter: these routes send none.
		if err := redisotel.InstrumentTracing(client, redisotel.WithTracerProvider(provider),
			redisotel.WithDBStatement(false), redisotel.WithCallerEnabled(false)); err != nil {
			b.Fatal(err)
		}
	}
	client.AddHook(fakeRedis{}) // last, so it sits innermost, under redisotel

	deps, _ = withPostgres(b, deps)
	router, err := NewRouter(testConfig(), deps)
	if err != nil {
		b.Fatal(err)
	}
	return router, flush
}

func serve(b *testing.B, router http.Handler, req *http.Request) {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		b.Errorf("status = %d, want 200", rec.Code)
	}
}

func BenchmarkTracing(b *testing.B) {
	for _, path := range []string{"/health", "/example/visits"} {
		for _, variant := range tracingVariants {
			b.Run("Serial"+path+"/"+variant.name, func(b *testing.B) {
				router, flush := benchRouter(b, variant.ratio, false)
				req := httptest.NewRequest(http.MethodGet, path, nil)
				b.ReportAllocs()
				for b.Loop() {
					serve(b, router, req)
				}
				reportExported(b, flush)
			})
			b.Run("Parallel"+path+"/"+variant.name, func(b *testing.B) {
				router, flush := benchRouter(b, variant.ratio, false)
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					req := httptest.NewRequest(http.MethodGet, path, nil)
					for pb.Next() {
						serve(b, router, req)
					}
				})
				reportExported(b, flush)
			})
		}
	}
}

// BenchmarkLogFile is the parallel case with the log file on; compare with Parallel/.../off.
func BenchmarkLogFile(b *testing.B) {
	router, _ := benchRouter(b, -1, true)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/example/visits", nil)
		for pb.Next() {
			serve(b, router, req)
		}
	})
}

func reportExported(b *testing.B, flush func() int64) {
	b.StopTimer()
	if sent := flush(); sent > 0 {
		b.ReportMetric(float64(sent)/float64(b.N), "otlp-B/op")
	}
}
