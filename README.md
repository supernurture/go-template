# go-template
A starter template for Go REST APIs: an OpenAPI-first HTTP server backed by GORM, with reusable building blocks for database access, outbound HTTP, and configuration.

> **Status: work in progress.** The shared packages under `pkg/` are usable today. The HTTP server layer (router, middleware, handlers) is being rebuilt — `cmd/api/main.go`, `internal/config`, and `internal/container` are currently stubs. See [Roadmap](#roadmap).

## Table of Contents

- [Tech Stack](#tech-stack)
- [Project Structure](#project-structure)
- [Prerequisites](#prerequisites)
- [Getting Started](#getting-started)
- [Configuration](#configuration)
- [Packages](#packages)
- [Code Generation](#code-generation)
- [Makefile Targets](#makefile-targets)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

## Tech Stack

| Concern | Choice |
|---|---|
| Language | [Go](https://go.dev) |
| ORM / database access | [GORM](https://gorm.io) |
| PostgreSQL driver | [gorm.io/driver/postgres](https://github.com/go-gorm/postgres) (pgx under the hood) |
| OpenAPI code generation | [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) |

Exact versions live in `go.mod` — the single source of truth.

Code generators are declared as [Go tool dependencies](https://go.dev/doc/modules/managing-dependencies#tools) in `go.mod`, so `go tool oapi-codegen` works without a separate install step.

## Project Structure

```
go-template/
├── api/
│   └── server/
│       ├── config.yaml     # oapi-codegen settings (gin-server + models)
│       └── specs/          # OpenAPI specs, one file per feature
│           └── health.yaml
├── cmd/
│   └── api/                # Application entrypoint
├── configs/
│   └── config.example.yaml # Non-secret configuration, committed
├── internal/               # Private application code
│   ├── config/             # Config structs and loading
│   └── container/          # Dependency wiring
├── pkg/                    # Reusable, importable packages
│   ├── database/           # GORM connection, pooling, transactions
│   ├── httpclient/         # JSON-oriented outbound HTTP client
│   └── util/               # Small generic helpers
├── scripts/
│   └── oapicodegen.sh      # Regenerates server code from every spec
├── .env.example            # Secrets and environment selector
└── Makefile
```

The layout follows the [Standard Go Project Layout](https://github.com/golang-standards/project-layout): `cmd/` for entrypoints, `internal/` for code that must not be imported by other modules, `pkg/` for packages that may be.

## Prerequisites

- **Go** — see `go.mod` for the minimum version
- **PostgreSQL**
- `make` and `bash` (the codegen script is a shell script; on Windows, use Git Bash or WSL)

## Getting Started

```bash
git clone https://github.com/nurture/go-template.git
cd go-template

cp .env.example .env                          # then fill in credentials
cp configs/config.example.yaml configs/config.yaml

go mod download
make run                                       # or: go run ./cmd/api
```

## Configuration

Configuration is split in two, by whether a value is a secret.

**`configs/config.yaml`** — everything non-secret: hosts, ports, pool sizes, timeouts, log levels. Safe to commit. Copy `config.example.yaml` as your starting point; it documents every available key and its allowed values.

**`.env`** — secrets and the environment selector only. Never committed.

| Variable | Description | Allowed Values |
|---|---|---|
| `APP_ENV` | Runtime environment | `development`, `sit`, `uat`, `production` |
| `SERVER_MODE` | HTTP server mode | `test`, `debug`, `release` |
| `DATABASES_POSTGRES_EXAMPLE_USERNAME` | PostgreSQL username | — |
| `DATABASES_POSTGRES_EXAMPLE_PASSWORD` | PostgreSQL password | — |

## Packages

**`pkg/database`** — opens a pooled GORM connection with `PostgresInit`, applying the `PoolConfig` limits and pinging the database before returning. It logs a warning when the connection string has no `sslmode=require`/`verify`, so an unencrypted link never passes silently. `WithTransaction` and `WithTransactionResult` run a function inside a transaction, committing on success and rolling back on error.

**`pkg/httpclient`** — a small JSON client over `net/http`, configured with options (`WithBaseURL`, `WithTimeout`, `WithHeader`, `WithTransport`). `GetJSON` and `PostJSON` cover the common cases; `Do` is there when you need the raw response. Responses with status ≥ 400 become errors carrying a truncated body.

**`pkg/util`** — generic helpers. Currently just `Ternary`.

## Code Generation

The OpenAPI spec is the source of truth: define the contract in YAML, generate the Go interface, implement against it.

```bash
make oapicodegen
```

`scripts/oapicodegen.sh` walks every `*.yaml` in `api/server/specs/` and generates into its own package under `internal/api/server/oapicodegen/<name>/`, so specs never clash. Adding a feature means dropping a new spec file in `specs/` and re-running — no script changes needed.

## Makefile Targets

Run `make help` to list these with descriptions.

| Target | Description |
|---|---|
| `make run` | Run an app (`APP=name`, defaults to the first under `cmd/`) |
| `make test` | Run tests with the race detector |
| `make cover` | Run tests and open the coverage report |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run` |
| `make fmt` | Format and fix imports via `goimports` |
| `make check` | `fmt` + `vet` + `lint` + `test` |
| `make tidy` | `go mod tidy` |
| `make build` | Build every app for the host OS |
| `make build-all` | Cross-compile every app for linux/windows/darwin on amd64 + arm64 |
| `make clean` | Remove build artifacts |
| `make oapicodegen` | Regenerate server code from the OpenAPI specs |

## Roadmap

Rebuilding the HTTP layer on top of the existing `pkg/` foundation:

- [ ] `internal/config` — load `configs/config.yaml` layered with `.env`
- [ ] `internal/container` — dependency wiring
- [ ] `internal/api/server` — router, middleware, health handler
- [ ] `cmd/api/main.go` — startup and graceful shutdown

Restoring the server layer means re-adding its dependencies, which `go mod tidy` pruned once nothing imported them:

```bash
go get github.com/gin-gonic/gin go.uber.org/zap github.com/spf13/viper
```

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/your-feature`
3. Commit your changes: `git commit -m "feat: add your feature"`
4. Push the branch: `git push origin feat/your-feature`
5. Open a Pull Request

## License

This project is open source. See the repository for licensing details.