// Package rest holds the HTTP delivery layer: the chi router, its middleware
// stack and the handlers. Phase 2 adds auth (register/login) and wallet
// (deposit/withdraw/balance) routes, the JWT authenticator and the audit
// middleware; room and WebSocket routes hang off the same router in Phase 3.
package rest

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	authapp "github.com/mateusxis/cassino/internal/application/auth"
	"github.com/mateusxis/cassino/internal/application/ports"
	walletapp "github.com/mateusxis/cassino/internal/application/wallet"
)

// HealthChecker reports whether a downstream dependency is reachable. The
// health handler calls one per dependency and never blocks longer than the
// request context allows.
type HealthChecker func(r *http.Request) error

// Dependency pairs a name with its probe, for the /health response body.
type Dependency struct {
	Name  string
	Check HealthChecker
}

// RouterOptions configures the router built by NewRouter.
type RouterOptions struct {
	// Version is reported by /health, useful when several builds are running.
	Version string
	// Dependencies are probed by /health; an unreachable one turns the
	// response into 503.
	Dependencies []Dependency
	// RequestTimeout bounds every request. Zero disables the timeout.
	RequestTimeout time.Duration

	// RegisterUseCase, LoginUseCase back POST /auth/register and
	// POST /auth/login — the two endpoints open to unauthenticated callers.
	RegisterUseCase *authapp.RegisterUseCase
	LoginUseCase    *authapp.LoginUseCase

	// DepositUseCase, WithdrawUseCase, BalanceUseCase back the /wallet/*
	// routes. All of them require an authenticated caller.
	DepositUseCase  *walletapp.DepositUseCase
	WithdrawUseCase *walletapp.WithdrawUseCase
	BalanceUseCase  *walletapp.GetBalanceUseCase

	// Tokens verifies bearer tokens for every route except the open ones and
	// /health.
	Tokens ports.TokenService

	// AuditRepo, Clock and IDs back the audit middleware, which wraps every
	// route mounted below except /health. Leaving AuditRepo nil disables
	// auditing (used by narrow unit tests that only exercise /health).
	AuditRepo ports.AuditRepository
	Clock     ports.Clock
	IDs       ports.IDGenerator

	// Logger receives audit-write failures and other middleware-level
	// diagnostics. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// NewRouter builds the HTTP router: base middleware, the health probe, and
// the audited auth/wallet API. /health is deliberately outside the audit and
// JWT middleware — it carries no business payload and orchestrators must be
// able to call it unauthenticated.
func NewRouter(opts RouterOptions) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	// Recoverer turns a panic in any handler into a 500 instead of taking the
	// whole process down with it.
	r.Use(middleware.Recoverer)
	if opts.RequestTimeout > 0 {
		r.Use(middleware.Timeout(opts.RequestTimeout))
	}

	r.Get("/health", healthHandler(opts))

	r.Group(func(gr chi.Router) {
		gr.Use(AuditMiddleware(opts.AuditRepo, opts.Clock, opts.IDs, opts.Logger))

		gr.Post("/auth/register", RegisterHandler(opts.RegisterUseCase))
		gr.Post("/auth/login", LoginHandler(opts.LoginUseCase))

		gr.Group(func(pr chi.Router) {
			pr.Use(Authenticator(opts.Tokens))

			pr.Post("/wallet/deposit", DepositHandler(opts.DepositUseCase))
			pr.Post("/wallet/withdraw", WithdrawHandler(opts.WithdrawUseCase))
			pr.Get("/wallet/balance", BalanceHandler(opts.BalanceUseCase))
		})
	})

	return r
}

type healthResponse struct {
	Status       string            `json:"status"`
	Version      string            `json:"version,omitempty"`
	Time         time.Time         `json:"time"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
}

// healthHandler reports liveness plus the state of each dependency. It returns
// 200 when everything is reachable and 503 otherwise, which is what the
// compose healthcheck and any orchestrator watch.
func healthHandler(opts RouterOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := healthResponse{
			Status:  "ok",
			Version: opts.Version,
			Time:    time.Now().UTC(),
		}
		status := http.StatusOK

		if len(opts.Dependencies) > 0 {
			resp.Dependencies = make(map[string]string, len(opts.Dependencies))
			for _, dep := range opts.Dependencies {
				if err := dep.Check(r); err != nil {
					resp.Dependencies[dep.Name] = "error: " + err.Error()
					resp.Status = "degraded"
					status = http.StatusServiceUnavailable
					continue
				}
				resp.Dependencies[dep.Name] = "ok"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
