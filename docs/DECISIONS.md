# Architecture Decision Records

Short ADRs for the choices that weren't fully dictated by the spec, or that
trade something off worth writing down. Each one: context, decision,
consequences.

## ADR 1 — Even/odd on the dice sum at 1.92× for a 0.96 RTP

**Context.** The spec fixes the target RTP at 0.96 but doesn't fix the bet
type or the payout multiplier. Two dice, even/odd on the sum, is the
simplest bet that reads naturally as "a dice game" and is genuinely 50/50
(18 of 36 ordered outcomes are even, 18 are odd) — no house edge hides in
the dice themselves.

**Decision.** Bet on even/odd; pay `1.92×` the stake on a win (derived as
`RTP = P(win) × multiplier → 0.96 = 0.5 × multiplier → multiplier = 1.92`).
Applied as exact integer math, `stake * 192 / 100`, truncating toward zero.

**Consequences.** The house edge (4%) is flat and stake-independent, easy to
audit and to explain. Integer truncation costs the player a fraction of a
cent on stakes that don't divide evenly by 100/gcd, nudging the realized RTP
infinitesimally below 0.96 — documented in `docs/GAME_RULES.md`, verified by
a property test over many simulated rounds
(`internal/domain/game/payout_test.go`).

## ADR 2 — Money as `int64` cents everywhere

**Context.** Floating-point currency arithmetic is a classic source of
off-by-a-fraction-of-a-cent bugs and non-reproducible results across
languages/platforms.

**Decision.** Every monetary value — REST bodies, WS payloads, PostgreSQL
columns (`BIGINT`), Go fields (`int64`) — is an integer count of cents. No
`float64` monetary field exists anywhere in the codebase.

**Consequences.** All arithmetic is exact and reproducible; JSON encodes
amounts as plain integers, never strings or decimals, so clients don't need
a bignum/decimal library either. The cost is that every amount needs a "×100
for display" step at the very edge of a real UI — out of scope here, since
v1 has no frontend.

## ADR 3 — Owner-only room/round authority

**Context.** Six players share a table; someone has to decide when a round
starts and when the table closes early. The spec says "starting a room or a
round is done via WebSocket" and "closing a room is done via a REST
endpoint," but not who is allowed to.

**Decision.** The player who creates a room (`POST /rooms`) becomes its
owner. Only the owner may start a round (`room.start`/`round.start`) or
request a close (`DELETE /rooms/{id}`); the domain layer enforces this
directly (`Room.StartRound`/`Room.Close` check `IsOwner`), so it can't be
bypassed by calling a use case out of order.

**Consequences.** Simple, unambiguous authority model — no voting, no
"first to click" races for round control. The trade-off: if the owner
disconnects and never comes back, the table is stuck (no round can start)
until *someone* closes it — but nobody but the owner can. v1 accepts this;
a production system would probably want an idle-room reaper or
owner-transfer. Not implemented here; flagged as a known limitation.

## ADR 4 — Abort + refund on shutdown, never settle early

**Context.** A process can stop (deploy, crash, SIGTERM) while a room's
betting window is open. Two options: settle the round with whatever bets
happened to be in by then, or refund every open stake and close the room
without rolling dice.

**Decision.** Refund. `Engine.Shutdown` aborts every room with a live
betting window via `AbortRoomUseCase`: each unsettled bet gets a `payout`
ledger entry equal to its own stake (a wash), the round row is left in
`status='betting'` (no dice, no outcome — the schema's own check constraint
forbids recording a settlement without both), and the room is force-closed
with `reason: server_shutdown`.

**Consequences.** Nobody is unfairly resolved against a window other players
never got the chance to finish betting into — the outcome is neutral (stake
back, no win, no loss) rather than "whoever the process caught mid-flight."
The cost is that a round in progress at shutdown time never completes; a
player watching the countdown sees the table simply close. Considered and
rejected: settling with a "phantom roll" — rejected because it resolves bets
against a window some players may not have finished betting into, which is
not neutral.

## ADR 5 — Blunt startup recovery: close everything, refund, don't resume

