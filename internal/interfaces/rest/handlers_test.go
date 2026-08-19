package rest_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/audit"
)

// doRequest issues method/path with an optional JSON body and bearer token
// against router, returning the recorded response.
func doRequest(t *testing.T, router *chi.Mux, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// errorEnvelope decodes {"error": {"code": ..., "message": ...}}.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NotEmpty(t, env.Error.Code, "a non-2xx response must carry the {error:{code,message}} envelope")
	return env
}

// registerAndLogin is a test helper that registers a fresh account and
// returns its player id and a valid access token.
func registerAndLogin(t *testing.T, router *chi.Mux, email, password string) (playerID, token string) {
	t.Helper()

	regRec := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{
		"email":    email,
		"password": password,
	})
	require.Equal(t, http.StatusCreated, regRec.Code, "register: %s", regRec.Body.String())

	var regBody struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(regRec.Body.Bytes(), &regBody))

	loginRec := doRequest(t, router, http.MethodPost, "/auth/login", "", map[string]string{
		"email":    email,
		"password": password,
	})
	require.Equal(t, http.StatusOK, loginRec.Code, "login: %s", loginRec.Body.String())

	var loginBody struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginRec.Body.Bytes(), &loginBody))

	return regBody.ID, loginBody.Token
}

// --- register ----------------------------------------------------------

func TestRegisterHandlerSuccess(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	rec := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{
		"email":    "alice@example.com",
		"password": "correct-horse",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.NotContains(t, rec.Body.String(), "correct-horse", "the response must never echo the password")
	assert.NotContains(t, rec.Body.String(), "hashed:", "the response must never echo the password hash")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "alice@example.com", body["email"])
	assert.Equal(t, float64(0), body["balance"])
	assert.NotContains(t, body, "password")
	assert.NotContains(t, body, "password_hash")
}

func TestRegisterHandlerDuplicateEmail(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	first := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{"email": "alice@example.com", "password": "correct-horse"})
	require.Equal(t, http.StatusCreated, first.Code)

	second := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{"email": "alice@example.com", "password": "another-pass"})
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Equal(t, "email_already_used", decodeError(t, second).Error.Code)
}

func TestRegisterHandlerWeakPassword(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	rec := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{"email": "alice@example.com", "password": "short"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeError(t, rec).Error.Code)
}

func TestRegisterHandlerBadEmail(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	rec := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{"email": "not-an-email", "password": "correct-horse"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeError(t, rec).Error.Code)
}

func TestRegisterHandlerMalformedBody(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeError(t, rec).Error.Code)
}

// --- login ---------------------------------------------------------------

func TestLoginHandlerSuccess(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	reg := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{"email": "alice@example.com", "password": "correct-horse"})
	require.Equal(t, http.StatusCreated, reg.Code)

	rec := doRequest(t, router, http.MethodPost, "/auth/login", "", map[string]string{"email": "alice@example.com", "password": "correct-horse"})
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotEmpty(t, body.Token)
	assert.NotEmpty(t, body.ExpiresAt)
}

