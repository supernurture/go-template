// Package metrics keeps a Prometheus registry for a host agent to scrape: the Go runtime and the process
// from the start, connection pools and HTTP requests as the app registers them.
package metrics

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/extra/redisprometheus/v9"
	goredis "github.com/redis/go-redis/v9"
)

// NewRegistry returns a registry holding the Go runtime (goroutines, heap, GC) and process (CPU, memory,
// file descriptors) collectors. A registry of its own, not the global default, so tests and a second app
// in the same process never collide.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// Handler serves reg in the Prometheus text format, or OpenMetrics when the scraper asks for it: only
// OpenMetrics carries the exemplars that link a latency bucket to a trace. It counts its own scrapes and
// errors in reg as promhttp_metric_handler_*, reusing those series when called again on the same reg.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.InstrumentMetricHandler(reg, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
		// One collector failing must not blank the rest of a scrape.
		ErrorHandling: promhttp.ContinueOnError,
	}))
}

// RegisterSQL adds a pool's open, idle and in-use connections and its waits as go_sql_*{db_name=name}.
func RegisterSQL(reg prometheus.Registerer, name string, db *sql.DB) error {
	return reg.Register(collectors.NewDBStatsCollector(db, name))
}

// RegisterRedis adds a client's pool hits, misses, timeouts and connections as go_redis_*{db_name=name},
// the label go_sql_* uses, so one dashboard variable selects either.
func RegisterRedis(reg prometheus.Registerer, name string, client *goredis.Client) error {
	labeled := prometheus.WrapRegistererWith(prometheus.Labels{"db_name": name}, reg)
	return labeled.Register(redisprometheus.NewCollector("go", "redis", client))
}

// Reuse registers c, or returns the collector already registered under the same descriptors, so a
// constructor called twice on one registry (a second router in a test) shares one set of series.
func Reuse[C prometheus.Collector](reg prometheus.Registerer, c C) (C, error) {
	err := reg.Register(c)
	if err == nil {
		return c, nil
	}
	if existing, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); ok {
		if same, ok := existing.ExistingCollector.(C); ok {
			return same, nil
		}
	}
	var zero C
	return zero, err
}
