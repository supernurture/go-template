package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/trace"
)

// histogramSeries returns the request histogram's series, keyed "<method> <route> <status>".
func histogramSeries(t *testing.T, reg prometheus.Gatherer) map[string]*dto.Histogram {
	t.Helper()
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := map[string]*dto.Histogram{}
	for _, family := range gathered {
		if family.GetName() != "http_server_request_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			key := strings.Join([]string{labels["http_request_method"], labels["http_route"],
				labels["http_response_status_code"]}, " ")
			out[key] = metric.GetHistogram()
		}
	}
	return out
}

func activeRequests(t *testing.T, reg prometheus.Gatherer, method string) float64 {
	t.Helper()
	gathered, _ := reg.Gather()
	for _, family := range gathered {
		if family.GetName() != "http_server_active_requests" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metric.GetLabel()[0].GetValue() == method {
				return metric.GetGauge().GetValue()
			}
		}
	}
	return -1
}

// metricsRouter mounts Metrics outside Recovery, as NewRouter does, with optional middleware before it.
func metricsRouter(t *testing.T, reg prometheus.Registerer, before ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	requestMetrics, err := Metrics(reg)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	router := gin.New()
	router.Use(before...)
	log, _ := newLogger(t)
	router.Use(requestMetrics, Recovery(log))
	router.GET("/notes/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/panic", func(*gin.Context) { panic("boom") })
	return router
}

func serve(router http.Handler, method, path string) int {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	_, _ = io.Copy(io.Discard, rec.Result().Body)
	return rec.Code
}

// withSpan puts a span context on the request, as otelgin does before Metrics runs.
func withSpan(flags trace.TraceFlags) gin.HandlerFunc {
	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	return func(c *gin.Context) {
		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID, SpanID: trace.SpanID{1}, TraceFlags: flags})
		c.Request = c.Request.WithContext(trace.ContextWithSpanContext(c.Request.Context(), sc))
		c.Next()
	}
}

func TestMetrics(t *testing.T) {
	t.Run("positive: one series per method, route template and status", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		router := metricsRouter(t, reg)
		serve(router, http.MethodGet, "/notes/42")
		serve(router, http.MethodGet, "/notes/43")
		serve(router, http.MethodGet, "/missing")

		got := histogramSeries(t, reg)
		if series := got["GET /notes/:id 204"]; series.GetSampleCount() != 2 {
			t.Errorf("GET /notes/:id 204 = %v, want both requests in one series", series)
		}
		if series := got["GET  404"]; series.GetSampleCount() != 1 {
			t.Errorf("series = %v, want the unmatched request under an empty route", got)
		}
		if active := activeRequests(t, reg, http.MethodGet); active != 0 {
			t.Errorf("active GET = %v, want 0 once every request is done", active)
		}
	})

	t.Run("positive: a panic counts as the 500 Recovery writes", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		if code := serve(metricsRouter(t, reg), http.MethodGet, "/panic"); code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", code)
		}
		if series := histogramSeries(t, reg)["GET /panic 500"]; series.GetSampleCount() != 1 {
			t.Errorf("series = %v, want the panic as a 500", histogramSeries(t, reg))
		}
	})

	t.Run("positive: a sampled request's bucket carries its trace_id as an exemplar", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		serve(metricsRouter(t, reg, withSpan(trace.FlagsSampled)), http.MethodGet, "/notes/1")

		var traceIDs []string
		for _, bucket := range histogramSeries(t, reg)["GET /notes/:id 204"].GetBucket() {
			for _, pair := range bucket.GetExemplar().GetLabel() {
				traceIDs = append(traceIDs, pair.GetValue())
			}
		}
		if len(traceIDs) != 1 || traceIDs[0] != "0af7651916cd43dd8448eb211c80319c" {
			t.Errorf("exemplar trace IDs = %v, want the request's one", traceIDs)
		}
	})

	t.Run("negative: an unsampled request leaves no exemplar", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		serve(metricsRouter(t, reg, withSpan(0)), http.MethodGet, "/notes/1")
		for _, bucket := range histogramSeries(t, reg)["GET /notes/:id 204"].GetBucket() {
			if bucket.GetExemplar() != nil {
				t.Errorf("bucket %v has exemplar %v, want none: the trace was not kept", bucket.GetUpperBound(),
					bucket.GetExemplar())
			}
		}
	})

	t.Run("negative: an unknown method is _OTHER, not a series of its own", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		serve(metricsRouter(t, reg), "PROPFIND", "/notes/1")
		got := histogramSeries(t, reg)
		if len(got) != 1 || got["_OTHER  404"] == nil {
			t.Errorf("series = %v, want only _OTHER", got)
		}
	})

	t.Run("positive: a second router on the same registry shares its series", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		serve(metricsRouter(t, reg), http.MethodGet, "/notes/1")
		serve(metricsRouter(t, reg), http.MethodGet, "/notes/2")
		if series := histogramSeries(t, reg)["GET /notes/:id 204"]; series.GetSampleCount() != 2 {
			t.Errorf("series = %v, want both routers counted together", series)
		}
	})

	t.Run("negative: a clashing registration is an error", func(t *testing.T) {
		for _, name := range []string{"http_server_request_duration_seconds", "http_server_active_requests"} {
			reg := prometheus.NewRegistry()
			reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: "Taken."}))
			if _, err := Metrics(reg); err == nil {
				t.Errorf("%s taken: Metrics = nil error, want the clash reported", name)
			}
		}
	})
}

func BenchmarkMetrics(b *testing.B) {
	gin.SetMode(gin.TestMode)
	requestMetrics, _ := Metrics(prometheus.NewRegistry())
	router := gin.New()
	router.Use(requestMetrics)
	router.GET("/notes/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/notes/1", nil)
	b.ReportAllocs()
	for b.Loop() {
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
}
