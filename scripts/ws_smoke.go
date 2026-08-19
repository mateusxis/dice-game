// Command ws_smoke is a small, self-contained tool that exercises the full
// WebSocket gameplay loop against a running instance of the Cassino API. It
// exists because Bruno's native WebSocket request support is limited and
// version-dependent (see bruno/Realtime/ and README.md "WebSocket testing"
// for both paths) — this script is the reliable fallback that always works,
// since it only needs `go run`.
//
// It registers N fresh players (random e-mails), logs each in, deposits
// funds, has the first player create a room, has every player join it over
// WS, starts a round, has every player place a bet, and prints every event
// each socket receives until the round settles and every player has a fresh
// balance.
//
// Usage:
//
//	go run ./scripts/ws_smoke.go -base http://localhost:8090 -players 2
//
// Flags:
//
//	-base     Base HTTP URL of a running API instance (default http://localhost:8090)
//	-players  Number of players to seat, 2..6 (default 2)
//	-deposit  Cents deposited per player before playing (default 10000)
//	-bet      Cents each player wagers (default 1000)
//	-timeout  How long to wait for the round to settle (default 30s)
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type registerResponse struct {
	ID string `json:"id"`
}

type loginResponse struct {
	Token string `json:"token"`
}

type balanceResponse struct {
	Balance int64 `json:"balance"`
}

type roomResponse struct {
	ID string `json:"id"`
}

type inbound struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func main() {
	base := flag.String("base", "http://localhost:8090", "base HTTP URL of a running API instance")
	numPlayers := flag.Int("players", 2, "number of players to seat (2..6)")
	depositAmt := flag.Int64("deposit", 10000, "cents deposited per player before playing")
	betAmt := flag.Int64("bet", 1000, "cents each player wagers")
	timeout := flag.Duration("timeout", 30*time.Second, "how long to wait for the round to settle")
	flag.Parse()

	if *numPlayers < 2 || *numPlayers > 6 {
		log.Fatalf("players must be between 2 and 6, got %d", *numPlayers)
	}

	client := &http.Client{Timeout: 10 * time.Second}

	fmt.Printf("== ws_smoke: %d players against %s ==\n", *numPlayers, *base)

	type player struct {
		index int
		email string
		token string
		id    string
	}

	players := make([]player, *numPlayers)
	for i := range players {
		email := fmt.Sprintf("ws-smoke-%s@example.com", randomHex(6))
		password := "correct-horse-battery-staple"

		id, err := register(client, *base, email, password)
		must(err, "register player %d", i)
		token, err := login(client, *base, email, password)
		must(err, "login player %d", i)
		balance, err := deposit(client, *base, token, *depositAmt)
		must(err, "deposit for player %d", i)

		players[i] = player{index: i, email: email, token: token, id: id}
		fmt.Printf("player %d: id=%s email=%s balance=%d\n", i, id, email, balance)
	}

	roomID, err := createRoom(client, *base, players[0].token)
	must(err, "create room")
	fmt.Printf("room created: %s (owner: player 0)\n", roomID)

	wsBase := strings.Replace(strings.Replace(*base, "http://", "ws://", 1), "https://", "wss://", 1)

	var wg sync.WaitGroup
	done := make(chan struct{})
	conns := make([]*websocket.Conn, len(players))

	// The hub allows exactly one connection per player — a second dial for
	// the same token replaces the first (error{connection_replaced}) — so
	// every frame for a given player, including the owner's round.start,
	// must reuse that player's single socket.
	for _, p := range players {
		p := p
		u := fmt.Sprintf("%s/ws?token=%s", wsBase, url.QueryEscape(p.token))
		conn, _, err := websocket.DefaultDialer.Dial(u, nil)
		must(err, "dial ws for player %d", p.index)
		defer conn.Close()
		conns[p.index] = conn

		sendJSON(conn, "room.join", map[string]string{"room_id": roomID})

		wg.Add(1)
		go func() {
			defer wg.Done()
			readLoop(conn, p.index, betAmt, done)
		}()
	}

	// Give everyone a moment to join before the owner starts the round.
	time.Sleep(500 * time.Millisecond)
	sendJSON(conns[0], "round.start", nil)
	fmt.Println("owner sent round.start")

	select {
	case <-done:
		fmt.Println("== round settled, smoke test complete ==")
	case <-time.After(*timeout):
		fmt.Println("== timed out waiting for settlement ==")
		os.Exit(1)
	}

	wg.Wait()
}

// readLoop prints every event a socket receives, places a bet as soon as
// round.started arrives (alternating even/odd by player index), and signals
// done (non-blocking — the first player to finish wakes the main goroutine)
// once this player has seen both a round.result and a balance.updated.
func readLoop(conn *websocket.Conn, index int, betAmt *int64, done chan struct{}) {
	choice := "even"
	if index%2 == 1 {
		choice = "odd"
	}

	gotResult, gotBalance := false, false
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg inbound
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		fmt.Printf("[player %d] %s %s\n", index, msg.Type, string(msg.Data))

		switch msg.Type {
		case "round.started":
			sendJSON(conn, "bet.place", map[string]any{"choice": choice, "amount": *betAmt})
		case "round.result":
			gotResult = true
		case "balance.updated":
			gotBalance = true
		}

		if gotResult && gotBalance {
			select {
			case done <- struct{}{}:
			default:
			}
			return
		}
	}
}

func sendJSON(conn *websocket.Conn, eventType string, data any) {
	payload := map[string]any{"type": eventType}
	if data != nil {
		payload["data"] = data
	}
	if err := conn.WriteJSON(payload); err != nil {
		log.Printf("write %s: %v", eventType, err)
	}
}

func register(client *http.Client, base, email, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := client.Post(base+"/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, readAll(resp.Body))
	}
	var out registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func login(client *http.Client, base, email, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := client.Post(base+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, readAll(resp.Body))
	}
	var out loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Token, nil
}

func deposit(client *http.Client, base, token string, amount int64) (int64, error) {
	body, _ := json.Marshal(map[string]int64{"amount": amount})
	req, _ := http.NewRequest(http.MethodPost, base+"/wallet/deposit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, readAll(resp.Body))
	}
	var out balanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Balance, nil
}

func createRoom(client *http.Client, base, token string) (string, error) {
	req, _ := http.NewRequest(http.MethodPost, base+"/rooms", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, readAll(resp.Body))
	}
	var out roomResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func readAll(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func must(err error, format string, args ...any) {
	if err != nil {
		log.Fatalf(format+": %v", append(args, err)...)
	}
}
