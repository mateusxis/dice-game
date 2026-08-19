package rest_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/interfaces/rest"
)

func protectedEcho() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := rest.ClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, "no claims", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"player_id": claims.PlayerID})
	}
}

func TestAuthenticatorRejectsMissingHeader(t *testing.T) {
	t.Parallel()

	tokens := newFakeTokenService()
	handler := rest.Authenticator(tokens)(protectedEcho())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wallet/balance", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var body map[string]map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "unauthorized", body["error"]["code"])
}

func TestAuthenticatorRejectsMalformedHeader(t *testing.T) {
	t.Parallel()

	tokens := newFakeTokenService()
	handler := rest.Authenticator(tokens)(protectedEcho())

	req := httptest.NewRequest(http.MethodGet, "/wallet/balance", nil)
	req.Header.Set("Authorization", "not-a-bearer-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticatorRejectsUnknownToken(t *testing.T) {
	t.Parallel()

	tokens := newFakeTokenService()
	handler := rest.Authenticator(tokens)(protectedEcho())

	req := httptest.NewRequest(http.MethodGet, "/wallet/balance", nil)
	req.Header.Set("Authorization", "Bearer garbage-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticatorRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	tokens := newFakeTokenService()
	token, _, err := tokens.Issue("player-1", "alice@example.com")
	require.NoError(t, err)
	tokens.expire(token)

	handler := rest.Authenticator(tokens)(protectedEcho())

	req := httptest.NewRequest(http.MethodGet, "/wallet/balance", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticatorAcceptsValidToken(t *testing.T) {
	t.Parallel()

	tokens := newFakeTokenService()
	token, _, err := tokens.Issue("player-1", "alice@example.com")
	require.NoError(t, err)

	var seenClaims ports.Claims
	var sawClaims bool
	handler := rest.Authenticator(tokens)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenClaims, sawClaims = rest.ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/wallet/balance", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, sawClaims)
	assert.Equal(t, "player-1", seenClaims.PlayerID)
}
