// Package auth implements the credential-facing ports: HS256 JWT issuing and
// verification, bcrypt password hashing, and UUID generation.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mateusxis/cassino/internal/application/ports"
)

// Token errors surfaced to the transport layer.
var (
	// ErrInvalidToken covers a malformed, mis-signed or otherwise unusable token.
	ErrInvalidToken = errors.New("auth: invalid token")
	// ErrExpiredToken is returned when the token is well-formed but past its expiry.
	ErrExpiredToken = errors.New("auth: token expired")
)

// JWTService issues and verifies HS256 access tokens.
type JWTService struct {
	secret []byte
	ttl    time.Duration
	issuer string
	now    func() time.Time
}

var _ ports.TokenService = (*JWTService)(nil)

// NewJWTService builds a token service. The secret comes from configuration
// and must be non-empty; the caller (config.Load) already enforces that.
func NewJWTService(secret string, ttl time.Duration) *JWTService {
	return &JWTService{
		secret: []byte(secret),
		ttl:    ttl,
		issuer: "cassino",
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// claims is the token body: the registered claims plus the player's e-mail,
// which saves a database round-trip on every authenticated request.
type claims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// Issue mints a signed access token for a player.
func (s *JWTService) Issue(playerID, email string) (string, time.Time, error) {
	issuedAt := s.now()
	expiresAt := issuedAt.Add(s.ttl)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   playerID,
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	})

	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign token: %w", err)
	}
	return signed, expiresAt, nil
}

// Verify parses and validates a token, returning the identity it carries.
// The signing method is pinned to HS256 so a token claiming "alg": "none"
// (or an asymmetric algorithm) can never be accepted.
func (s *JWTService) Verify(token string) (ports.Claims, error) {
	parsed, err := jwt.ParseWithClaims(
		token,
		&claims{},
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("%w: unexpected signing method %v", ErrInvalidToken, t.Header["alg"])
			}
			return s.secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(s.issuer),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return ports.Claims{}, ErrExpiredToken
		}
		return ports.Claims{}, errors.Join(ErrInvalidToken, err)
	}

	c, ok := parsed.Claims.(*claims)
	if !ok || !parsed.Valid || c.Subject == "" {
		return ports.Claims{}, ErrInvalidToken
	}

	var expiresAt time.Time
	if c.ExpiresAt != nil {
		expiresAt = c.ExpiresAt.Time.UTC()
	}
	return ports.Claims{
		PlayerID:  c.Subject,
		Email:     c.Email,
		ExpiresAt: expiresAt,
	}, nil
}