**Context.** A room's *timing* lives in an in-memory goroutine (the room's
Engine loop) that dies with the process; its *state* lives in PostgreSQL and
survives. A room a previous process left `open`/`closing` at boot has nobody
to settle its round and its members are still bound to it in Redis (which
would block their withdrawals forever if left alone).

**Decision.** At every boot, before serving traffic: list every
non-`closed` room (`RoomRecoveryRepository.ListActiveRoomIDs`) and abort
each one — same code path as ADR 4 (`AbortRoomUseCase`, `reason:
recovered_after_restart`). Nothing is resumed; no timer is re-armed.

Storage shape of an aborted round, precisely: the `rounds` row for the open
round stays `status = 'betting'` forever (a closed room can perfectly well
contain a round permanently stuck in `betting` — this is the marker of "this
round was aborted, not settled"), its `bets` rows keep `won = NULL, payout =
NULL`, and the money is made whole entirely through the `transactions`
ledger — a `payout` entry equal to each bet's stake. `deposits + payouts -
withdrawals - bets = balance` still holds exactly; a database consistency
check for that invariant does not need a special case for aborted rounds.

**Consequences.** Correct and simple, at the cost of being pessimistic:
"resume what's resumable" (rooms whose window hasn't actually expired in
wall-clock terms yet) would give players a smoother experience across a
rolling deploy, but requires persisting the engine's *intent* (which room
has a loop, and what it's waiting for) somewhere that survives the process —
a meaningfully bigger design than a technical test warrants. Recorded here
as the explicit next step for a production version.

## ADR 6 — Balance pushed only at round end / room end

**Context.** The spec is explicit: "Placing a bet ... does not return the
balance, only the bet result" and "the balance is only updated upon a
'round end' or 'room end' call."

**Decision.** `bet.accepted` (the direct response to `bet.place`) carries
`bet_id, room_id, round_id, round_number, choice, amount` — no balance
field exists in that payload's type at all (`BetAcceptedPayload`). The
*only* event that carries a balance is `balance.updated`, pushed once per
affected player right after `round.result` (reason `round_end`) or right
after `room.closed` (reason `room_end`). `GET /wallet/balance` remains
available as an explicit pull, any time.

**Consequences.** A client cannot infer its balance from the bet
acknowledgement even if it wanted to — it has to either wait for the push or
poll REST. This is intentional: it keeps the WS channel's per-bet payload
minimal and makes "when does the client learn its balance" a single,
auditable rule instead of "sometimes it's on the bet response too."

## ADR 7 — Audit log is append-only and bypasses the request transaction

**Context.** Every REST call and every WS event needs an audit row,
including *failed* ones — a rejected withdrawal or a duplicate bet is
exactly the kind of event an audit trail exists to capture. If the audit
write happened inside the same transaction as the business operation, a
rolled-back operation would also roll back its own audit trail, which
defeats the purpose.

**Decision.** Two things, layered:

1. The `audit_logs` table itself: PostgreSQL triggers (`BEFORE UPDATE`,
   `BEFORE DELETE`, `BEFORE TRUNCATE`) raise on any mutation attempt,
   enforced at the database level regardless of which application code (or
   which future bug) tries it.
2. The audit write is **not** nested inside the request/event's own
   transaction. `AuditMiddleware` (REST) and `writeAudit` (WS) run their
   `INSERT INTO audit_logs` on a context detached from the caller's
   cancellation, after the handler/session has already returned — so a
   handler that returned an error (and rolled its own transaction back)
   still gets audited, and a client that disconnected mid-request doesn't
   erase the record of what it attempted.

**Consequences.** The audit trail is genuinely append-only and genuinely
complete — including failures, including disconnects. The cost: audit
writes are technically decoupled from the business transaction, so in the
vanishingly rare case where the audit `INSERT` itself fails (a full disk),
the business operation still succeeds — logged and swallowed, not fatal
(`logger.Error(...)`, request unaffected). This is the correct trade-off: an
audit outage should not be able to take the betting engine down.

## ADR 8 — chi + pgx/sqlc + gorilla/websocket stack

**Context.** Go has several viable choices for each layer; the spec doesn't
mandate any of them beyond "Go, PostgreSQL, Redis."

**Decision.**
- **chi** for HTTP routing/middleware — small, stdlib-`net/http`-compatible,
  no framework magic, good middleware composition for the audit/auth stack.
- **pgx/v5 + sqlc** for PostgreSQL — pgx for a fast native-protocol driver
  with proper `pgtype` support (UUID, `timestamptz`, `numeric`-free BIGINT
  money), sqlc to generate typed Go from hand-written SQL (`db/queries/*.sql`)
  instead of hand-rolling scans or reaching for a full ORM neither the spec
  nor a betting engine's row-lock-heavy transactions particularly want.
- **golang-migrate**, migrations embedded via `db/embed.go` so the compiled
  binary carries its own schema — no separate migrate step required to run
  the container.
- **go-redis/v9** for Redis — the standard client, nothing exotic needed for
  `SET NX` + hash operations.
- **gorilla/websocket** for the WS transport — the de facto standard,
  predates `nhooyr.io/websocket` in most Go codebases' muscle memory, and its
  read/write-pump-plus-channel pattern is exactly what a "one writer, N
  concurrent event sources" connection needs.
- **golang-jwt/jwt/v5** + `golang.org/x/crypto/bcrypt` for auth.

**Consequences.** All boring, well-understood, single-purpose libraries; no
framework owns the process's control flow. The main cost of sqlc over an ORM
is that every new query is a hand-written `.sql` file plus a `sqlc generate`
step — acceptable for a project this size, and it keeps every query
reviewable as plain SQL.

## ADR 9 — No `room.leave` event in v1

**Context.** The spec's WS event list doesn't include a leave event, and
explicitly frames a room's life as bounded by *closing*, not by players
coming and going: "A room is only closed when the current round ends."

**Decision.** There is no `room.leave` client event and no server-initiated
"remove a player from a room" path short of the room closing entirely.
Disconnecting a socket does not unseat a player — `Hub.unregister` clears
the socket's connection-level bookkeeping (who to push events to) but never
touches `room_players` or the player's session binding. A disconnected
player's seat, and their claim on that room (blocking a second room), both
persist until the room closes.

**Consequences.** Matches the spec's model exactly: a table is a fixed set
of up to 6 seats for its whole life. A disconnected player can reconnect
(new socket, same JWT) and `room.join` the same room again — that's the
idempotent re-attach path in `JoinRoomUseCase`, not a new seat. The
trade-off: a player who disconnects and never returns occupies a seat (and
blocks their own ability to join/create a different room) for the rest of
that table's life — there's no timeout-based eviction. Acceptable for v1;
flagged as a natural v2 addition (an idle-seat timeout) alongside owner
transfer (ADR 3).

## ADR 10 — Redis cache-aside, PostgreSQL as source of truth

**Context.** "Data for open rooms is stored in Redis/PostgreSQL to make the
system faster. Data for active players is also stored there" (requirements)
— the spec asks for both stores but doesn't specify which one is
authoritative when they disagree.

**Decision.** PostgreSQL is unconditionally the source of truth. Redis is
populated cache-aside (write-through on the operations that change room
state; read-through on `GET /rooms`) with bounded TTLs, and every
Redis-backed *decision that matters for correctness* — one room per player,
the 6-seat cap, withdraw-while-in-room — is re-verified against PostgreSQL
inside the same transaction that commits the change. A `GET /rooms` cache
hit is trusted for *listing* (a slightly stale "is this room still open"
view is an acceptable UX cost); nothing safety-critical is ever decided from
a cache read alone.

**Consequences.** Redis can be flushed, restarted, or entirely unreachable
without corrupting state — every guard it provides has a PostgreSQL fallback
behind it (see `docs/ARCHITECTURE.md`'s race-condition table). The
`response.source` field on `GET /rooms` (`"cache"` vs `"database"`) exists
specifically so this behavior is observable rather than a black box during
review/testing.
