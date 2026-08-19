package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/player"
	"github.com/mateusxis/cassino/internal/infrastructure/auth"
)

func TestJWTRoundTrip(t *testing.T) {
	t.Parallel()

	svc := auth.NewJWTService("super-secret", time.Hour)

	token, expiresAt, err := svc.Issue("player-1", "alice@example.com")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	assert.WithinDuration(t, time.Now().UTC().Add(time.Hour), expiresAt, 5*time.Second)

	claims, err := svc.Verify(token)
	require.NoError(t, err)
	assert.Equal(t, "player-1", claims.PlayerID)
	assert.Equal(t, "alice@example.com", claims.Email)
	assert.Equal(t, expiresAt.Unix(), claims.ExpiresAt.Unix())
}

func TestJWTRejectsForeignSignature(t *testing.T) {
	t.Parallel()

	issuer := auth.NewJWTService("secret-a", time.Hour)
	verifier := auth.NewJWTService("secret-b", time.Hour)

	token, _, err := issuer.Issue("player-1", "alice@example.com")
	require.NoError(t, err)

	_, err = verifier.Verify(token)
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestJWTRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	// A negative TTL produces a token that expired before it was issued.
	svc := auth.NewJWTService("super-secret", -time.Minute)
	token, _, err := svc.Issue("player-1", "alice@example.com")
	require.NoError(t, err)

	_, err = svc.Verify(token)
	assert.ErrorIs(t, err, auth.ErrExpiredToken)
}

func TestJWTRejectsGarbage(t *testing.T) {
	t.Parallel()

	svc := auth.NewJWTService("super-secret", time.Hour)
	for _, token := range []string{"", "not.a.token", "aaa.bbb.ccc"} {
		_, err := svc.Verify(token)
		assert.ErrorIs(t, err, auth.ErrInvalidToken, "token %q must be rejected", token)
	}
}

// A token signed with "alg": "none" must never authenticate anyone.
func TestJWTRejectsUnsignedAlgorithm(t *testing.T) {
	t.Parallel()

	svc := auth.NewJWTService("super-secret", time.Hour)
	// header {"alg":"none","typ":"JWT"} . payload {"sub":"attacker"} . (empty)
	unsigned := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJhdHRhY2tlciJ9."

	_, err := svc.Verify(unsigned)
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestBcryptHasher(t *testing.T) {
	t.Parallel()

	// MinCost keeps the test fast; production uses the configured cost.
	hasher := auth.NewBcryptHasher(4)

	hash, err := hasher.Hash("correct horse battery staple")
	require.NoError(t, err)
	assert.NotEqual(t, "correct horse battery staple", hash)

	require.NoError(t, hasher.Compare(hash, "correct horse battery staple"))
	assert.ErrorIs(t, hasher.Compare(hash, "wrong password"), player.ErrInvalidCredentials)
}

func TestBcryptHashesAreSalted(t *testing.T) {
	t.Parallel()

	hasher := auth.NewBcryptHasher(4)
	first, err := hasher.Hash("same password")
	require.NoError(t, err)
	second, err := hasher.Hash("same password")
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "each hash must use a fresh salt")
	require.NoError(t, hasher.Compare(first, "same password"))
	require.NoError(t, hasher.Compare(second, "same password"))
}

// bcrypt silently ignores bytes past 72, which would make two different long
// passwords interchangeable; we reject them instead.
func TestBcryptRejectsOverlongPassword(t *testing.T) {
	t.Parallel()

	hasher := auth.NewBcryptHasher(4)
	_, err := hasher.Hash(strings.Repeat("a", 73))
	assert.ErrorIs(t, err, auth.ErrPasswordTooLong)
}

func TestUUIDGeneratorProducesDistinctIDs(t *testing.T) {
	t.Parallel()

	gen := auth.NewUUIDGenerator()
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := gen.NewID()
		require.Len(t, id, 36)
		_, dup := seen[id]
		require.False(t, dup, "generated a duplicate id")
		seen[id] = struct{}{}
	}
}
