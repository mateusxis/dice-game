package rest

import (
	"context"
	"net/http"
	"strings"

	"github.com/mateusxis/cassino/internal/application/ports"
)

// claimsCtxKey is the context key for the authenticated caller's ports.Claims.
type claimsCtxKey struct{}

// bearerPrefix is the required Authorization header scheme.
const bearerPrefix = "Bearer "

// Authenticator verifies the Authorization: Bearer <token> header on every
// request it wraps, rejecting the request with 401 when the header is
// missing or the token does not verify. On success it stores the caller's
// ports.Claims on the request context (read back with ClaimsFromContext) and
// tells the audit recorder, if any, who the actor is.
func Authenticator(tokens ports.TokenService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, bearerPrefix) {
				writeError(w, r, errMissingToken)
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
			if token == "" {
				writeError(w, r, errMissingToken)
				return
			}

			claims, err := tokens.Verify(token)
			if err != nil {
				writeError(w, r, errInvalidToken)
				return
			}

			if ar := auditRecorderFrom(r.Context()); ar != nil {
				ar.SetActor(claims.PlayerID)
			}

			ctx := context.WithValue(r.Context(), claimsCtxKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromContext returns the authenticated caller's claims, as attached by
// Authenticator. ok is false when the request never passed through it.
func ClaimsFromContext(ctx context.Context) (ports.Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey{}).(ports.Claims)
	return c, ok
}
