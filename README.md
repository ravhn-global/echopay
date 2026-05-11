# echoApp Backend

Payments orchestrator for Echo — a walletless ultrasonic payment app for Nigerian markets.

## Stack

- **Go 1.26** + Echo (HTTP)
- **Postgres** via pgx (state, ledger, audit)
- **Redis** (session tokens with TTL, rate limits)
- **sqlc** (compile-time-checked SQL)
- **golang-migrate** (schema migrations)
- **Paystack** (Direct Debit + Transfers)

## Layout

```
cmd/server/         Entry point
internal/
  config/           Env loading
  logger/           slog setup
  db/               pgx pool
  cache/            Redis client
  http/             Echo server, middleware, handlers
  store/            sqlc-generated query code (created by `make sqlc`)
migrations/         golang-migrate SQL files
sqlc/               sqlc input (sqlc.yaml + queries/*.sql)
```

## Setup

1. **Install Go 1.26+**, Postgres, and Redis locally.
2. Copy `.env.example` to `.env` and fill in connection strings.
3. Install dev tools (one-time):
   ```
   go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
   go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
   ```
4. Pull deps and run:
   ```
   go mod tidy
   make run
   ```
5. Verify: `curl http://localhost:8080/health`

## Common tasks

| Command | Purpose |
|---|---|
| `make run` | Run the server with live env |
| `make build` | Compile to `bin/server` |
| `make test` | Run tests with race detector |
| `make lint` | `go vet ./...` |
| `make sqlc` | Regenerate `internal/store` from `sqlc/queries/*.sql` |
| `make migrate-create name=add_users` | Scaffold a new migration pair |
| `make migrate-up` | Apply pending migrations |
| `make migrate-down` | Roll back one migration |

## Status

**Foundation pass.** Server boots, connects to Postgres + Redis, exposes `/health`. No business logic yet — modules (tokens, payments, ledger, etc.) added in subsequent passes.
