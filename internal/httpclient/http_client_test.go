package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/pkg/logger"
)

func newTestLogger(t *testing.T) (*logger.Logger, func() string) {
	t.Helper()

	dir := t.TempDir()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: dir, Level: "DEBUG"})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	return log, func() string {
		if err := log.Close(); err != nil {
			t.Fatalf("close logger: %v", err)
		}
		files, err := filepath.Glob(filepath.Join(dir, "test", "*"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(files) == 0 {
			return ""
		}
		contents, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		return string(contents)
	}
}

func testConfig(baseURL string) *config.Config {
	cfg := &config.Config{}
	cfg.Services = map[string]config.Service{
		"example": {
			BaseURL: baseURL,
			Timeout: 5 * time.Second,
			Auth:    config.ServiceAuth{User: "user", Password: "password"},
		},
	}
	return cfg
}

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	fn()
}

// authSeen sends one request through a client built from auth and returns the Authorization values received.
func authSeen(t *testing.T, auth config.ServiceAuth) []string {
	t.Helper()
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header["Authorization"]
	}))
	defer server.Close()

	log, _ := newTestLogger(t)
	cfg := testConfig(server.URL)
	service := cfg.Services["example"]
	service.Auth = auth
	cfg.Services["example"] = service

	resp, err := newExampleClient(cfg, log, nil).Do(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	return got
}

// traceparentSeen sends one request under a parent span and returns the traceparent the upstream received.
func traceparentSeen(parent context.Context, t *testing.T, provider trace.TracerProvider) string {
	t.Helper()
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
	}))
	defer server.Close()

	log, _ := newTestLogger(t)
	resp, err := NewHTTPClient(testConfig(server.URL), log, provider).Example.
		Do(parent, http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	return got
}

func TestNewHTTPClient(t *testing.T) {
	t.Run("positive: builds every upstream", func(t *testing.T) {
		log, _ := newTestLogger(t)
		if clients := NewHTTPClient(testConfig("https://api.example.com"), log, nil); clients.Example == nil {
			t.Error("Example client was not built")
		}
	})

	t.Run("negative: an empty config still builds every upstream", func(t *testing.T) {
		log, _ := newTestLogger(t)
		if clients := NewHTTPClient(&config.Config{}, log, nil); clients.Example == nil {
			t.Error("Example client was not built from an empty config")
		}
	})

	t.Run("negative: a nil config panics", func(t *testing.T) {
		log, _ := newTestLogger(t)
		mustPanic(t, "NewHTTPClient(nil, log)", func() { NewHTTPClient(nil, log, nil) })
	})
}

func TestTracedClient(t *testing.T) {
	t.Run("positive: an upstream call is a client span whose traceparent reaches the upstream", func(t *testing.T) {
		recorder := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
		parent, handler := provider.Tracer("test").Start(context.Background(), "handler")

		got := traceparentSeen(parent, t, provider)
		handler.End()

		var client sdktrace.ReadOnlySpan
		for _, span := range recorder.Ended() {
			if span.SpanKind() == trace.SpanKindClient {
				client = span
			}
		}
		if client == nil || client.Parent().SpanID() != handler.SpanContext().SpanID() {
			t.Fatalf("client span = %v, want a child of the handler's span", client)
		}
		want := "00-" + client.SpanContext().TraceID().String() + "-" + client.SpanContext().SpanID().String() + "-01"
		if got != want {
			t.Errorf("traceparent = %q, want %q: the upstream must see the client span as its parent", got, want)
		}
	})

	t.Run("negative: without a provider no traceparent is sent", func(t *testing.T) {
		provider := sdktrace.NewTracerProvider()
		parent, span := provider.Tracer("test").Start(context.Background(), "handler")
		defer span.End()
		if got := traceparentSeen(parent, t, nil); got != "" {
			t.Errorf("traceparent = %q, want none", got)
		}
	})
}

func TestWarnIfNotHTTPS(t *testing.T) {
	warned := func(t *testing.T, baseURL string) bool {
		t.Helper()
		log, logged := newTestLogger(t)
		warnIfNotHTTPS(log, "Example", baseURL)
		return strings.Contains(logged(), "not HTTPS")
	}

	// Nothing configured means no credentials to send in cleartext.
	t.Run("positive: TLS or unconfigured URLs stay quiet", func(t *testing.T) {
		for _, baseURL := range []string{"https://api.example.com", "HTTPS://api.example.com", ""} {
			if warned(t, baseURL) {
				t.Errorf("warned for %q, want quiet", baseURL)
			}
		}
	})

	t.Run("negative: plaintext or schemeless URLs warn", func(t *testing.T) {
		for _, baseURL := range []string{"http://api.example.com", "api.example.com", " https://api.example.com"} {
			if !warned(t, baseURL) {
				t.Errorf("no warning for %q", baseURL)
			}
		}
	})

	t.Run("negative: a nil logger panics only when it has to warn", func(t *testing.T) {
		warnIfNotHTTPS(nil, "Example", "https://api.example.com")
		mustPanic(t, "warnIfNotHTTPS(nil, http://...)", func() { warnIfNotHTTPS(nil, "Example", "http://x") })
	})
}

// An empty Authorization header is worse than none: some upstreams reject the malformed
// value, and it says the request is authenticated when nothing was configured.
func TestNewExampleClient(t *testing.T) {
	t.Run("positive: sends basic auth to the configured base URL", func(t *testing.T) {
		got := authSeen(t, config.ServiceAuth{User: "user", Password: "password"})
		if len(got) != 1 || got[0] != "Basic dXNlcjpwYXNzd29yZA==" {
			t.Errorf("Authorization = %q, want the basic-auth value", got)
		}
	})

	t.Run("negative: partial or missing credentials send no header", func(t *testing.T) {
		for _, auth := range []config.ServiceAuth{{}, {Password: "password"}, {User: "user"}} {
			if got := authSeen(t, auth); len(got) != 0 {
				t.Errorf("auth %+v: Authorization = %q, want the header absent", auth, got)
			}
		}
	})

	// A missing service is only noticed on the first request, not at startup.
	t.Run("negative: an unconfigured service fails on use", func(t *testing.T) {
		log, _ := newTestLogger(t)
		client := newExampleClient(&config.Config{}, log, nil)
		if _, err := client.Do(context.Background(), http.MethodGet, "/x", nil); err == nil {
			t.Error("Do = nil error, want a failure for a client with no base URL")
		}
	})
}
