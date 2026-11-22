package tracing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// collector stands in for Alloy or Tempo: it records the path and body of every OTLP export.
type collector struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func newCollector(t *testing.T) (*collector, string) {
	t.Helper()
	got := &collector{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.paths, got.bodies = append(got.paths, r.URL.Path), append(got.bodies, string(body))
		got.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return got, server.URL
}

func (c *collector) received() (paths []string, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...), strings.Join(c.bodies, "")
}

// exportOne starts and ends a root span, then flushes it through Shutdown.
func exportOne(t *testing.T, provider *sdktrace.TracerProvider, name string) {
	t.Helper()
	_, span := provider.Tracer("test").Start(context.Background(), name)
	span.End()
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func sampled(parent context.Context, provider *sdktrace.TracerProvider) bool {
	_, span := provider.Tracer("test").Start(parent, "x")
	defer span.End()
	return span.SpanContext().IsSampled()
}

func remoteParent(flags trace.TraceFlags) context.Context {
	return trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: flags, Remote: true,
	}))
}

func TestNew(t *testing.T) {
	t.Run("positive: exports to <endpoint>/v1/traces with the service on the resource", func(t *testing.T) {
		for name, suffix := range map[string]string{"bare": "", "trailing slash": "/"} {
			t.Run(name, func(t *testing.T) {
				got, url := newCollector(t)
				provider, err := New(context.Background(), Config{
					ServiceName: "svc-under-test", ServiceVersion: "9.9.9", Env: "staging",
					Endpoint: url + suffix, SampleRatio: 1,
				})
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				exportOne(t, provider, "span-under-test")

				paths, body := got.received()
				if len(paths) != 1 || paths[0] != "/v1/traces" {
					t.Fatalf("export paths = %v, want one POST to /v1/traces", paths)
				}
				for _, want := range []string{"span-under-test", "svc-under-test", "9.9.9", "staging"} {
					if !strings.Contains(body, want) {
						t.Errorf("export does not carry %q", want)
					}
				}
			})
		}
	})

	t.Run("positive: an empty endpoint falls back to OTEL_EXPORTER_OTLP_ENDPOINT", func(t *testing.T) {
		got, url := newCollector(t)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", url)
		provider, err := New(context.Background(), Config{ServiceName: "svc", SampleRatio: 1})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		exportOne(t, provider, "from-env")
		if paths, _ := got.received(); len(paths) != 1 {
			t.Errorf("exports = %v, want one to the collector the environment names", paths)
		}
	})

	t.Run("positive: OTEL_SERVICE_NAME overrides the config", func(t *testing.T) {
		got, url := newCollector(t)
		t.Setenv("OTEL_SERVICE_NAME", "renamed-by-deploy")
		provider, err := New(context.Background(), Config{ServiceName: "from-config", Endpoint: url, SampleRatio: 1})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		exportOne(t, provider, "x")
		_, body := got.received()
		if !strings.Contains(body, "renamed-by-deploy") || strings.Contains(body, "from-config") {
			t.Error("service.name is not the one OTEL_SERVICE_NAME set")
		}
	})

	t.Run("positive: the ratio samples new traces, a caller's decision wins", func(t *testing.T) {
		tests := []struct {
			ratio  float64
			parent context.Context
			want   bool
		}{
			{1, context.Background(), true},
			{0, context.Background(), false},
			{0, remoteParent(trace.FlagsSampled), true}, // the caller kept it, so this hop must too
			{1, remoteParent(0), false},                 // the caller dropped it
		}
		for _, test := range tests {
			provider, err := New(context.Background(), Config{Endpoint: "http://127.0.0.1:1", SampleRatio: test.ratio})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := sampled(test.parent, provider); got != test.want {
				t.Errorf("ratio %v, parent sampled %v: sampled = %v, want %v", test.ratio,
					trace.SpanContextFromContext(test.parent).IsSampled(), got, test.want)
			}
			_ = provider.Shutdown(context.Background())
		}
	})

	t.Run("negative: a sample ratio outside 0..1", func(t *testing.T) {
		for _, ratio := range []float64{-0.1, 1.01} {
			if _, err := New(context.Background(), Config{SampleRatio: ratio}); err == nil ||
				!strings.Contains(err.Error(), "sample ratio") {
				t.Errorf("ratio %v: error = %v, want it refused", ratio, err)
			}
		}
	})

	t.Run("negative: an endpoint that is not an http(s) URL with a host", func(t *testing.T) {
		for _, endpoint := range []string{"localhost:4318", "grpc://alloy:4317", "http://", "http://[::1"} {
			if _, err := New(context.Background(), Config{Endpoint: endpoint}); err == nil ||
				!strings.Contains(err.Error(), "tracing endpoint") {
				t.Errorf("endpoint %q: error = %v, want it refused", endpoint, err)
			}
		}
	})

	t.Run("negative: the exporter cannot be built", func(t *testing.T) {
		boom := errors.New("boom")
		orig := newExporter
		newExporter = func(context.Context, ...otlptracehttp.Option) (*otlptrace.Exporter, error) { return nil, boom }
		t.Cleanup(func() { newExporter = orig })
		if _, err := New(context.Background(), Config{}); !errors.Is(err, boom) {
			t.Errorf("error = %v, want the exporter failure", err)
		}
	})

	t.Run("negative: OTEL_RESOURCE_ATTRIBUTES does not parse", func(t *testing.T) {
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "no-equals-sign")
		_, err := New(context.Background(), Config{})
		if err == nil || !strings.Contains(err.Error(), "build resource") {
			t.Errorf("error = %v, want the resource failure", err)
		}
	})
}

