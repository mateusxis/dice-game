//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// settleTimeout bounds how long a test waits for a round to settle. It must
// comfortably exceed testBettingWindow (2s) plus whatever the engine/DB need
// to actually run the settlement transaction.
const settleTimeout = 10 * time.Second

// waitForRoomState reads room.state events off c until one reports the given
// player_count, skipping any that don't (a client can receive several —
// one per membership change in the room).
func waitForRoomState(t *testing.T, c *wsClient, playerCount int, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for room.state with player_count=%d", playerCount)
		}
		ev := c.waitFor("room.state", remaining)
		m := ev.asMap(t)
		if count, ok := m["player_count"].(float64); ok && int(count) == playerCount {
			return m
		}
	}
}

func TestHappyPathFullRound(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	const depositAmt = int64(100000)
	const betAmt = int64(1000)
	const expectedPayout = betAmt * 192 / 100 // 1920, see docs/GAME_RULES.md

	p1ID, p1Token := registerAndLogin(t, "happy-p1")
	p2ID, p2Token := registerAndLogin(t, "happy-p2")

	if got := deposit(t, p1Token, depositAmt); got != depositAmt {
		t.Fatalf("p1 deposit: balance=%d want=%d", got, depositAmt)
	}
	if got := deposit(t, p2Token, depositAmt); got != depositAmt {
		t.Fatalf("p2 deposit: balance=%d want=%d", got, depositAmt)
	}

	roomID := createRoom(t, p1Token)

	p1 := dialWS(t, p1Token)
	p1.send(t, "room.join", map[string]string{"room_id": roomID})
	waitForRoomState(t, p1, 1, 5*time.Second)

	p2 := dialWS(t, p2Token)
	p2.send(t, "room.join", map[string]string{"room_id": roomID})
	waitForRoomState(t, p2, 2, 5*time.Second)
	waitForRoomState(t, p1, 2, 5*time.Second)

	p1.send(t, "round.start", nil)
	started1 := p1.waitFor("round.started", 5*time.Second).asMap(t)
	started2 := p2.waitFor("round.started", 5*time.Second).asMap(t)
	if started1["round_number"].(float64) != 1 || started2["round_number"].(float64) != 1 {
		t.Fatalf("expected round_number 1, got p1=%v p2=%v", started1["round_number"], started2["round_number"])
	}

	p1.send(t, "bet.place", map[string]any{"choice": "even", "amount": betAmt})
	accepted1 := p1.waitFor("bet.accepted", 5*time.Second).asMap(t)
	if _, hasBalance := accepted1["balance"]; hasBalance {
		t.Fatalf("bet.accepted must never carry a balance, got %#v", accepted1)
	}
	if accepted1["amount"].(float64) != float64(betAmt) || accepted1["choice"] != "even" {
		t.Fatalf("bet.accepted p1: unexpected payload %#v", accepted1)
	}

	p2.send(t, "bet.place", map[string]any{"choice": "odd", "amount": betAmt})
	accepted2 := p2.waitFor("bet.accepted", 5*time.Second).asMap(t)
	if _, hasBalance := accepted2["balance"]; hasBalance {
		t.Fatalf("bet.accepted must never carry a balance, got %#v", accepted2)
	}

	result1 := p1.waitFor("round.result", settleTimeout).asMap(t)
	result2 := p2.waitFor("round.result", settleTimeout).asMap(t)
	if result1["outcome"] != result2["outcome"] || result1["sum"] != result2["sum"] {
		t.Fatalf("both players must see the same result: p1=%#v p2=%#v", result1, result2)
	}
	outcome := result1["outcome"].(string)
	if outcome != "even" && outcome != "odd" {
		t.Fatalf("unexpected outcome %q", outcome)
	}
	if result1["room_closed"].(bool) {
		t.Fatalf("room should not close after round 1 of 10: %#v", result1)
	}

	winners1, _ := result1["winners"].([]any)
	var winnerPlayerID string
	for _, w := range winners1 {
		wm := w.(map[string]any)
		winnerPlayerID = wm["player_id"].(string)
		if wm["payout"].(float64) != float64(expectedPayout) {
			t.Fatalf("winner payout = %v, want %d (1.92x of %d)", wm["payout"], expectedPayout, betAmt)
		}
	}
	if len(winners1) != 1 {
		t.Fatalf("expected exactly one winner (even/odd is mutually exclusive), got %d: %#v", len(winners1), winners1)
	}

	bal1 := p1.waitFor("balance.updated", 5*time.Second).asMap(t)
	bal2 := p2.waitFor("balance.updated", 5*time.Second).asMap(t)
	if bal1["reason"] != "round_end" || bal2["reason"] != "round_end" {
		t.Fatalf("expected reason=round_end, got p1=%v p2=%v", bal1["reason"], bal2["reason"])
	}

	balances := map[string]float64{
		bal1["player_id"].(string): bal1["balance"].(float64),
		bal2["player_id"].(string): bal2["balance"].(float64),
	}
	wantWinner := float64(depositAmt - betAmt + expectedPayout)
	wantLoser := float64(depositAmt - betAmt)
	loserPlayerID := p1ID
	if winnerPlayerID == p1ID {
		loserPlayerID = p2ID
	}
	if balances[winnerPlayerID] != wantWinner {
		t.Fatalf("winner (%s) balance = %v, want %v", winnerPlayerID, balances[winnerPlayerID], wantWinner)
	}
	if balances[loserPlayerID] != wantLoser {
		t.Fatalf("loser (%s) balance = %v, want %v", loserPlayerID, balances[loserPlayerID], wantLoser)
	}

	// Cross-check against the pull-based REST endpoint too.
	if got := getBalance(t, p1Token); got != int64(balances[p1ID]) {
		t.Fatalf("REST balance for p1 = %d, want %d (from balance.updated)", got, int64(balances[p1ID]))
	}
	if got := getBalance(t, p2Token); got != int64(balances[p2ID]) {
		t.Fatalf("REST balance for p2 = %d, want %d (from balance.updated)", got, int64(balances[p2ID]))
	}

	// Ledger reconciliation: deposit, bet, and (winner only) payout rows.
	assertLedger(t, p1ID, depositAmt, betAmt, p1ID == winnerPlayerID, expectedPayout)
	assertLedger(t, p2ID, depositAmt, betAmt, p2ID == winnerPlayerID, expectedPayout)
}

