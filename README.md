# go-template

A Go service template: config-driven dependencies, an OpenAPI-first HTTP layer, structured logging, OpenTelemetry tracing, and graceful shutdown. Clone it, delete the example module, and start writing your own.

## Quickstart

```bash
cp configs/config.example.yaml configs/config.yaml
cp .env.example .env
docker compose up -d --wait   # PostgreSQL + Redis on the ports config.yaml expects
make run
```

The defaults in `config.example.yaml` match `compose.yaml`, so nothing needs editing to get a running server. To ship its traces, logs and metrics to a stack such as Grafana, see [Observability](#observability). `--wait` matters on a fresh clone: the app pings every dependency at startup, and Postgres does not accept connections until it has run the schema below.

```bash
curl localhost:8080/health
# {"condition":"Healthy"}

curl localhost:8080/ready
# {"condition":"Ready"}                   <- every database and Redis answered a ping; 503 otherwise

curl localhost:8080/example/visits
# {"visits":1}                            <- Redis

curl -X POST localhost:8080/example/notes -H 'Content-Type: application/json' \
     -d '{"title":"First note","body":"Written from the example module."}'
# {"id":1,"title":"First note","body":"...","created_at":"..."}   <- PostgreSQL

curl 'localhost:8080/example/notes?limit=5'

curl -s localhost:9464/metrics | grep '^http_server_request_duration_seconds_count'
# ...{http_request_method="GET",http_response_status_code="200",http_route="/health"} 1   <- its own port
```

Run `make help` for every target.

## The example module

`modules/example` exists to show the wiring, over both dependencies at once. Delete it — with `api/server/specs/example.yaml`, its generated package, and `scripts/schema.sql` — once you have your own.

| Route | Shows |
| --- | --- |
| `GET /example/visits` | Reaching a client (Redis) from the service layer |
| `GET`/`POST /example/notes` | Input validation, the full path down to PostgreSQL, a transaction, and logging with the request ID |

It is split into four files, the shape to follow for a module that has dependencies. Layers only ever point downwards:

| File | Holds | Knows about |
| --- | --- | --- |
| `constant.go` | Keys and limits | nothing |
| `handler.go` | Generated request in, generated response out | the service and the logger |
| `service.go` | Validation, defaults, the actual behaviour | the repository and any clients |
| `repository.go` | The stored rows and their queries | `*gorm.DB` |

The handler does no validation and holds no client, so the rules are testable without HTTP; the service returns a `ValidationError` for a caller mistake and a wrapped error for anything else, and the handler turns the first into the spec's `400`, passing the message through `middleware.Reject` so the access log records it as `reason`, and lets the second become a `500`. Nothing above `repository.go` sees a `*gorm.DB`.

Take only the layers you need. `modules/health` has no rules and no table of its own, so it is a single `handler.go` — add `service.go` when there is a rule to enforce, `repository.go` when there is a table.

`Repository.Create` writes the note and its audit row through `database.WithTransaction`, so a note can never exist without its event. Reach for that helper whenever two writes have to land together; a single write does not need one, because GORM already wraps it.

A module mounts only when **every** dependency it needs is configured. Remove `redis:` from `config.yaml` and all three routes return 404 rather than failing at startup or panicking on the first request; a warning at startup names the module and what it needs. See `register` in `internal/api/server/router.go`.

The `example_notes` table comes from `scripts/schema.sql`, which compose mounts into the Postgres entrypoint — it runs once, when the data volume is first created. After editing it, replay with `docker compose down -v && docker compose up -d`. Reach for a migration tool (goose, atlas) once you have a schema that has to change in place.

## Adding a module

1. Write `api/server/specs/<name>.yaml`.
2. `make oapicodegen` — generates `internal/api/server/oapicodegen/<name>/`.
3. Implement `StrictServerInterface` in `internal/api/server/modules/<name>/`, following the four files above.
4. Register it in `register()` in `internal/api/server/router.go`, wiring `NewRepository` → `NewService` → `NewHandler`, through `RegisterHandlersWithOptions` and `NewStrictHandlerWithOptions` with `invalidParam`, `badRequest` and `internalError` as the existing modules do: the generated defaults write internal error messages into the response, keyed `msg` instead of the spec's `message`.
5. If it needs a table, add it to `scripts/schema.sql` and replay with `docker compose down -v && docker compose up -d`.

One spec per module: every route in `<name>.yaml` is registered in a single call, so a module is mounted whole or not at all.

CI regenerates the contracts and fails if they differ from what is committed. That includes a Dependabot bump of oapi-codegen whose output changes: run `make oapicodegen` on that PR's branch and commit the result.

The generated server does not validate request bodies against the schema. Validate in the service and return a `ValidationError` — `modules/example/service.go` does this.

## Configuration

`configs/config.yaml` is the source of truth. It and `.env` are both gitignored, which is why the repo ships `.example` copies of each. Values are overridden by `.env` and real environment variables, using the config path uppercased with `_` between segments:

```
databases.postgres.example.password  ->  DATABASES_POSTGRES_EXAMPLE_PASSWORD
```

The key must already exist in `config.yaml` — an environment variable for a key that is not in the file is ignored. Keep secrets out of the YAML and set them this way.

Every entry under `databases:` and `redis:` is opened **and pinged** at startup, so a block you are not running yet must be removed or commented out, not left blank. `services:` only builds HTTP clients; an unreachable `base_url` costs nothing until a handler calls it, though a malformed one is rejected at startup along with a service that declares no endpoints.

`server.timeout` is the deadline a handler and everything it calls gets. The server's write timeout is derived from it, so raising one raises the other; keep every upstream `services.*.timeout` below it, or the request dies before the shorter deadline can fire.

On SIGTERM the server stops accepting and waits `server.shutdown_timeout` (default 8s) for requests in flight — less the share tracing keeps to flush spans, when it is on. `/metrics` keeps answering through the drain and closes after it, so the agent's last scrapes still see it. Keep it under your platform's grace period — 10s for `docker stop`, 30s by default on Kubernetes — or the process is killed mid-drain. A request running longer than it is cut off, so where the grace period allows, set it above `server.timeout`.

## Layout

```
api/server/specs/      OpenAPI specs, one file per module
scripts/               Codegen script and the example schema compose loads
cmd/api/               Entry point: load config, build container, serve, shut down
internal/api/server/   Router, generated contracts, module handlers
internal/config/       Config structs, loading, validation
internal/container/    Opens every configured dependency; Close unwinds in reverse
internal/httpclient/   The app's configured upstream clients
internal/middleware/   Request ID, access log, recovery, timeout, security headers, CORS, body limit, request metrics
internal/pkg/          App-specific helpers, not meant to be lifted out
pkg/                   Reusable, app-agnostic: database, redis, logger, tracing, metrics, httpclient, util
```

`internal/` is app-specific and free to change; `pkg/` is meant to be lifted into other services.

### Dependencies

`container.NewContainer` opens everything in `config.yaml` and registers a shutdown hook per connection — and, with `metrics.enabled`, exports each one's pool — plus one that flushes spans when tracing is on and one that gives the standard library's `log` back (see [Observability](#observability)). `Close` runs them in reverse so the logger outlives anything that might log while closing, and the flush comes after every connection it traced. If one dependency fails to open, the failure is logged as a `start failed` ERROR and the ones already open are closed before the error is returned.

Handlers receive what they need through their constructor — they never see the container.

## Requests

Every request passes through `middleware.Default`: request ID, access log, panic recovery, timeout, security headers, CORS, and a body-size limit. Outside it, in this order, come the tracing span when `tracing.enabled` and `middleware.Metrics` when `metrics.enabled`, so both see the 500 a recovered panic becomes. `ContextWithFallback` is on, so the `context.Context` a handler receives carries the timeout deadline — pass it to every database, cache, and HTTP call and a stalled dependency cannot outlive the request.

That same context carries the request ID. Log with the `*Ctx` methods — `log.InfoCtx(ctx, "note created", fields)` — and the line gets `request_id` without the handler copying it, so it lands next to the access-log line for the same request; `modules/example/handler.go` does this after storing a note. The logger gets the ID from the `middleware.LogFields` hook that `container.newLogger` installs through `logger.Config.ContextFields`; a key you pass in `fields` wins over the hook's. Use the plain `Info`/`Warn` methods only for lines outside a request, such as startup. `middleware.RequestIDFrom(ctx)` still returns the ID when you need it for something other than a log line. With tracing on, the same lines also get `trace_id` and `span_id` — see below.

`/health` is the liveness probe and never touches a dependency; `/ready` pings every database and Redis from `container.Pings` and answers 503 if one is down, logging which. The checks run concurrently, each capped at 2s, so give the probe a `timeoutSeconds` of 3 or more. Point a Kubernetes readiness probe at `/ready` and a liveness probe at `/health`, so an outage takes the pod out of rotation instead of restarting it.

## Tracing

Off by default. Set `tracing.enabled: true` and every request becomes a trace, sent over OTLP/HTTP to `tracing.endpoint` — an OTLP collector, usually the agent on the same host, `http://localhost:4318` by default (see [Observability](#observability)):

| Span | From | Carries |
| --- | --- | --- |
| `GET /example/notes` | `otelgin`, first in the chain | route, status |
| `SELECT example_notes` | `database.NewTracingPlugin` on every GORM connection | the SQL with its `$1` placeholders, never the bound values |
| `incr` | `redisotel` on every Redis client | the command name, not its arguments |
| a client span per call | `otelhttp` on every `services:` client | method and status, and a `traceparent` header so the upstream's spans join the trace |

Every `*Ctx` log line inside a request then carries `trace_id`, `span_id` and `trace_flags` — the names and lowercase-hex encoding the OpenTelemetry spec asks of non-OTLP logs — next to `request_id`, so a log line links to its trace in whatever stack stores both. A request that arrives with a `traceparent` continues the caller's trace instead of starting one.

`tracing.sample_ratio` is the share of new traces kept. Lines of a trace that was not kept still carry its `trace_id`, with `trace_flags` `00`. Keep `1.0` locally; production usually wants `0.1` or less. A caller's decision wins over the ratio, so one trace is never half recorded across services.

Left out on purpose: `/health` and `/ready`, since a probe every few seconds per pod would outnumber real requests, and the Redis `PING` that `/ready` sends. Redis arguments and SQL values stay off the spans because they can hold tokens and personal data.

To add a span of your own, `otel.Tracer("<module>").Start(ctx, "name")` — the container installs its provider globally. Pass the handler's `ctx` down, as for the timeout, or the span starts a trace of its own.

The standard `OTEL_*` variables still apply: `OTEL_EXPORTER_OTLP_ENDPOINT` when `tracing.endpoint` is empty, `OTEL_EXPORTER_OTLP_HEADERS` for a collector that wants an API key, `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` to override what `app:` sets.

A collector that is down never stops the app. The batch it cannot take is dropped and the exporter's failure lands in the JSON log as an `opentelemetry error` warning. On shutdown, the spans still buffered are flushed out of the same `server.shutdown_timeout`: a quarter of it, at most 2s, so the default 8s drains requests for 6s and flushes for 2s, and the whole exit stays under `docker stop`'s 10s. Without tracing the full budget drains. A flush that runs out is logged as `spans not flushed on shutdown` rather than failing the exit.

Cost, from `BenchmarkTracing` on `/example/visits` with Redis answered in-process: off adds nothing; on adds about 5µs of CPU and 43 allocations to a 5µs request at a 10% ratio, and about 10µs at 100%. Most of it is paid whether or not the trace is kept, so a lower ratio saves export work more than request time. Tracing takes no lock of its own: in parallel it scales as well as the untraced request does. Spans are queued and exported in the background, so a slow collector costs dropped spans, never request latency. One instance exports about 130k spans a second; on the benchmark machine that drops spans above roughly 65k requests a second at 100%, and nothing at 10%. The binary grows by about 6 MB.

## Observability

The service produces its signals and stops there: collecting, storing and showing them is a separate stack's job, so the same code works with any OTLP collector, log agent and Prometheus-compatible scraper. The intended shape is an agent on every host that runs services — Grafana Alloy, for one — collecting next to the service and pushing to a central store, so the store never has to reach a service:

| Signal | The service | The host's agent |
| --- | --- | --- |
| Traces | pushes OTLP/HTTP to `tracing.endpoint` (`localhost:4318`) | receives and forwards them |
| Logs | writes JSON lines: to stderr in a container, to files under `logger.path` on a host | reads them — Docker's container output, or the files — and forwards them |
| Metrics | serves Prometheus `/metrics` on `metrics.port` (9464), apart from the API | scrapes it |

Logs are collected, not pushed, so a line survives an agent or stack that is down — Docker or the file holds it until it is read — and in a container, output the logger never wrote, such as a crash on stderr, is caught with the rest. What an agent can rely on in each line:

| Field | Meaning |
| --- | --- |
| `application_name` | the service, equal to the `service.name` the spans carry, so a trace and its lines share a name; the label to stream by |
| `level` | `DEBUG`, `INFO`, `WARN` or `ERROR`; safe as a label |
| `timestamp` | the line's own time, to use instead of the moment it was read. Go layout `2006-01-02T15:04:05.000Z0700`: on a host outside UTC the offset has no colon (`+0700`), so it is not RFC 3339 |
| `trace_id`, `span_id`, `request_id` | unique per request: keep them searchable, never as labels, or each would start a stream of its own |
| `method`, `path`, `status` | on access-log lines, for per-route panels |

Keep those names when changing the logger: whatever collects the logs is configured against them.

Every line the app logs is JSON, including what `pkg/database`, `pkg/redis` and GORM report through the standard library's `log`: `container.NewContainer` redirects it into the logger (`logger.RedirectStdLog`) before anything opens, and `Close` puts it back. A message starting `warning: ` — the cleartext-connection notices, GORM's slow queries and errors — becomes a WARN, the rest INFO. Without it those lines would reach the agent as plain text it cannot attribute to the service, and on a host, where it reads only the files, not at all. The one plain-text line is `main`'s on stderr when the start fails: before the logger exists — an unreadable config — it is the only record; after, the same failure is also in the JSON log as `start failed`.

Locally, any stack with an agent on your machine works: it listens on `localhost:4318`, which `tracing.endpoint` already points at, scrapes `localhost:9464/metrics`, and tails the files under `./logs`, so `logger.disable_file` must stay `false` while the app runs through `make run`. Then set `tracing.enabled: true`. Without one, leave tracing off: the JSON lines on stderr and under `./logs`, and `/metrics`, can be read directly.

Where a dashboard takes its error ratio from spans rather than `http_server_request_duration_seconds`, it counts server spans in status ERROR: a 5xx, or a request whose handler called `c.Error`. A 4xx leaves the span unset, as the HTTP conventions ask, so record why a request was refused with `middleware.Reject` rather than `c.Error`; `invalidParam`, `badRequest` and the example handlers do.

### Metrics

With `metrics.enabled` (on in `config.example.yaml`: a scrape needs nothing running), `GET /metrics` answers on `metrics.port`, a port of its own:

| Series | From | Labels |
| --- | --- | --- |
| `http_server_request_duration_seconds` (histogram), `http_server_active_requests` | `middleware.Metrics`, mounted after tracing and before `middleware.Default` | `http_request_method`, `http_route` (the template, `/notes/:id`; `""` when no route matched), `http_response_status_code` |
| `go_sql_*` — open, idle and in-use connections, waits | every `databases:` pool | `db_name`: `postgres/<name>`, `sql_server/<name>` |
| `go_redis_pool_*` — hits, misses, timeouts, connections | every `redis:` client | `db_name`: `redis/<name>` |
| `go_*`, `process_*` | the Go runtime and the process: goroutines, heap, GC, CPU, memory, file descriptors | — |
| `promhttp_metric_handler_*` | the scrapes themselves | `code` |

The names, labels and buckets are the OpenTelemetry HTTP conventions', so a service in another language lines up on the same dashboard; `db_name` is the key `/ready` reports a check by. Every label is bounded — an unknown method is `_OTHER`, a path never becomes a label — so a scanner cannot multiply the series.

Unlike rates derived from spans (Tempo's metrics generator, for one), these count every request whatever `tracing.sample_ratio` keeps, so use them for request rate, errors and latency. A sampled request's bucket carries its `trace_id` as an exemplar, served when the scraper asks for OpenMetrics, so a point on a latency panel opens its trace. The series carry no service name: the agent adds one as a target label when it scrapes, matching the logs' `application_name`.

The port is for the agent only. It describes the service's insides, so never publish it: `make docker-run` maps only the API's port, and in compose leave it off `ports:` and let the agent reach it on the shared network. Cost, from `BenchmarkMetrics`: about 0.4µs and 5 allocations a request.

## Make targets

| Target | |
| --- | --- |
| `make run` | Run an app (`APP=name`, default: first in `cmd/`) |
| `make test` | Tests with the race detector |
| `make cover` | Coverage report in the browser |
| `make cover-gaps` | List functions that are not fully covered |
| `make check` | fmt, vet, lint, test |
| `make lint-install` | Install the pinned golangci-lint |
| `make vuln` | govulncheck: known vulnerabilities in code the app calls |
| `make oapicodegen` | Regenerate server code from the specs |
| `make build` / `build-all` | Host binary / cross-compile |
| `make docker-build` / `docker-run` | Distroless image |

`make help` lists every target, including `vet`, `lint`, `fmt`, `tidy` and `clean`.

`docker-run` starts a container, so `localhost` in `config.yaml` points at that container, not your machine. Compose publishes Postgres and Redis on `127.0.0.1` only, so join the compose network and point the hosts at the service names (`docker run --network go-template_default …`, compose naming it after the folder; hosts `postgres` and `redis`); `host.docker.internal` reaches them on Docker Desktop but not on Linux.

In a container, set `logger.console: true` and `logger.disable_file: true`: logs go to stderr for the host's agent to collect, and nothing is written inside the image. Point `tracing.endpoint` at the agent too: `localhost` is the container itself, so use the agent's name on a Docker network they share (`TRACING_ENDPOINT=http://<agent>:4318`). It is also faster under load: the log file takes a lock and a write per line, which caps one instance at around 130k requests a second however many cores it has (`BenchmarkLogFile`).

## Requirements

Go 1.26.11+, Docker for the local dependencies. `make fmt` needs `goimports`; `make lint` needs golangci-lint, which `make lint-install` pins to the v2 release `.golangci.yml` is written for.

Beyond the golangci-lint defaults the config turns on `errorlint` (a `%v` where `%w` was meant silently breaks `errors.Is`), `bodyclose`, `sqlclosecheck`, `revive`, and `lll` at 120 columns with tabs counted as four. Generated code under `oapicodegen/` is excluded, since `make oapicodegen` overwrites any fix made there. A `//nolint` must name its linter and give a reason.

In production code, exported functions come before unexported ones, so a file opens with what it offers; tests keep their helpers first. This is a convention for review, not a lint rule — leave a helper next to its only caller when splitting them would read worse.

## Coverage

`make cover-gaps` prints the current number and everything short of 100%. Coverage is at
100%, so anything that appears is a genuine gap.

`cmd/api` reaches its failure paths through the package-level seams in `main.go` — `exit`,
`listen`, `newRouter`, `closeDeps` — which tests swap to force an error the real process
cannot be made to produce. `internal/container` uses the same pattern, `pkg/tracing` does
for `newExporter`, which never fails without a dial, and so does `internal/middleware` for
`generateID`, since `crypto/rand` cannot be made to fail.

`make cover` and `make cover-gaps` exclude generated code under `oapicodegen/` and pass
`-coverpkg`, so a module reached through the router is credited rather than reported as
untested. Without that flag, coverage is counted per package and anything exercised only
through another package's tests reads as 0%.

## License

MIT — see [LICENSE](LICENSE).
