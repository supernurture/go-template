package postgre

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	Name     string
}

func PostgreInit(ctx context.Context, config Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx,
		fmt.Sprintf("postgres://%s:%s@%s:%d/%s", config.Username, config.Password, config.Host, config.Port, config.Name))
	if err != nil {
		return nil, fmt.Errorf("unable to create postgre connection pool for database %s: %w", config.Name, err)
	}

	log.Printf("successfully created postgre connection pool for database: %s \n", config.Name)
	return pool, nil
}
