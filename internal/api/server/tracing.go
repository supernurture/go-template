package server

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel/trace"

	"github.com/supernurture/go-template/pkg/tracing"
)

// serverTracing starts a server span per request, continuing the caller's traceparent if it sent one.
func serverTracing(service string, provider trace.TracerProvider) gin.HandlerFunc {
	return otelgin.Middleware(service,
		otelgin.WithTracerProvider(provider),
		otelgin.WithPropagators(tracing.Propagator()),
		otelgin.WithGinFilter(notProbe))
}

// notProbe keeps /health and /ready out of traces: probes would outnumber real requests.
func notProbe(c *gin.Context) bool {
	path := c.Request.URL.Path
	return path != "/health" && path != "/ready"
}
