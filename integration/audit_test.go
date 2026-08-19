//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pollForAuditRow polls audit_logs for a row matching the given predicate
// SQL (a WHERE clause fragment using $1..$N placeholders) up to timeout,
// since the audit write happens in a goroutine-detached, best-effort manner
// slightly after the HTTP/WS response is already on the wire.
func pollForAuditRow(t *testing.T, timeout time.Duration, whereClause string, args ...any) (action string, errText *string, payload []byte, found bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	query := `SELECT action, error, payload FROM audit_logs WHERE ` + whereClause + ` ORDER BY occurred_at DESC LIMIT 1`
	for {
		row := app.Pool.QueryRow(context.Background(), query, args...)
		var action_ string
		var errText_ *string
		var payload_ []byte
		err := row.Scan(&action_, &errText_, &payload_)
		if err == nil {
			return action_, errText_, payload_, true
		}
		if time.Now().After(deadline) {
			return "", nil, nil, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAuditRESTRecorded(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	email := uniqueEmail("audit-rest")
	resp := doRequest(t, http.MethodPost, "/auth/register", "", map[string]string{
		"email": email, "password": testPassword,
	})
	if resp.Status != http.StatusCreated {
		t.Fatalf("register: status=%d body=%#v", resp.Status, resp.Body)
	}
	playerID, _ := resp.Body["id"].(string)

	action, errText, payload, found := pollForAuditRow(t, 5*time.Second,
		"channel = 'rest' AND endpoint_or_event = '/auth/register' AND actor_id IS NULL AND payload::text LIKE $1",
		"%"+email+"%")
	if !found {
		t.Fatalf("no audit_logs row found for /auth/register with email %s", email)
	}
	if action != "auth.register" {
		t.Fatalf("action = %q, want auth.register", action)
	}
	if errText != nil {
		t.Fatalf("register succeeded but audit row carries an error: %q", *errText)
	}
	if !strings.Contains(string(payload), email) {
		t.Fatalf("payload does not contain the registered email: %s", payload)
	}
	if strings.Contains(string(payload), testPassword) {
		t.Fatalf("payload leaks the plaintext password, must be redacted: %s", payload)
	}
	if !strings.Contains(string(payload), "REDACTED") {
		t.Fatalf("payload should carry a [REDACTED] password field: %s", payload)
	}

	// A later authenticated call (deposit) must be attributed to the actor.
	_, token := loginOnly(t, email)
	deposit(t, token, 1000)

	depositAction, depositErr, _, depositFound := pollForAuditRow(t, 5*time.Second,
		"channel = 'rest' AND endpoint_or_event = '/wallet/deposit' AND actor_id = $1", playerID)
	if !depositFound {
		t.Fatalf("no audit_logs row found for /wallet/deposit by actor %s", playerID)
	}
	if depositAction != "wallet.deposit" {
		t.Fatalf("action = %q, want wallet.deposit", depositAction)
	}
	if depositErr != nil {
		t.Fatalf("successful deposit but audit row carries an error: %q", *depositErr)
	}
}

// loginOnly logs an already-registered e-mail in, without creating a new
// account (registerAndLogin always registers fresh).
func loginOnly(t *testing.T, email string) (playerID, token string) {
	t.Helper()
	resp := doRequest(t, http.MethodPost, "/auth/login", "", map[string]string{
		"email": email, "password": testPassword,
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("login %s: status=%d body=%#v", email, resp.Status, resp.Body)
	}
	token, _ = resp.Body["token"].(string)
	return "", token
}

func TestAuditWSRecorded(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	playerID, token := registerAndLogin(t, "audit-ws")

	c := dialWS(t, token)

	_, connectErrText, _, connectFound := pollForAuditRow(t, 5*time.Second,
		"channel = 'ws' AND endpoint_or_event = 'ws.connect' AND actor_id = $1", playerID)
	if !connectFound {
		t.Fatalf("no audit_logs row found for ws.connect by actor %s", playerID)
	}
	if connectErrText != nil {
		t.Fatalf("successful connect but audit row carries an error: %q", *connectErrText)
	}

	// A rejected event (joining a room that doesn't exist) must still be
	// audited, with the failure text captured.
	c.send(t, "room.join", map[string]string{"room_id": "00000000-0000-0000-0000-000000000000"})
	errEv := c.waitFor("error", 5*time.Second).asMap(t)
	if errEv["code"] != "room_not_found" {
		t.Fatalf("expected room_not_found, got %#v", errEv)
	}

	joinAction, joinErrText, _, joinFound := pollForAuditRow(t, 5*time.Second,
		"channel = 'ws' AND endpoint_or_event = 'room.join' AND actor_id = $1", playerID)
	if !joinFound {
		t.Fatalf("no audit_logs row found for room.join by actor %s", playerID)
	}
	if joinAction != "room.join" {
		t.Fatalf("action = %q, want room.join", joinAction)
	}
	if joinErrText == nil {
		t.Fatalf("failed room.join must carry an error in its audit row")
	}
}

func TestAuditLogsAppendOnly(t *testing.T) {
	requireDeps(t)
	t.Parallel()

	// Seed at least one row via a real REST call, then attack it directly.
	email := uniqueEmail("audit-immutable")
	resp := doRequest(t, http.MethodPost, "/auth/register", "", map[string]string{
		"email": email, "password": testPassword,
	})
	if resp.Status != http.StatusCreated {
		t.Fatalf("register: status=%d body=%#v", resp.Status, resp.Body)
	}

	var id string
	err := app.Pool.QueryRow(context.Background(),
		`SELECT id FROM audit_logs WHERE endpoint_or_event = '/auth/register' AND payload::text LIKE $1 ORDER BY occurred_at DESC LIMIT 1`,
		"%"+email+"%").Scan(&id)
	if err != nil {
		t.Fatalf("find seeded audit row: %v", err)
	}

	_, err = app.Pool.Exec(context.Background(), `UPDATE audit_logs SET action = 'tampered' WHERE id = $1`, id)
	if err == nil {
		t.Fatalf("UPDATE on audit_logs must be rejected by the append-only trigger, but succeeded")
	}
	if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE error = %v, want it to mention append-only", err)
	}

	_, err = app.Pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE id = $1`, id)
	if err == nil {
		t.Fatalf("DELETE on audit_logs must be rejected by the append-only trigger, but succeeded")
	}
	if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE error = %v, want it to mention append-only", err)
	}
}