func TestLoginHandlerWrongPassword(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	reg := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{"email": "alice@example.com", "password": "correct-horse"})
	require.Equal(t, http.StatusCreated, reg.Code)

	rec := doRequest(t, router, http.MethodPost, "/auth/login", "", map[string]string{"email": "alice@example.com", "password": "wrong-pass"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", decodeError(t, rec).Error.Code)
}

func TestLoginHandlerUnknownEmail(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	rec := doRequest(t, router, http.MethodPost, "/auth/login", "", map[string]string{"email": "nobody@example.com", "password": "whatever1"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", decodeError(t, rec).Error.Code)
}

// --- wallet ----------------------------------------------------------------

func TestWalletEndpointsRequireAuthentication(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/wallet/deposit", map[string]int64{"amount": 100}},
		{http.MethodPost, "/wallet/withdraw", map[string]int64{"amount": 100}},
		{http.MethodGet, "/wallet/balance", nil},
	}
	for _, tc := range cases {
		rec := doRequest(t, router, tc.method, tc.path, "", tc.body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s without a token must be rejected", tc.method, tc.path)
		assert.Equal(t, "unauthorized", decodeError(t, rec).Error.Code)
	}
}

func TestWalletDepositAndBalance(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	_, token := registerAndLogin(t, router, "alice@example.com", "correct-horse")

	rec := doRequest(t, router, http.MethodPost, "/wallet/deposit", token, map[string]int64{"amount": 1_500})
	require.Equal(t, http.StatusOK, rec.Code)
	var depositBody struct {
		Balance int64 `json:"balance"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &depositBody))
	assert.Equal(t, int64(1_500), depositBody.Balance)

	balRec := doRequest(t, router, http.MethodGet, "/wallet/balance", token, nil)
	require.Equal(t, http.StatusOK, balRec.Code)
	var balBody struct {
		Balance int64 `json:"balance"`
	}
	require.NoError(t, json.Unmarshal(balRec.Body.Bytes(), &balBody))
	assert.Equal(t, int64(1_500), balBody.Balance)
}

func TestWalletDepositRejectsNonPositiveAmount(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	_, token := registerAndLogin(t, router, "alice@example.com", "correct-horse")

	rec := doRequest(t, router, http.MethodPost, "/wallet/deposit", token, map[string]int64{"amount": 0})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", decodeError(t, rec).Error.Code)
}

func TestWalletWithdrawSuccess(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	_, token := registerAndLogin(t, router, "alice@example.com", "correct-horse")

	dep := doRequest(t, router, http.MethodPost, "/wallet/deposit", token, map[string]int64{"amount": 1_000})
	require.Equal(t, http.StatusOK, dep.Code)

	rec := doRequest(t, router, http.MethodPost, "/wallet/withdraw", token, map[string]int64{"amount": 400})
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Balance int64 `json:"balance"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, int64(600), body.Balance)
}

func TestWalletWithdrawInsufficientBalance(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	_, token := registerAndLogin(t, router, "alice@example.com", "correct-horse")

	rec := doRequest(t, router, http.MethodPost, "/wallet/withdraw", token, map[string]int64{"amount": 100})
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "insufficient_balance", decodeError(t, rec).Error.Code)
}

func TestWalletWithdrawBlockedWhileInActiveRoom(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)
	_, token := registerAndLogin(t, router, "alice@example.com", "correct-horse")

	dep := doRequest(t, router, http.MethodPost, "/wallet/deposit", token, map[string]int64{"amount": 1_000})
	require.Equal(t, http.StatusOK, dep.Code)

	// Simulate the player being seated in a room via the fast Redis-backed
	// session store, exactly what game.JoinRoom would set in Phase 3.
	deps.sessions.activeRoom = "room-1"

	rec := doRequest(t, router, http.MethodPost, "/wallet/withdraw", token, map[string]int64{"amount": 100})
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "withdrawal_blocked", decodeError(t, rec).Error.Code)
}

// --- audit -----------------------------------------------------------------

func TestAuditRedactsPasswordAndRecordsEveryCall(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)

	rec := doRequest(t, router, http.MethodPost, "/auth/register", "", map[string]string{
		"email":    "alice@example.com",
		"password": "supersecret1",
	})
	require.Equal(t, http.StatusCreated, rec.Code)

	entries := deps.auditRepo.all()
	require.NotEmpty(t, entries)

	var registerEntry *audit.Entry
	for _, e := range entries {
		if e.Action == "auth.register" {
			registerEntry = e
			break
		}
	}
	require.NotNil(t, registerEntry, "the register call must produce an audit entry")

	assert.NotContains(t, string(registerEntry.Payload), "supersecret1", "the plaintext password must never reach the audit log")
	assert.Contains(t, string(registerEntry.Payload), "[REDACTED]")
	assert.Contains(t, string(registerEntry.Payload), "alice@example.com", "non-secret fields must still be recorded")
	assert.Nil(t, registerEntry.Error, "a successful call must not carry an error")
	assert.Nil(t, registerEntry.ActorID, "register is unauthenticated: there is no actor yet")
	assert.Equal(t, "/auth/register", registerEntry.EndpointOrEvent)
	require.NotNil(t, registerEntry.HTTPMethod)
	assert.Equal(t, http.MethodPost, *registerEntry.HTTPMethod)
}

func TestAuditRecordsActorOnAuthenticatedCalls(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)
	playerID, token := registerAndLogin(t, router, "alice@example.com", "correct-horse")

	rec := doRequest(t, router, http.MethodGet, "/wallet/balance", token, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	entries := deps.auditRepo.all()
	var balanceEntry *audit.Entry
	for _, e := range entries {
		if e.Action == "wallet.balance" {
			balanceEntry = e
		}
	}
	require.NotNil(t, balanceEntry)
	require.NotNil(t, balanceEntry.ActorID)
	assert.Equal(t, playerID, *balanceEntry.ActorID)
}

func TestAuditRecordsFailuresWithErrorMessage(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)

	rec := doRequest(t, router, http.MethodPost, "/auth/login", "", map[string]string{"email": "nobody@example.com", "password": "whatever1"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	entries := deps.auditRepo.all()
	var loginEntry *audit.Entry
	for _, e := range entries {
		if e.Action == "auth.login" {
			loginEntry = e
		}
	}
	require.NotNil(t, loginEntry)
	require.NotNil(t, loginEntry.Error, "a failed call must carry the error field")
	assert.Contains(t, *loginEntry.Error, "invalid credentials")
}
