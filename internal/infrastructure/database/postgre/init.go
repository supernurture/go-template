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

func Init(config Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(context.Background(),
		fmt.Sprintf("postgres://%s:%s@%s:%d/%s", config.Username, config.Password, config.Host, config.Port, config.Name))
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}

	log.Printf("successfully create connection pool for database %s \n", config.Name)
	return pool, nil
}
