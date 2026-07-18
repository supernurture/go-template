package container

import (
	"github.com/supernurture/go-template/internal/config"
)

// Container holds shared infrastructure dependencies used across the app.
type Container struct {
	shutdowns []func() error
}

// NewContainer builds the Container: logger, database connections, HTTP client, and their shutdown hooks. Call Close when done.
func NewContainer(cfg *config.Config) (*Container, error) {
	return &Container{
		// Shutdown hooks run in order, logger last so earlier hooks can still log.
		shutdowns: []func() error{},
	}, nil
}

// Close runs every registered shutdown hook, returning on the first error.
func (c *Container) Close() error {
	for _, shutdown := range c.shutdowns {
		if err := shutdown(); err != nil {
			return err
		}
	}

	return nil
}
