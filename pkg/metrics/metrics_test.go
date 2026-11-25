package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	goredis "github.com/redis/go-redis/v9"
)

// families gathers reg into its metric families, keyed by name.
func families(t *testing.T, reg prometheus.Gatherer) map[string]*dto.MetricFamily {
	t.Helper()
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(gathered))
	for _, family := range gathered {
		out[family.GetName()] = family
	}
	return out
}

func label(metric *dto.Metric, name string) string {
	for _, pair := range metric.GetLabel() {
		if pair.GetName() == name {
			return pair.GetValue()
		}
	}
	return ""
}

func scrape(t *testing.T, handler http.Handler, accept string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Result(), string(body)
}

// broken is a collector whose every scrape fails.
type broken struct{ desc *prometheus.Desc }

func (b broken) Describe(ch chan<- *prometheus.Desc) { ch <- b.desc }
func (b broken) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.NewInvalidMetric(b.desc, io.ErrUnexpectedEOF)
}

func TestNewRegistry(t *testing.T) {
	t.Run("positive: the Go runtime and the process from the start", func(t *testing.T) {
		got := families(t, NewRegistry())
		for _, name := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "process_cpu_seconds_total",
			"process_resident_memory_bytes", "process_open_fds"} {
			if got[name] == nil {
				t.Errorf("no %s", name)
			}
		}
	})

	t.Run("negative: each call is a registry of its own", func(t *testing.T) {
		first := NewRegistry()
		if err := first.Register(prometheus.NewCounter(prometheus.CounterOpts{Name: "only_in_first"})); err != nil {
			t.Fatalf("Register: %v", err)
		}
		if families(t, NewRegistry())["only_in_first"] != nil {
			t.Error("a second registry sees the first one's series")
		}
	})
}

func TestHandler(t *testing.T) {
	t.Run("positive: the text format by default, with the handler's own counts", func(t *testing.T) {
		resp, body := scrape(t, Handler(NewRegistry()), "")
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
			t.Fatalf("scrape = %d %s, want 200 text/plain", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		for _, want := range []string{"go_goroutines ", "promhttp_metric_handler_requests_total"} {
			if !strings.Contains(body, want) {
				t.Errorf("body lacks %s", want)
			}
		}
	})

	t.Run("positive: OpenMetrics when the scraper asks, so exemplars can travel", func(t *testing.T) {
		resp, body := scrape(t, Handler(NewRegistry()), "application/openmetrics-text; version=1.0.0")
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/openmetrics-text") ||
			!strings.HasSuffix(body, "# EOF\n") {
			tail := body[max(0, len(body)-10):]
			t.Errorf("scrape = %s, ends %q; want OpenMetrics", resp.Header.Get("Content-Type"), tail)
		}
	})

	t.Run("positive: a second handler on the same registry shares the scrape counts", func(t *testing.T) {
		reg := NewRegistry()
		_, _ = scrape(t, Handler(reg), "")
		_, body := scrape(t, Handler(reg), "")
		if !strings.Contains(body, `promhttp_metric_handler_requests_total{code="200"} 1`) {
			t.Errorf("body lacks the first scrape's count:\n%s", body)
		}
	})

	t.Run("negative: one failing collector leaves the rest of the scrape", func(t *testing.T) {
		reg := NewRegistry()
		reg.MustRegister(broken{prometheus.NewDesc("pool_broken", "Fails.", nil, nil)})
		resp, body := scrape(t, Handler(reg), "")
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "go_goroutines ") ||
			strings.Contains(body, "pool_broken ") {
			t.Errorf("scrape = %d, want 200 with the healthy series and without the broken one", resp.StatusCode)
		}
	})
}

func TestRegisterSQL(t *testing.T) {
	t.Run("positive: the pool's figures under db_name", func(t *testing.T) {
		db, _, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		reg := prometheus.NewRegistry()
		if err := RegisterSQL(reg, "postgres/primary", db); err != nil {
			t.Fatalf("RegisterSQL: %v", err)
		}
		family := families(t, reg)["go_sql_open_connections"]
		if family == nil || label(family.GetMetric()[0], "db_name") != "postgres/primary" {
			t.Errorf("go_sql_open_connections = %v, want it labelled db_name=postgres/primary", family)
		}
	})

	t.Run("negative: the same name twice", func(t *testing.T) {
		db, _, _ := sqlmock.New()
		t.Cleanup(func() { _ = db.Close() })
		reg := prometheus.NewRegistry()
		_ = RegisterSQL(reg, "postgres/primary", db)
		if err := RegisterSQL(reg, "postgres/primary", db); err == nil {
			t.Error("RegisterSQL = nil error, want the duplicate refused")
		}
	})
}

func TestRegisterRedis(t *testing.T) {
	newClient := func(t *testing.T) *goredis.Client {
		t.Helper()
		client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"}) // pool stats need no server
		t.Cleanup(func() { _ = client.Close() })
		return client
	}

	t.Run("positive: the pool's figures under db_name, as go_sql_* has them", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		if err := RegisterRedis(reg, "redis/cache", newClient(t)); err != nil {
			t.Fatalf("RegisterRedis: %v", err)
		}
		got := families(t, reg)
		for _, name := range []string{"go_redis_pool_hit_total", "go_redis_pool_timeout_total",
			"go_redis_pool_conn_total_current"} {
			if got[name] == nil || label(got[name].GetMetric()[0], "db_name") != "redis/cache" {
				t.Errorf("%s = %v, want it labelled db_name=redis/cache", name, got[name])
			}
		}
	})

	t.Run("negative: the same name twice", func(t *testing.T) {
		reg, client := prometheus.NewRegistry(), newClient(t)
		_ = RegisterRedis(reg, "redis/cache", client)
		if err := RegisterRedis(reg, "redis/cache", client); err == nil {
			t.Error("RegisterRedis = nil error, want the duplicate refused")
		}
	})
}

func TestReuse(t *testing.T) {
	counter := func() *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "calls_total", Help: "Calls."}, []string{"kind"})
	}

	t.Run("positive: the first call registers, the second gets the same collector back", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		first, err := Reuse(reg, counter())
		if err != nil {
			t.Fatalf("first Reuse: %v", err)
		}
		second, err := Reuse(reg, counter())
		if err != nil || second != first {
			t.Errorf("second Reuse = %p, %v; want the first collector %p", second, err, first)
		}
	})

	t.Run("negative: a conflicting registration is an error", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{Name: "calls_total", Help: "Other."}))
		if _, err := Reuse(reg, counter()); err == nil {
			t.Error("Reuse = nil error, want the clash reported")
		}
	})

	t.Run("negative: the same descriptors under another type is an error", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		reg.MustRegister(counter())
		gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "calls_total", Help: "Calls."}, []string{"kind"})
		if got, err := Reuse(reg, gauge); err == nil || got != nil {
			t.Errorf("Reuse = %v, %v; want no collector and the clash reported", got, err)
		}
	})
}