// assertLedger checks that a player's transactions rows reconcile: a deposit
// of depositAmt, a bet of betAmt, and — only for the winner — a payout of
// exactly expectedPayout.
func assertLedger(t *testing.T, playerID string, depositAmt, betAmt int64, won bool, expectedPayout int64) {
	t.Helper()
	rows, err := app.Pool.Query(context.Background(),
		`SELECT type, amount FROM transactions WHERE player_id = $1 ORDER BY created_at ASC`, playerID)
	if err != nil {
		t.Fatalf("query ledger for %s: %v", playerID, err)
	}
	defer rows.Close()

	type entry struct {
		Type   string
		Amount int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Type, &e.Amount); err != nil {
			t.Fatalf("scan ledger row: %v", err)
		}
		entries = append(entries, e)
	}

	wantLen := 2
	if won {
		wantLen = 3
	}
	if len(entries) != wantLen {
		t.Fatalf("ledger for %s: got %d rows %#v, want %d", playerID, len(entries), entries, wantLen)
	}
	if entries[0].Type != "deposit" || entries[0].Amount != depositAmt {
		t.Fatalf("ledger for %s: first row = %#v, want deposit %d", playerID, entries[0], depositAmt)
	}
	if entries[1].Type != "bet" || entries[1].Amount != betAmt {
		t.Fatalf("ledger for %s: second row = %#v, want bet %d", playerID, entries[1], betAmt)
	}
	if won {
		if entries[2].Type != "payout" || entries[2].Amount != expectedPayout {
			t.Fatalf("ledger for %s: third row = %#v, want payout %d", playerID, entries[2], expectedPayout)
		}
	}
}

