package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/supernurture/go-template/pkg/metrics"
)

// The OpenTelemetry HTTP semantic conventions' names and buckets, so these series line up with what an
// OTel-instrumented service in another language exports.
var requestDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// knownMethods bounds the method label: anything else is _OTHER, so a scanner cannot mint a series per verb.
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodConnect: true, http.MethodOptions: true,
	http.MethodTrace: true,
}

// Metrics records every request's duration by method, route template and status, and the requests in
// flight. Mount it outside Recovery, so a panic counts as the 500 it becomes, and inside the tracing
// middleware, so a sampled request's bucket carries its trace_id as an exemplar.
func Metrics(reg prometheus.Registerer) (gin.HandlerFunc, error) {
	duration, err := metrics.Reuse(reg, prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_server_request_duration_seconds",
		Help:    "Duration of HTTP server requests.",
		Buckets: requestDurationBuckets,
	}, []string{"http_request_method", "http_route", "http_response_status_code"}))
	if err != nil {
		return nil, err
	}
	active, err := metrics.Reuse(reg, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "http_server_active_requests",
		Help: "Number of HTTP server requests in flight.",
	}, []string{"http_request_method"}))
	if err != nil {
		return nil, err
	}

	return func(c *gin.Context) {
		method := c.Request.Method
		if !knownMethods[method] {
			method = "_OTHER"
		}
		inFlight := active.WithLabelValues(method)
		inFlight.Inc()
		defer inFlight.Dec() // also when a panic Recovery rethrows (http.ErrAbortHandler) unwinds through here

		start := time.Now()
		c.Next()

		// The route template, never the path: /notes/42 and /notes/43 are one series. Unmatched is "".
		observer := duration.WithLabelValues(method, c.FullPath(), strconv.Itoa(c.Writer.Status()))
		elapsed := time.Since(start).Seconds()
		sc := trace.SpanContextFromContext(c.Request.Context())
		if exemplars, ok := observer.(prometheus.ExemplarObserver); ok && sc.IsSampled() {
			exemplars.ObserveWithExemplar(elapsed, prometheus.Labels{"trace_id": sc.TraceID().String()})
			return
		}
		observer.Observe(elapsed)
	}, nil
}
