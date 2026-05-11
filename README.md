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
  http/             Echo server, JWT middleware, /me, /health
  store/            sqlc-generated query code (created by `make sqlc`)
  pgconv/           pgtype ↔ uuid/time helpers
  money/            Kobo integer type (never floats)
  paystack/         Thin Paystack REST client
  auth/             Phone OTP + HS256 JWT
  kyc/              BVN tier 1 stub
  mandates/         Paystack Direct Debit (authorization codes)
  tokens/           60s session tokens for the audio/QR channel
  ledger/           Append-only double-entry record
  payments/         Orchestration: claim → charge → transfer → ledger
                    plus refund flow and webhook event handling
  jobs/             Periodic reconciler for stuck payments
migrations/         golang-migrate SQL files (000001..000007)
sqlc/               sqlc input (sqlc.yaml + queries/*.sql)
```

## API surface (v1)

Public:
- `POST /v1/auth/otp`           Request OTP for phone
- `POST /v1/auth/verify`        Verify OTP, returns `{token, user, is_new}`

Authenticated (`Authorization: Bearer <jwt>`):
- `GET  /v1/me`                       Current user
- `PUT  /v1/me/receive-account`       Set NUBAN to receive into (Paystack-resolved)
- `PUT  /v1/me/limits`                Update sending limits
- `POST /v1/kyc/bvn`                  Submit BVN + DOB
- `POST /v1/mandates/init`            Start Paystack DD authorization
- `POST /v1/mandates/verify`          Confirm authorization → saves mandate
- `GET  /v1/mandates`                 List active mandates
- `POST /v1/mandates/:id/default`     Set default mandate
- `DELETE /v1/mandates/:id`           Revoke mandate
- `POST /v1/tokens/issue`             Receiver issues a 60s session token
- `GET  /v1/tokens/:code/resolve`     Sender resolves a token for confirmation
- `POST /v1/payments`                 Sender confirms — orchestrates the full flow
- `GET  /v1/payments/:id`             Payment status
- `POST /v1/payments/:id/refund`      Receiver-issued refund (linked NIP)
- `GET  /v1/activity`                 User payment history

Webhooks (public, HMAC-SHA512 signature verified):
- `POST /webhooks/paystack`           charge.* and transfer.* events

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

**Pay, receive, refund, webhook, reconciler all wired.** Full sender →
Paystack debit → Paystack transfer → ledger pair flow runs synchronously
inside `POST /v1/payments`. A 5-minute reconciler goroutine polls Paystack
for any payment stuck in `debiting`/`transferring`/`settling` longer than
10 minutes and flips it to terminal state. The `/webhooks/paystack`
endpoint handles `charge.*` and `transfer.*` events idempotently — every
branch tolerates the sync path having already reached the same state.

Not yet built:
- 24h cooldown for raising sending limits (lowering is instant)
- Trusted-merchant caps
- Force-update / device binding / suspicious-login alerts
- Real KYC provider (BVN stub validates format only)
- Auto-refund automation (reconciler currently only marks failed; the
  retry-as-refund logic for receivers' bank rejections lands next)
- Flutter app
