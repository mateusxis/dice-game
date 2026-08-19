package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/audit"
)

// maxAuditBodyBytes caps how much of a request body the audit middleware
// buffers for the payload column. Bodies on this API are small JSON objects;
// anything past this is truncated rather than held in memory whole.
const maxAuditBodyBytes = 64 << 10 // 64 KiB

// auditActions maps a route pattern + method to the business action name
// recorded in the audit log. Anything not listed falls back to "<method>
// <pattern>".
var auditActions = map[string]string{
	"POST /auth/register":   "auth.register",
	"POST /auth/login":      "auth.login",
	"POST /wallet/deposit":  "wallet.deposit",
	"POST /wallet/withdraw": "wallet.withdraw",
	"GET /wallet/balance":   "wallet.balance",
	"POST /rooms":           "room.create",
	"GET /rooms":            "room.list",
	"DELETE /rooms/{id}":    "room.close",
}

// auditCtxKey is the context key for the *auditRecorder attached to a
// request by AuditMiddleware.
type auditCtxKey struct{}

// auditRecorder is a mutable, context-carried scratchpad that inner
// middleware and handlers use to tell the (outer) audit middleware who the
// caller turned out to be and what, if anything, went wrong. It has to be a
// pointer shared by reference through the context because http.Request's
// context is only updated for handlers further down the chain — the audit
// middleware's own deferred logic runs against its original request value.
type auditRecorder struct {
	mu      sync.Mutex
	actorID *string
	errMsg  *string
}

// SetActor records the authenticated player id once the auth middleware
// verifies a token.
func (a *auditRecorder) SetActor(playerID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	id := playerID
	a.actorID = &id
}

// SetError records the failure text for the request; handlers call this
// (via writeError) instead of the audit middleware trying to reconstruct it
// from a status code alone.
func (a *auditRecorder) SetError(msg string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := msg
	a.errMsg = &m
}

func (a *auditRecorder) actor() *string {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.actorID
}

func (a *auditRecorder) failure() *string {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.errMsg
}

// auditRecorderFrom returns the recorder attached to ctx, or nil when none
// is present (for instance, in a handler unit test that does not go through
// AuditMiddleware).
func auditRecorderFrom(ctx context.Context) *auditRecorder {
	rec, _ := ctx.Value(auditCtxKey{}).(*auditRecorder)
	return rec
}

// statusWriter captures the status code a handler wrote, defaulting to 200
// to match net/http's own behaviour when WriteHeader is never called.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.status = http.StatusOK
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

// AuditMiddleware wraps every request it sees in an append-only audit.Entry,
// including successful ones, failures, and the open auth endpoints. It never
// fails the request: a broken audit write is logged and swallowed.
//
// Request bodies are buffered, redacted (any JSON key containing
// "password", case-insensitively, anywhere in the body is replaced) and
// stored as the entry's payload; the original body is restored so the
// downstream handler can still decode it.
func AuditMiddleware(repo ports.AuditRepository, clock ports.Clock, ids ports.IDGenerator, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		if repo == nil || clock == nil || ids == nil {
			// Missing wiring (e.g. a router built for a narrow unit test) —
			// behave as a no-op rather than panic.
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload json.RawMessage
			if r.Body != nil {
				raw, _ := io.ReadAll(io.LimitReader(r.Body, maxAuditBodyBytes))
				_ = r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(raw))
				payload = redactPayload(raw)
			}

			rec := &auditRecorder{}
			ctx := context.WithValue(r.Context(), auditCtxKey{}, rec)
			r = r.WithContext(ctx)

			sw := &statusWriter{ResponseWriter: w}

			method := r.Method
			start := clock.Now()

			defer func() {
				pattern := routePattern(r)
				action, ok := auditActions[method+" "+pattern]
				if !ok {
					action = method + " " + pattern
				}

				var opErr error
				if sw.status >= http.StatusBadRequest {
					if msg := rec.failure(); msg != nil {
						opErr = errString(*msg)
					} else {
						opErr = errString(http.StatusText(sw.status))
					}
				}

				entry, err := audit.NewEntry(
					ids.NewID(),
					start,
					rec.actor(),
					audit.ChannelREST,
					pattern,
					action,
					&method,
					opErr,
					payload,
				)
				if err != nil {
					logger.Error("audit: failed to build entry", "error", err, "path", r.URL.Path)
					return
				}

				// Deliberately not r.Context(): the request may already be
				// cancelled (client disconnect) by the time this runs, and an
				// audit record must still be written for a request that made
				// it this far.
				actx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := repo.Append(actx, entry); err != nil {
					logger.Error("audit: failed to append entry", "error", err, "path", r.URL.Path)
				}
			}()

			next.ServeHTTP(sw, r)
		})
	}
}

// errString is a tiny error adapter so a plain string can be passed where
// audit.NewEntry wants an error.
type errString string

func (e errString) Error() string { return string(e) }

// routePattern returns the chi route pattern matched for r ("/wallet/deposit")
// falling back to the raw path when chi has not resolved one (e.g. a 404).
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return r.URL.Path
}

// RedactPayload applies this API's redaction policy to a raw JSON body and
// returns what may be persisted in the audit trail. It is exported so the
// WebSocket audit writer shares exactly one policy with the REST middleware
// instead of drifting into a second, subtly different one.
func RedactPayload(raw []byte) json.RawMessage { return redactPayload(raw) }

// redactPayload parses raw as JSON and blanks out any object key containing
// "password" (case-insensitive), at any nesting depth. Non-JSON or empty
// bodies produce a nil payload rather than being stored as an opaque blob.
func redactPayload(raw []byte) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	redactValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return out
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if strings.Contains(strings.ToLower(k), "password") {
				t[k] = "[REDACTED]"
				continue
			}
			redactValue(val)
		}
	case []any:
		for _, item := range t {
			redactValue(item)
		}
	}
}
