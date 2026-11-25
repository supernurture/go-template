package container

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/supernurture/go-template/pkg/metrics"
)

// dbNames lists the db_name of every series in family.
func dbNames(t *testing.T, reg prometheus.Gatherer, family string) []string {
	t.Helper()
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var names []string
	for _, got := range gathered {
		if got.GetName() != family {
			continue
		}
		for _, metric := range got.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetName() == "db_name" {
					names = append(names, pair.GetValue())
				}
			}
		}
	}
	return names
}

func TestMetricsEnabled(t *testing.T) {
	t.Run("positive: every pool is exported under the name /ready reports it by", func(t *testing.T) {
		stubOpeners(t)
		cfg := everyDependency(testConfig(t))
		cfg.Metrics.Enabled = true
		c, err := NewContainer(cfg)
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}
		defer c.Close()

		sql := strings.Join(dbNames(t, c.Metrics, "go_sql_open_connections"), ",")
		if sql != "postgres/primary,sql_server/legacy" {
			t.Errorf("go_sql db_name = %s, want postgres/primary and sql_server/legacy", sql)
		}
		if got := dbNames(t, c.Metrics, "go_redis_pool_hit_total"); len(got) != 1 || got[0] != "redis/cache" {
			t.Errorf("go_redis db_name = %v, want redis/cache", got)
		}
		for name := range c.Pings() {
			if !strings.Contains(sql+",redis/cache", name) {
				t.Errorf("/ready check %s has no pool series under the same name", name)
			}
		}
	})

	t.Run("negative: disabled builds no registry", func(t *testing.T) {
		stubOpeners(t)
		c, err := NewContainer(everyDependency(testConfig(t)))
		if err != nil {
			t.Fatalf("NewContainer: %v", err)
		}
		defer c.Close()
		if c.Metrics != nil {
			t.Error("Metrics registry built with metrics.enabled false")
		}
	})

	t.Run("negative: a pool that cannot be exported fails the open, naming it", func(t *testing.T) {
		for want, taken := range map[string]string{
			`measure postgres "primary"`: "postgres/primary", `measure sql server "legacy"`: "sql_server/legacy",
			`measure redis "cache"`: "redis/cache",
		} {
			t.Run(want, func(t *testing.T) {
				stubOpeners(t)
				c := emptyContainer()
				c.Metrics = metrics.NewRegistry()
				// A collector already under the name makes the pool's registration clash.
				db, _ := mockGorm(t, false)
				sqlDB, _ := db.DB()
				if strings.HasPrefix(taken, "redis/") {
					client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
					_ = metrics.RegisterRedis(c.Metrics, taken, client)
				} else {
					_ = metrics.RegisterSQL(c.Metrics, taken, sqlDB)
				}
				err := c.open(everyDependency(testConfig(t)))
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("open = %v, want it to name %s", err, want)
				}
				_ = c.Close()
			})
		}
	})
}

func TestMeasureGorm(t *testing.T) {
	t.Run("negative: disabled registers nothing", func(t *testing.T) {
		db, _ := mockGorm(t, false)
		if err := emptyContainer().measureGorm(db, "postgres/primary"); err != nil {
			t.Errorf("measureGorm = %v, want nil with no registry", err)
		}
	})

	t.Run("negative: no connection pool", func(t *testing.T) {
		c := emptyContainer()
		c.Metrics = metrics.NewRegistry()
		if err := c.measureGorm(brokenGorm(), "postgres/primary"); err == nil {
			t.Error("measureGorm = nil error, want the missing pool reported")
		}
	})
}

func TestMeasureRedis(t *testing.T) {
	t.Run("negative: disabled registers nothing", func(t *testing.T) {
		client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
		t.Cleanup(func() { _ = client.Close() })
		if err := emptyContainer().measureRedis(client, "redis/cache"); err != nil {
			t.Errorf("measureRedis = %v, want nil with no registry", err)
		}
	})
}
