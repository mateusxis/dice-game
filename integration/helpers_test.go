//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// uniqueEmail returns an email that has never been used by any other test in
// this run, so tests never collide on the players_email_key unique index
// even though they all share one database for the whole suite.
func uniqueEmail(label string) string {
	return fmt.Sprintf("%s-%s@integration.test", label, uuid.NewString())
}

const testPassword = "correct-horse-battery-staple"

// apiResponse is a generically-decoded JSON response, used so a single
// helper can serve both the happy path (decode into a typed struct by the
// caller re-marshaling Body) and the error-case assertions (read
// error.code straight out of Body).
type apiResponse struct {
	Status int
	Body   map[string]any
}

func (r apiResponse) errorCode(t *testing.T) string {
	t.Helper()
	errObj, ok := r.Body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error envelope: %#v", r.Body)
	}
	code, _ := errObj["code"].(string)
	return code
}

func doRequest(t *testing.T, method, path, token string, body any) apiResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	var decoded map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s %s: decode response %q: %v", method, path, raw, err)
		}
	}
	return apiResponse{Status: resp.StatusCode, Body: decoded}
}

// registerAndLogin registers a fresh account and immediately logs in,
// returning its player id and bearer token — the setup every test scenario
// starts from.
func registerAndLogin(t *testing.T, label string) (playerID, token string) {
	t.Helper()
	email := uniqueEmail(label)

	reg := doRequest(t, http.MethodPost, "/auth/register", "", map[string]string{
		"email": email, "password": testPassword,
	})
	if reg.Status != http.StatusCreated {
		t.Fatalf("register %s: status %d body %#v", email, reg.Status, reg.Body)
	}
	playerID, _ = reg.Body["id"].(string)

	login := doRequest(t, http.MethodPost, "/auth/login", "", map[string]string{
		"email": email, "password": testPassword,
	})
	if login.Status != http.StatusOK {
		t.Fatalf("login %s: status %d body %#v", email, login.Status, login.Body)
	}
	token, _ = login.Body["token"].(string)
	if playerID == "" || token == "" {
		t.Fatalf("register/login for %s did not yield id+token: reg=%#v login=%#v", email, reg.Body, login.Body)
	}
	return playerID, token
}

func deposit(t *testing.T, token string, amount int64) int64 {
	t.Helper()
	resp := doRequest(t, http.MethodPost, "/wallet/deposit", token, map[string]int64{"amount": amount})
	if resp.Status != http.StatusOK {
		t.Fatalf("deposit %d: status %d body %#v", amount, resp.Status, resp.Body)
	}
	return int64(resp.Body["balance"].(float64))
}

func getBalance(t *testing.T, token string) int64 {
	t.Helper()
	resp := doRequest(t, http.MethodGet, "/wallet/balance", token, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("get balance: status %d body %#v", resp.Status, resp.Body)
	}
	return int64(resp.Body["balance"].(float64))
}

func createRoom(t *testing.T, token string) string {
	t.Helper()
	resp := doRequest(t, http.MethodPost, "/rooms", token, nil)
	if resp.Status != http.StatusCreated {
		t.Fatalf("create room: status %d body %#v", resp.Status, resp.Body)
	}
	id, _ := resp.Body["id"].(string)
	if id == "" {
		t.Fatalf("create room: no id in response %#v", resp.Body)
	}
	return id
}

func closeRoom(t *testing.T, token, roomID string) apiResponse {
	t.Helper()
	return doRequest(t, http.MethodDelete, "/rooms/"+roomID, token, nil)
}

// ---------------------------------------------------------------------------
// WebSocket client
// ---------------------------------------------------------------------------

type wsEvent struct {
	Type string
	Data json.RawMessage
}

func (e wsEvent) decode(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(e.Data, dst); err != nil {
		t.Fatalf("decode %s payload %s: %v", e.Type, e.Data, err)
	}
}

func (e wsEvent) asMap(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	e.decode(t, &m)
	return m
}

// wsClient wraps one authenticated gameplay socket. A background goroutine
// drains frames into a buffered channel so waitFor can skip over events that
// arrive out of the order a particular assertion cares about (e.g. another
// player's room.state broadcast) without losing them.
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
	ch   chan wsEvent
	done chan struct{}
}

func dialWS(t *testing.T, token string) *wsClient {
	t.Helper()
	dialURL := wsBaseURL + "/ws?token=" + url.QueryEscape(token)
	conn, resp, err := websocket.DefaultDialer.Dial(dialURL, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial ws (http status %d): %v", status, err)
	}
	c := &wsClient{t: t, conn: conn, ch: make(chan wsEvent, 128), done: make(chan struct{})}
	go c.readLoop()
	t.Cleanup(c.close)
	return c
}

func (c *wsClient) readLoop() {
	defer close(c.ch)
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		select {
		case c.ch <- wsEvent{Type: msg.Type, Data: msg.Data}:
		case <-c.done:
			return
		}
	}
}

func (c *wsClient) close() {
	select {
	case <-c.done:
		return
	default:
		close(c.done)
	}
	_ = c.conn.Close()
}

func (c *wsClient) send(t *testing.T, eventType string, data any) {
	t.Helper()
	payload := map[string]any{"type": eventType}
	if data != nil {
		payload["data"] = data
	}
	if err := c.conn.WriteJSON(payload); err != nil {
		t.Fatalf("ws send %s: %v", eventType, err)
	}
}

// waitFor blocks until an event of the given type arrives, or timeout
// elapses. Non-matching events are consumed (not put back) and remembered
// only for the failure message, so a test asserting on round.result is not
// tripped up by an interleaved room.state.
func (c *wsClient) waitFor(eventType string, timeout time.Duration) wsEvent {
	c.t.Helper()
	deadline := time.After(timeout)
	var skipped []string
	for {
		select {
		case ev, ok := <-c.ch:
			if !ok {
				c.t.Fatalf("ws connection closed while waiting for %q (saw: %v)", eventType, skipped)
			}
			if ev.Type == eventType {
				return ev
			}
			skipped = append(skipped, ev.Type)
		case <-deadline:
			c.t.Fatalf("timed out after %s waiting for %q (saw: %v)", timeout, eventType, skipped)
		}
	}
}
