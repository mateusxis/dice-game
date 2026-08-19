# Game Rules

The business rules implemented by this service, as a single reference. Every
rule here is enforced in code — see the file pointer at the end of each
section if you want the authoritative source.

## The bet

A round rolls two fair six-sided dice with `crypto/rand`
(`internal/infrastructure/dice/dice.go`) and sums them (2..12). A player bets
on the sum being **even** or **odd** before the roll, during a 15-second
betting window.

Two dice produce 36 equally likely ordered outcomes (6×6); exactly 18 sum to
even and 18 to odd. So even/odd is a genuine 50/50 proposition — there is no
"house zero" hiding in the dice themselves, unlike (say) roulette's green
pocket. The house edge lives entirely in the payout multiplier.

## Payout: 1.92× for a 0.96 RTP

The product spec fixes the target Return To Player (RTP) at **0.96** — over
a large number of bets, a player gets back 96% of what they wagered, on
average.

```
RTP = P(win) * multiplier
0.96 = 0.5 * multiplier
multiplier = 1.92
```

So a winning bet returns **1.92× the stake** (the stake back, plus 0.92× the
stake as profit); a losing bet returns nothing. That's a flat 4% house edge,
independent of stake size or choice.

`payout` in every place it's reported (the `round.result.winners[].payout`
WebSocket field, the `payout` column in `bets`, the `payout` ledger entry in
`transactions`) is always the **gross** return — it already includes the
stake, which was debited separately when the bet was placed. Net profit on a
win is `payout - amount`.

### Integer math and truncation

Money is `BIGINT`/`int64` **cents** everywhere; floating point is never used
for anything monetary. The multiplier is applied as:

```go
payout = stake * 192 / 100
```

evaluated left to right: the multiplication happens at full integer
precision, and a single division truncates toward zero at the very end.
Truncation means a fraction of a cent is kept by the house on odd stakes
(e.g. a 3-cent stake computes to `3*192/100 = 576/100 = 5` cents, not 5.76),
which nudges the *realized* RTP infinitesimally below the theoretical 0.96
for stakes that don't divide evenly. This is the conventional, auditable
rounding direction for a betting system: it can never mint money the house
didn't take in, and the direction is always the same regardless of who wins.

Overflow is a non-issue: `stake * 192` stays within `int64` for any stake
below ~4.8 × 10¹⁶ cents (about 480 trillion currency units) — far beyond any
balance this system could realistically hold.

Source: `internal/domain/game/payout.go` (`Payout(stake int64) int64`).

## Money as cents

Every monetary value in this API — request bodies, response bodies,
WebSocket payloads, database columns — is an integer number of cents. `1000`
means 10.00 in whatever currency unit the deployment uses. There is
deliberately no currency code anywhere in the system; it's a technical test,
and a single implicit unit keeps every amount an exact integer with no
rounding ambiguity at the API boundary.

## Bet lifecycle and money flow

1. **Deposit required first.** A brand-new account has a zero balance
   (`POST /auth/register` never seeds funds); `POST /wallet/deposit` credits
   it. Deposits are allowed at any time, including mid-room.
2. **A stake is debited immediately on `bet.place`**, before the round is
   decided. The bet is rejected outright (`insufficient_balance`) if the
   stake exceeds the current balance — the debit and the bet-row insert
   share one transaction, so a rejected bet never touches the balance.
3. **Winnings are credited only at settlement** (round end) — never at bet
   placement, and never speculatively. A losing bet's stake is simply gone;
   nothing further happens to it.
4. **The balance only ever reaches the client at round end or room end**,
   via the WebSocket `balance.updated` event (or by explicitly polling
   `GET /wallet/balance`). `bet.accepted` — the direct acknowledgement of
   `bet.place` — carries no balance at all, by explicit product requirement.
5. Every debit and credit is mirrored by an immutable ledger row in
   `transactions` (`type` ∈ `deposit | withdraw | bet | payout`). The
   invariant `sum(deposit) + sum(payout) - sum(withdraw) - sum(bet) =
   players.balance` holds for every player, always — including after an
   aborted round (see `docs/DECISIONS.md` ADR 4/5), where a refund is a
   `payout` ledger entry for exactly the stake.

## One bet per round per player

Enforced twice: in memory, by the `Round` aggregate's `PlaceBet` (rejects a
second bet from the same player id before touching storage), and — the
defense that actually matters under concurrency — by a PostgreSQL `UNIQUE
(round_id, player_id)` constraint on `bets`. Two near-simultaneous
`bet.place` frames from the same player race to the same insert; the loser
gets `duplicate_bet`.

Source: `internal/domain/game/round.go` (`PlaceBet`), `db/migrations/000001_init.up.sql` (`bets_round_player_key`).

## Room lifecycle

- **Up to 6 players** per room (`MaxPlayers`), enforced by a Redis
  short-lived mutex around the join (so two simultaneous joins can't both
  observe "5 seats taken" and both proceed), the aggregate's own `Full()`
  check on a row-locked room, and finally the `room_players` primary key as
  the last line of defense.
- **One room per player at a time.** A Redis `SET NX` binding
  (`active_player:{id}` → room id) settles the fast race, followed by an
  authoritative `FindActiveRoomForPlayer` check inside the same transaction
  that seats the player. Joining a room you are *already* in is idempotent
  (not an error) — this lets the owner both create a room over REST and
  attach to it over WebSocket without a special case.
- **Up to 10 rounds** per room (`MaxRounds`). The 10th round's settlement
  closes the room automatically (`reason: max_rounds_reached`).
- **Each round has a 15-second betting window** (`BETTING_WINDOW`,
  configurable — shortened in integration tests). The window is
  half-open `[opens_at, closes_at)`: a bet landing exactly at `closes_at` is
  rejected. `opens_at`/`closes_at` are computed entirely from the backend
  clock; client timestamps are never consulted for anything.
- **A room closes only when its current round ends** — never mid-round. An
  owner's `DELETE /rooms/{id}` with a live round moves the room to
  `"closing"` (not `"closed"`); the pending round is allowed to finish and
  settle normally, and *that* settlement is what actually closes the room.
  With no live round, a close request takes effect immediately.
- **Only the owner** (the player who created the room) may start a round or
  close the room. Every other member gets `not_owner`.

Source: `internal/domain/game/room.go`, `internal/application/game/{join_room,start_round,close_room}.go`.

## Timing authority

"The round timer is generated by the backend; the backend's time zone and
time are the authoritative sources" (requirements.md). Concretely: every
`opens_at`/`closes_at` the client ever sees was computed from
`ports.Clock.Now()` on the server, in UTC, and the settlement trigger is a
Go `time.Timer` armed for that same deadline inside the room's own
goroutine (`internal/application/game/engine.go`). A client's own clock is
never read, trusted, or used to decide whether betting is still open —
`Round.BettingOpen`/`PlaceBet` re-check the backend clock on every single bet.

## Recovery, refunds and aborts

If the process stops (SIGTERM, crash) while a room has a live betting
window, that window is **aborted, not settled**: every stake placed into it
is refunded via a `payout` ledger entry for the exact stake, and the room is
force-closed (`reason: server_shutdown`, or `recovered_after_restart` if a
*different* process discovers the stale room at its own startup). No dice
are rolled for an aborted round. This is a deliberate fairness call — settling
a window that other players might still have been about to bet into would
resolve it unfairly; refunding is the neutral outcome. Full rationale in
`docs/DECISIONS.md` (ADR 4, ADR 5).
