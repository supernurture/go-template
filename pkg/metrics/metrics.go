// Package metrics builds the Prometheus registry a host agent scrapes.
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

// NewRegistry returns a registry with the Go runtime and process collectors. Not the global default,
// so tests never collide.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// Handler serves reg as Prometheus text, or OpenMetrics on request, the only format carrying exemplars.
// It counts its own scrapes in reg, reusing the series when called again.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.InstrumentMetricHandler(reg, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
		// One collector failing must not blank the rest of a scrape.
		ErrorHandling: promhttp.ContinueOnError,
	}))
}

// RegisterSQL exports a pool's stats as go_sql_*{db_name=name}.
func RegisterSQL(reg prometheus.Registerer, name string, db *sql.DB) error {
	return reg.Register(collectors.NewDBStatsCollector(db, name))
}

// RegisterRedis exports a client's pool stats as go_redis_*{db_name=name}, the label go_sql_* uses.
func RegisterRedis(reg prometheus.Registerer, name string, client *goredis.Client) error {
	labeled := prometheus.WrapRegistererWith(prometheus.Labels{"db_name": name}, reg)
	return labeled.Register(redisprometheus.NewCollector("go", "redis", client))
}

// Reuse registers c, or returns the collector already registered with its descriptors, so a second
// router on one registry shares the series.
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
