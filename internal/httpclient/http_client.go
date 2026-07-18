package httpclient

import (
	"strings"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/pkg/util"
	"github.com/supernurture/go-template/pkg/httpclient"
	"github.com/supernurture/go-template/pkg/logger"
)

// HTTPClient bundles the app's configured upstream HTTP clients.
type HTTPClient struct {
	Example *httpclient.Client
}

// NewHTTPClient builds every upstream HTTP client from config.
func NewHTTPClient(cfg *config.Config, log *logger.Logger) *HTTPClient {
	return &HTTPClient{
		Example: newExampleClient(cfg, log),
	}
}

func warnIfNotHTTPS(log *logger.Logger, service, baseURL string) {
	if !strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		log.Warn(service+" base URL is not HTTPS; basic-auth credentials will be sent in cleartext", map[string]any{"base_url": baseURL})
	}
}

func newExampleClient(cfg *config.Config, log *logger.Logger) *httpclient.Client {
	example := cfg.Services["example"]

	warnIfNotHTTPS(log, "Example", example.BaseURL)

	return httpclient.New(
		httpclient.WithHeader("Authorization", util.GenerateBasicAuth(example.Auth.User, example.Auth.Password)),
		httpclient.WithTimeout(example.Timeout),
		httpclient.WithBaseURL(example.BaseURL),
	)
}
