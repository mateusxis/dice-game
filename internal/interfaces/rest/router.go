// Package rest holds the HTTP delivery layer: the chi router, its middleware
// stack and the handlers. Phase 1 mounts only the health probe; auth, wallet
// and room routes hang off the same router in later phases.
package rest

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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
}

// NewRouter builds the HTTP router with the base middleware stack.
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
