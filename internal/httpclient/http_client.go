package httpclient

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/pkg/util"
	"github.com/supernurture/go-template/pkg/httpclient"
	"github.com/supernurture/go-template/pkg/logger"
	"github.com/supernurture/go-template/pkg/tracing"
)

// HTTPClient bundles the app's configured upstream HTTP clients.
type HTTPClient struct {
	Example *httpclient.Client
}

// NewHTTPClient builds every upstream HTTP client from config. A non-nil provider gives each call a
// client span and sends the traceparent header, so the upstream's spans join the caller's trace.
func NewHTTPClient(cfg *config.Config, log *logger.Logger, provider trace.TracerProvider) *HTTPClient {
	return &HTTPClient{
		Example: newExampleClient(cfg, log, provider),
	}
}

// An unconfigured service has no base URL and no credentials to leak, so it is not warned about.
func warnIfNotHTTPS(log *logger.Logger, service, baseURL string) {
	if baseURL != "" && !strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		log.Warn(service+" base URL is not HTTPS; basic-auth credentials will be sent in cleartext",
			map[string]any{"base_url": baseURL})
	}
}

func newExampleClient(cfg *config.Config, log *logger.Logger, provider trace.TracerProvider) *httpclient.Client {
	example := cfg.Services["example"]

	warnIfNotHTTPS(log, "Example", example.BaseURL)

	opts := []httpclient.Option{
		httpclient.WithTimeout(example.Timeout),
		httpclient.WithBaseURL(example.BaseURL),
	}
	// Setting it unconditionally would send an empty Authorization header on every request.
	if auth := util.GenerateBasicAuth(example.Auth.User, example.Auth.Password); auth != "" {
		opts = append(opts, httpclient.WithHeader("Authorization", auth))
	}
	if provider != nil {
		opts = append(opts, httpclient.WithTransport(tracedTransport(provider)))
	}

	return httpclient.New(opts...)
}

func tracedTransport(provider trace.TracerProvider) http.RoundTripper {
	return otelhttp.NewTransport(http.DefaultTransport,
		otelhttp.WithTracerProvider(provider), otelhttp.WithPropagators(tracing.Propagator()))
}
