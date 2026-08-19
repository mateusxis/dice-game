# Cassino — Dice Betting Engine

A backend for a two-dice, even/odd betting game, built as a technical test. 
Players register, deposit funds, sit at rooms of up to 6,
and bet even/odd on the sum of two dice across up to 10 timed rounds per
room. A winning bet pays **1.92×** the stake (an exact **0.96 RTP**). Built
in Go with PostgreSQL (source of truth) and Redis (cache + coordination),
following DDD + Clean Architecture.

- **REST API reference**: [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1)
- **WebSocket protocol reference**: [`docs/WS_EVENTS.md`](docs/WS_EVENTS.md)
- **Architecture**: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- **Business rules / payout math**: [`docs/GAME_RULES.md`](docs/GAME_RULES.md)
- **Design decisions (ADRs)**: [`docs/DECISIONS.md`](docs/DECISIONS.md)

## Prerequisites

- Go 1.26+ (module targets `go 1.26.6`)
- Docker + Docker Compose (for the full local stack, or just Postgres/Redis)
- [Bruno](https://www.usebruno.com/) (optional, for the request collection under `bruno/`)

No `make`/gcc dependency for the core workflow — a `Makefile` is included as
a convenience wrapper (`make run`, `make test`, `make docker-up`, ...), but
every target is a thin wrapper over a plain `go` or `docker compose` command,
listed below, that works without it too.

## Quickstart

### Option A — everything in Docker

```sh
cp .env.example .env        # review JWT_SECRET before anything but local dev
docker compose up -d --build
curl http://localhost:8080/health
```

This starts Postgres, Redis and the API together; the API applies its
embedded migrations on boot and listens on `:8080` inside the compose
network (mapped to `${HTTP_PORT:-8080}` on the host).

Tear down with `docker compose down` (keeps the Postgres volume) or
`docker compose down -v` (also deletes it).

### Option B — API on the host, dependencies in Docker

```sh
cp .env.example .env
docker compose up -d postgres redis

export $(grep -v '^#' .env | xargs)   # or: set -a; source .env; set +a
go run ./cmd/api
```

`config.Load()` (`internal/infrastructure/config/config.go`) reads every
setting from the environment with sane local defaults; the only variable
with **no** default — deliberately — is `JWT_SECRET`, so the process refuses
to start with a well-known signing key.

**Port 8080 already taken?** Set `HTTP_PORT=8090` (or any free port) before
running — either `export HTTP_PORT=8090` for a host run, or `HTTP_PORT=8090
docker compose up -d --build` for compose (compose's `${HTTP_PORT:-8080}`
host-port mapping honors it; the container's internal port stays 8080).

Key environment variables (full list + defaults in `.env.example`):

| Variable | Default | Notes |
|---|---|---|
| `HTTP_PORT` | `8080` | REST/WS listen port |
| `DATABASE_URL` | `postgres://cassino:cassino@localhost:5432/cassino?sslmode=disable` | pgx connection string |
| `REDIS_ADDR` | `localhost:6379` | host:port |
| `JWT_SECRET` | *(required, no default)* | signs access tokens — generate with `openssl rand -base64 48` |
| `JWT_TTL` | `24h` | access token lifetime |
| `BETTING_WINDOW` | `15s` | length of each round's betting period — the spec fixes this at 15s; shorten only for faster manual/integration testing |
| `BCRYPT_COST` | `12` | password hashing work factor |
| `RUN_MIGRATIONS` | `true` | apply embedded migrations at boot |

### Health check

```sh
curl http://localhost:8080/health
# {"status":"ok","version":"dev","time":"...","dependencies":{"postgres":"ok","redis":"ok"}}
```

## Running tests

```sh
# Unit tests (domain + application, mocked ports; no external services)
go test ./...

# ... with coverage
go test -coverprofile=coverage.out -covermode=atomic ./...
go tool cover -func=coverage.out | tail -1

# gofmt / vet
gofmt -l .
go vet ./...
```

### Integration tests

The `integration/` suite is behind the `integration` build tag, so it is
excluded from a plain `go test ./...` and requires live Postgres + Redis:

```sh
docker compose up -d postgres redis
go test -tags integration ./...
docker compose down -v
```

It boots the real app in-process (`internal/bootstrap`, the same wiring
`cmd/api/main.go` uses) on an ephemeral port against those live
dependencies, runs a fresh migrated schema, and exercises the full stack —
HTTP, WebSocket, PostgreSQL, Redis, the audit trail — end to end. It uses a
short `BETTING_WINDOW` (2s) so a full round settles in test time. If
Postgres or Redis is unreachable at the configured address, the suite skips
with a clear message (`t.Skip(...)`) rather than failing opaquely.

Covered scenarios (see `integration/game_test.go` and
`integration/audit_test.go` for the full list): full two-player happy path
(register → deposit → create room → both join over WS → start round → both
bet → settlement → `balance.updated` with the exact 1.92× payout math →
ledger rows reconcile); duplicate bet rejected; bet exceeding balance
rejected; a 7th player rejected from a full room; withdrawal blocked while
seated in a room and allowed once it closes; joining a second room rejected;
a graceful close mid-round settles the live round before closing; REST and
WS calls each write an audit row; `UPDATE`/`DELETE` on `audit_logs` is
rejected by the database trigger.

## Bruno collection

The `bruno/` directory is a full Bruno collection: `Auth`, `Wallet`, `Rooms`,
`Health`, and `Realtime` (WebSocket). Open the `bruno/` folder as a
collection in Bruno and select the `local` environment
(`bruno/environments/local.bru` — `host`/`port` default to
`localhost`/`8080`; change `port` to `8090` if you used the workaround
above).

### REST walkthrough

Run, in order, from the `Auth`, `Wallet` and `Rooms` folders:

1. **Auth → 1 - Register Player 1**, **2 - Register Player 2** — creates two
   accounts.
2. **Auth → 3 - Login Player 1**, **4 - Login Player 2** — a post-response
   script on each captures the returned JWT into the `token`/`token2`
   environment variables automatically; every other authenticated request in
   the collection reads its Bearer token from `{{token}}`.
3. **Wallet → Deposit** — run once per player (swap the Bearer Auth tab to
   `{{token2}}` for Player 2), so both have funds to bet with.
4. **Rooms → Create Room** — Player 1 creates a room; its post-response
   script captures the new id into `{{roomId}}`.
5. **Rooms → List Rooms** — see the room you just created, and whether the
   answer came from the cache or the database.

Every request's `docs` tab lists its expected success shape and the error
cases you can trigger on purpose (wrong owner closing a room, withdrawing
while seated, a duplicate registration, etc.).

### WebSocket testing (two-player round)

Gameplay — starting a round and placing bets — only happens over `GET /ws`;
there's no REST equivalent. Two ways to exercise it, covering Bruno's
version-dependent native WS support:

**Path 1 — Bruno's native WS request** (`Realtime → Connect (WS)`): connects
using `{{token}}` via the `?token=` query parameter. If your Bruno version
renders a "send frame" box, send, in order:
```json
{"type":"room.join","data":{"room_id":"{{roomId}}"}}
```
then, once connected as Player 1 (the owner):
```json
{"type":"round.start"}
```
then, from each connected player:
```json
{"type":"bet.place","data":{"choice":"even","amount":1000}}
```
Watch `room.state`, `round.started`, `bet.accepted` arrive as responses, and
`round.result` + `balance.updated` arrive on their own once the 15s betting
window (or whatever `BETTING_WINDOW` is configured to) closes.

**Path 2 — `scripts/ws_smoke.go`** (works regardless of Bruno version, no
Bruno required at all): a small, self-contained Go program that registers N
fresh players, logs them in, deposits funds, creates a room, joins everyone
over WS, starts a round, places a bet for each player, and prints every
event as it arrives:

```sh
go run ./scripts/ws_smoke.go -base http://localhost:8080 -players 2
```

(use `-base http://localhost:8090` if you're running with the port
workaround). Flags: `-players` (2..6), `-deposit`/`-bet` (cents),
`-timeout`. See the file's header comment for the full flag list.

## Project layout

```
cmd/api/main.go             process entry point (thin: config, signals, listen, shutdown)
internal/
  bootstrap/                 composition root — wires every adapter + use case
  domain/                    player, game (room/round/bet/payout), audit — pure Go
  application/                use cases (auth, wallet, game) + the round Engine + ports
  infrastructure/             postgres (sqlc), redis, auth (JWT/bcrypt), dice, clock, config
  interfaces/
    rest/                     chi router, handlers, JWT + audit middleware
    ws/                       gorilla hub, per-connection session, WS audit
db/
  migrations/                 golang-migrate SQL, embedded into the binary
  queries/                    sqlc source SQL
api/openapi.yaml              REST API reference (OpenAPI 3.1)
docs/                         WS_EVENTS.md, ARCHITECTURE.md, GAME_RULES.md, DECISIONS.md
bruno/                        Bruno request collection (REST + WS)
scripts/ws_smoke.go           WS protocol smoke test, works without Bruno
integration/                  black-box integration suite (`-tags integration`)
docker-compose.yml            api + postgres + redis
Dockerfile                    multi-stage build
Makefile                      convenience wrappers over the commands above
.env.example                  copy to .env and adjust
```

## Future updates

- A frontend with animations for rounds and rooms;
- Support for multiple languages in the frontend;
- Support for multiple currencies.

## Notes on this technical test

- Every architectural decision worth writing down is an ADR in
  `docs/DECISIONS.md`, not just prose scattered across code comments.
- The audit trail (`audit_logs`) captures every REST call and every WS
  event, success or failure, with password fields redacted, and is
  database-enforced append-only (`UPDATE`/`DELETE`/`TRUNCATE` all raise).
- The engine and use-case behavior is exactly what Phases 1–3 implemented;
  this phase (docs, the Bruno collection, and the integration suite) makes
  no behavioral changes beyond extracting `cmd/api/main.go`'s wiring into
  `internal/bootstrap` so the integration tests can boot the real app
  in-process.