func TestDuplicateBetRejected(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	_, token := registerAndLogin(t, "dup-bet")
	deposit(t, token, 50000)
	roomID := createRoom(t, token)

	c := dialWS(t, token)
	c.send(t, "room.join", map[string]string{"room_id": roomID})
	waitForRoomState(t, c, 1, 5*time.Second)

	c.send(t, "round.start", nil)
	c.waitFor("round.started", 5*time.Second)

	c.send(t, "bet.place", map[string]any{"choice": "even", "amount": 1000})
	c.waitFor("bet.accepted", 5*time.Second)

	c.send(t, "bet.place", map[string]any{"choice": "odd", "amount": 1000})
	errEv := c.waitFor("error", 5*time.Second).asMap(t)
	if errEv["code"] != "duplicate_bet" {
		t.Fatalf("expected error code duplicate_bet, got %#v", errEv)
	}
	if errEv["event"] != "bet.place" {
		t.Fatalf("expected error.event=bet.place, got %#v", errEv)
	}
}

func TestBetExceedsBalanceRejected(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	_, token := registerAndLogin(t, "bet-too-big")
	deposit(t, token, 500) // only 5.00
	roomID := createRoom(t, token)

	c := dialWS(t, token)
	c.send(t, "room.join", map[string]string{"room_id": roomID})
	waitForRoomState(t, c, 1, 5*time.Second)

	c.send(t, "round.start", nil)
	c.waitFor("round.started", 5*time.Second)

	c.send(t, "bet.place", map[string]any{"choice": "even", "amount": 10000}) // 100.00 > 5.00 balance
	errEv := c.waitFor("error", 5*time.Second).asMap(t)
	if errEv["code"] != "insufficient_balance" {
		t.Fatalf("expected error code insufficient_balance, got %#v", errEv)
	}

	// The rejected bet must not have moved the balance at all.
	if got := getBalance(t, token); got != 500 {
		t.Fatalf("balance after rejected bet = %d, want unchanged 500", got)
	}
}

func TestSeventhPlayerRejectedFromFullRoom(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	const totalAccounts = 7 // 1 owner + 5 more fill the 6 seats, 7th is rejected
	tokens := make([]string, totalAccounts)
	for i := range tokens {
		_, tokens[i] = registerAndLogin(t, "full-room")
	}

	roomID := createRoom(t, tokens[0]) // owner seated as player 1/6

	conns := make([]*wsClient, 0, totalAccounts)
	for i := 1; i < 6; i++ { // players 2..6 fill the remaining 5 seats
		c := dialWS(t, tokens[i])
		conns = append(conns, c)
		c.send(t, "room.join", map[string]string{"room_id": roomID})
		waitForRoomState(t, c, i+1, 5*time.Second)
	}

	seventh := dialWS(t, tokens[6])
	seventh.send(t, "room.join", map[string]string{"room_id": roomID})
	errEv := seventh.waitFor("error", 5*time.Second).asMap(t)
	if errEv["code"] != "room_full" {
		t.Fatalf("expected error code room_full, got %#v", errEv)
	}
}

func TestWithdrawBlockedThenAllowedAfterClose(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	_, token := registerAndLogin(t, "withdraw-block")
	deposit(t, token, 20000)
	roomID := createRoom(t, token) // owner is now seated -> "in an active room"

	blocked := doRequest(t, http.MethodPost, "/wallet/withdraw", token, map[string]int64{"amount": 100})
	if blocked.Status != http.StatusConflict || blocked.errorCode(t) != "withdrawal_blocked" {
		t.Fatalf("withdraw while in room: status=%d body=%#v, want 409 withdrawal_blocked", blocked.Status, blocked.Body)
	}

	closeResp := closeRoom(t, token, roomID)
	if closeResp.Status != http.StatusOK || closeResp.Body["closed"] != true {
		t.Fatalf("close room (no live round): status=%d body=%#v, want 200 closed=true", closeResp.Status, closeResp.Body)
	}

	allowed := doRequest(t, http.MethodPost, "/wallet/withdraw", token, map[string]int64{"amount": 100})
	if allowed.Status != http.StatusOK {
		t.Fatalf("withdraw after close: status=%d body=%#v, want 200", allowed.Status, allowed.Body)
	}
	if got := allowed.Body["balance"].(float64); got != 19900 {
		t.Fatalf("balance after withdraw = %v, want 19900", got)
	}
}

