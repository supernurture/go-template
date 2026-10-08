package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

const validConfig = `
app:
  name: template
  version: 1.0.0
  env: development
server:
  mode: test
  port: 8080
  timeout: 40s
  trusted_proxies: ["10.0.0.0/8"]
logger:
  level: INFO
`

const datastoreConfig = validConfig + `
databases:
  postgres:
    primary:
      host: db.internal
      port: 5432
      user: app
      password: secret
      database: app
      max_open_conns: 25
      max_idle_conns: 5
      conn_max_lifetime: 30m
redis:
  cache:
    host: cache.internal
    port: 6379
    db: 2
    pool_size: 20
    min_idle_conns: 4
    conn_max_lifetime: 1h
`

func service(baseURL, endpoints string) string {
	return fmt.Sprintf("services:\n  upstream:\n    base_url: %s\n    endpoints:\n      %s\n    timeout: 10s\n",
		baseURL, endpoints)
}

func chdirTemp(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func writeFile(t *testing.T, name, contents string) {
	t.Helper()

	if dir := filepath.Dir(name); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func configFile() string {
	return filepath.Join(configPath, configName+"."+configType)
}

func repoFile(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

func loadFrom(t *testing.T, contents string) (*Config, error) {
	t.Helper()
	chdirTemp(t)
	writeFile(t, configFile(), contents)
	return Load()
}

func mustLoad(t *testing.T, contents string) *Config {
	t.Helper()
	cfg, err := loadFrom(t, contents)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoad(t *testing.T) {
	t.Run("positive: loads, decodes and validates the config", func(t *testing.T) {
		t.Run("minimal file", func(t *testing.T) {
			cfg := mustLoad(t, validConfig)
			if cfg.App.Name != "template" || cfg.Server.Timeout != 40*time.Second {
				t.Errorf("app.name = %q, server.timeout = %v, want template and 40s", cfg.App.Name, cfg.Server.Timeout)
			}
			if got := cfg.Server.TrustedProxies; len(got) != 1 || got[0] != "10.0.0.0/8" {
				t.Errorf("server.trusted_proxies = %v, want [10.0.0.0/8]", got)
			}
			if len(cfg.Services) != 0 || len(cfg.Databases.Postgres) != 0 {
				t.Errorf("services = %v, postgres = %v, want both empty", cfg.Services, cfg.Databases.Postgres)
			}
		})

		t.Run("datastore pools", func(t *testing.T) {
			cfg := mustLoad(t, datastoreConfig)
			postgres, ok := cfg.Databases.Postgres["primary"]
			if !ok || postgres.MaxOpenConns != 25 || postgres.MaxIdleConns != 5 ||
				postgres.ConnMaxLifetime != 30*time.Minute {
				t.Errorf("postgres = %+v, want primary with 25/5/30m", cfg.Databases.Postgres)
			}
			cache, ok := cfg.Redis["cache"]
			if !ok || cache.DB != 2 || cache.PoolSize != 20 || cache.MinIdleConns != 4 ||
				cache.ConnMaxLifetime != time.Hour {
				t.Errorf("redis = %+v, want cache with db 2 and 20/4/1h", cfg.Redis)
			}
		})

		t.Run("console-only logger", func(t *testing.T) {
			if cfg := mustLoad(t, validConfig+"  console: true\n  disable_file: true\n"); !cfg.Logger.DisableFile {
				t.Error("logger.disable_file = false, want true")
			}
		})

		t.Run("environment overrides the file", func(t *testing.T) {
			t.Setenv("APP_NAME", "from-env")
			if cfg := mustLoad(t, validConfig); cfg.App.Name != "from-env" {
				t.Errorf("app.name = %q, want the environment to win", cfg.App.Name)
			}
		})

		t.Run("quickstart examples", func(t *testing.T) {
			exampleConfig := repoFile(t, filepath.Join(configPath, configName+".example."+configType))
			exampleEnv := repoFile(t, dotEnvPath+".example")
			vars, err := godotenv.Unmarshal(exampleEnv)
			if err != nil {
				t.Fatalf("parse .env.example: %v", err)
			}
			t.Cleanup(func() {
				for key := range vars {
					_ = os.Unsetenv(key)
				}
			})

			chdirTemp(t)
			writeFile(t, configFile(), exampleConfig)
			writeFile(t, dotEnvPath, exampleEnv)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(cfg.Databases.Postgres) != 1 || len(cfg.Redis) != 1 || len(cfg.Databases.SQLServer) != 0 {
				t.Errorf("postgres = %v, redis = %v, sql_server = %v; want one postgres, one redis, no sql server",
					cfg.Databases.Postgres, cfg.Redis, cfg.Databases.SQLServer)
			}
			if got := cfg.Databases.Postgres["example"].User; got != "postgres" {
				t.Errorf("postgres user = %q, want %q from .env.example", got, "postgres")
			}
		})
	})

	t.Run("negative: unreadable sources", func(t *testing.T) {
		tests := []struct {
			name  string
			files map[string]string
			want  string
		}{
			{"unparsable .env", map[string]string{dotEnvPath: "neither an assignment nor a comment\n"}, dotEnvPath},
			{"no config file", nil, "config.example"},
			{"unparsable yaml", map[string]string{configFile(): "app: [2, 4\n"}, "read config file"},
			{"value the struct cannot hold", map[string]string{configFile(): "server:\n  timeout: banana\n"},
				"unmarshal config"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				chdirTemp(t)
				for name, contents := range test.files {
					writeFile(t, name, contents)
				}
				if cfg, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
					t.Errorf("Load = (%+v, %v), want an error mentioning %q", cfg, err, test.want)
				}
			})
		}
	})

	t.Run("negative: fails validation", func(t *testing.T) {
		tests := map[string]struct{ contents, want string }{
			"missing required fields": {"app:\n  name: template\n", "invalid config"},
			"negative shutdown timeout": {strings.Replace(validConfig,
				"  timeout: 40s\n", "  timeout: 40s\n  shutdown_timeout: -1s\n", 1), "ShutdownTimeout"},
			"file disabled with no console left": {validConfig + "  disable_file: true\n", "Console"},
			"service base_url is not a url": {
				validConfig + service("api.example.com", "inquiry: /v1/inquiry"), "BaseURL"},
			"service has no endpoints": {validConfig + service("https://api.example.com", "{}"), "Endpoints"},
			"service endpoint has an empty path": {
				validConfig + service("https://api.example.com", `inquiry: ""`), "Endpoints[inquiry]"},
			// pkg/logger accepts any case, the validator only upper case.
			"lower-case logger level": {strings.Replace(validConfig, "level: INFO", "level: info", 1), "Level"},
		}
		for name, test := range tests {
			t.Run(name, func(t *testing.T) {
				if cfg, err := loadFrom(t, test.contents); err == nil || !strings.Contains(err.Error(), test.want) {
					t.Errorf("Load = (%+v, %v), want an error mentioning %q", cfg, err, test.want)
				}
			})
		}
	})

	// AutomaticEnv only overrides keys viper already knows from the file.
	t.Run("negative: an env var for a key missing from the file is ignored", func(t *testing.T) {
		t.Setenv("SERVER_MAX_BODY_BYTES", "10")
		if cfg := mustLoad(t, validConfig); cfg.Server.MaxBodyBytes != 0 {
			t.Errorf("server.max_body_bytes = %d, want the env var ignored", cfg.Server.MaxBodyBytes)
		}
	})
}