func TestPropagator(t *testing.T) {
	t.Run("positive: a traceparent round-trips with its baggage", func(t *testing.T) {
		header := propagation.HeaderCarrier{}
		header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
		header.Set("baggage", "tenant=acme")

		ctx := Propagator().Extract(context.Background(), header)
		out := propagation.HeaderCarrier{}
		Propagator().Inject(ctx, out)
		if got := out.Get("traceparent"); got != header.Get("traceparent") {
			t.Errorf("traceparent = %q, want %q", got, header.Get("traceparent"))
		}
		if got := out.Get("baggage"); got != "tenant=acme" {
			t.Errorf("baggage = %q, want tenant=acme", got)
		}
	})

	t.Run("negative: nothing to inject without a span", func(t *testing.T) {
		out := propagation.HeaderCarrier{}
		Propagator().Inject(context.Background(), out)
		if len(out.Keys()) != 0 {
			t.Errorf("headers = %v, want none", out.Keys())
		}
	})
}

func TestLogFields(t *testing.T) {
	t.Run("positive: the span's IDs as lowercase hex under the spec's names", func(t *testing.T) {
		traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
		spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
		ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
		}))

		got := LogFields(ctx)
		want := map[string]any{
			"trace_id": "0af7651916cd43dd8448eb211c80319c", "span_id": "b7ad6b7169203331", "trace_flags": "01",
		}
		if len(got) != len(want) {
			t.Fatalf("fields = %v, want %v", got, want)
		}
		for key, value := range want {
			if got[key] != value {
				t.Errorf("%s = %v, want %v", key, got[key], value)
			}
		}
	})

	t.Run("positive: an unsampled span still links its line, flagged 00", func(t *testing.T) {
		if got := LogFields(remoteParent(0)); got["trace_flags"] != "00" || got["trace_id"] == nil {
			t.Errorf("fields = %v, want the IDs with trace_flags 00", got)
		}
	})

	t.Run("negative: a context without a span adds nothing", func(t *testing.T) {
		if got := LogFields(context.Background()); got != nil {
			t.Errorf("fields = %v, want nil", got)
		}
	})
}

func BenchmarkLogFields(b *testing.B) {
	ctx := remoteParent(trace.FlagsSampled)
	b.ReportAllocs()
	for b.Loop() {
		_ = LogFields(ctx)
	}
}