func TestSecondRoomJoinRejected(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	_, tokenA := registerAndLogin(t, "second-room-a")
	_, tokenB := registerAndLogin(t, "second-room-b")

	createRoom(t, tokenA) // player A now bound to a room

	// A second POST /rooms by the same (already-seated) player is rejected.
	secondCreate := doRequest(t, http.MethodPost, "/rooms", tokenA, nil)
	if secondCreate.Status != http.StatusConflict || secondCreate.errorCode(t) != "already_in_another_room" {
		t.Fatalf("second create room: status=%d body=%#v, want 409 already_in_another_room", secondCreate.Status, secondCreate.Body)
	}

	// Joining someone else's room over WS is rejected the same way.
	roomB := createRoom(t, tokenB)
	cA := dialWS(t, tokenA)
	cA.send(t, "room.join", map[string]string{"room_id": roomB})
	errEv := cA.waitFor("error", 5*time.Second).asMap(t)
	if errEv["code"] != "already_in_another_room" {
		t.Fatalf("ws join of a second room: got %#v, want code=already_in_another_room", errEv)
	}
}

func TestGracefulCloseMidRoundSettlesThenCloses(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	_, token := registerAndLogin(t, "graceful-close")
	deposit(t, token, 20000)
	roomID := createRoom(t, token)

	c := dialWS(t, token)
	c.send(t, "room.join", map[string]string{"room_id": roomID})
	waitForRoomState(t, c, 1, 5*time.Second)

	c.send(t, "round.start", nil)
	c.waitFor("round.started", 5*time.Second)

	c.send(t, "bet.place", map[string]any{"choice": "even", "amount": 1000})
	c.waitFor("bet.accepted", 5*time.Second)

	// Request a close while the round is still live (well inside the 2s
	// testBettingWindow).
	closeResp := closeRoom(t, token, roomID)
	if closeResp.Status != http.StatusOK {
		t.Fatalf("close mid-round: status=%d body=%#v", closeResp.Status, closeResp.Body)
	}
	if closeResp.Body["closed"] != false || closeResp.Body["status"] != "closing" {
		t.Fatalf("close mid-round must be pending: body=%#v, want closed=false status=closing", closeResp.Body)
	}

	// The live round must still be allowed to finish and settle...
	result := c.waitFor("round.result", settleTimeout).asMap(t)
	if result["room_closed"] != true {
		t.Fatalf("round.result after a pending close must report room_closed=true: %#v", result)
	}

	// ...and only then does the room actually close.
	bal := c.waitFor("balance.updated", 5*time.Second).asMap(t)
	if bal["reason"] != "room_end" {
		t.Fatalf("balance.updated reason = %v, want room_end", bal["reason"])
	}
	closed := c.waitFor("room.closed", 5*time.Second).asMap(t)
	if closed["reason"] != "owner_closed" {
		t.Fatalf("room.closed reason = %v, want owner_closed", closed["reason"])
	}

	var status string
	var closedAtIsNull bool
	err := app.Pool.QueryRow(context.Background(),
		`SELECT status, closed_at IS NULL FROM rooms WHERE id = $1`, roomID).Scan(&status, &closedAtIsNull)
	if err != nil {
		t.Fatalf("query room row: %v", err)
	}
	if status != "closed" {
		t.Fatalf("room status in DB = %q, want closed", status)
	}
	if closedAtIsNull {
		t.Fatalf("room closed_at must be set once status=closed")
	}
}
