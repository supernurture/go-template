package config

import (
	"fmt"
	"strings"
	"time"

	env "github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type ApplicationConfig struct {
	Name        string
	Version     string
	Environment string
}

type ServerConfig struct {
	Port    int
	Timeout time.Duration
}

type DatabaseConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	Name     string
}

type DatabasesConfig struct {
	Postgre map[string]DatabaseConfig
}

type LoggerConfig struct {
	Format string
	Level  string
	Output string
}

type Config struct {
	Application ApplicationConfig
	Server      ServerConfig
	Databases   DatabasesConfig
	Logger      LoggerConfig
}

const (
	configName = "config"
	configType = "yaml"
	configPath = "configs/"
)

func loadFile() error {
	viper.SetConfigName(configName)
	viper.SetConfigType(configType)
	viper.AddConfigPath(configPath)

	err := viper.ReadInConfig()
	if err != nil {
		return fmt.Errorf("unable to load the configuration file: %w", err)
	}

	return nil
}

func loadConsul() error {
	return nil
}

func Load() (*Config, error) {
	var (
		err error

		environment string
		config      Config
	)

	err = env.Load(".env")
	if err != nil {
		fmt.Println("unable to load the .env file, relying on system environment variables")
	}

	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	environment = viper.GetString("application.environment")
	switch environment {
	case "development":
		err = loadFile()
		if err != nil {
			return nil, err
		}
	case "sit", "uat", "production":
		err = loadConsul()
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown environment %s: must be development, sit, uat, or production", environment)
	}

	err = viper.Unmarshal(&config)
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal the config into the struct: %w", err)
	}

	return &config, nil
}
