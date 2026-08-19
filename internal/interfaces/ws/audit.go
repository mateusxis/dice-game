package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/mateusxis/cassino/internal/domain/audit"
	"github.com/mateusxis/cassino/internal/interfaces/rest"
)

// auditTimeout bounds an audit write. It is generous: dropping the record of
// what a player did is worse than a slow event.
const auditTimeout = 5 * time.Second

// writeAudit appends one entry for a WebSocket event — the socket equivalent
// of the REST audit middleware, and it holds to the same three rules:
//
//   - every inbound event is recorded, successful or not, plus the handshake
//     itself ("ws.connect");
//   - the payload is stored with the same redaction policy as REST, so a
//     secret can never reach the table through the other channel;
//   - the write uses a context detached from the caller's, because the socket
//     may already be gone by the time the entry is built and a disconnect must
//     not erase the trail.
//
// A failed audit write is logged and swallowed: it must not take the
// connection down.
func writeAudit(ctx context.Context, opts HandlerOptions, actorID *string, event string, payload json.RawMessage, opErr error) {
	if opts.AuditRepo == nil || opts.Clock == nil || opts.IDs == nil {
		return
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if event == "" {
		event = "ws.message"
	}

	entry, err := audit.NewEntry(
		opts.IDs.NewID(),
		opts.Clock.Now(),
		actorID,
		audit.ChannelWS,
		event,
		event,
		nil, // no HTTP method on the socket channel
		opErr,
		rest.RedactPayload(payload),
	)
	if err != nil {
		logger.Error("ws audit: failed to build entry", "event", event, "error", err)
		return
	}

	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	if err := opts.AuditRepo.Append(actx, entry); err != nil {
		logger.Error("ws audit: failed to append entry", "event", event, "error", err)
	}
}
