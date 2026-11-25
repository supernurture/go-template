package container

import (
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/supernurture/go-template/pkg/metrics"
)

// measureGorm exports the pool's stats under name, the key /ready uses.
func (c *Container) measureGorm(conn *gorm.DB, name string) error {
	if c.Metrics == nil {
		return nil
	}
	sqlDB, err := conn.DB()
	if err != nil {
		return err
	}
	return metrics.RegisterSQL(c.Metrics, name, sqlDB)
}

// measureRedis exports the client's pool stats under name.
func (c *Container) measureRedis(client *goredis.Client, name string) error {
	if c.Metrics == nil {
		return nil
	}
	return metrics.RegisterRedis(c.Metrics, name, client)
}
