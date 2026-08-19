package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/audit"
	"github.com/mateusxis/cassino/internal/infrastructure/postgres/sqlcgen"
)

// AuditRepository is the PostgreSQL adapter for the append-only audit trail.
type AuditRepository struct {
	pool *pgxpool.Pool
}

var _ ports.AuditRepository = (*AuditRepository)(nil)

// NewAuditRepository builds an audit repository over a pool.
func NewAuditRepository(pool *pgxpool.Pool) *AuditRepository {
	return &AuditRepository{pool: pool}
}

// Append writes one audit entry. It deliberately uses the pool rather than any
// ambient transaction: an audited operation that rolls back must still leave a
// record of the attempt.
func (r *AuditRepository) Append(ctx context.Context, e *audit.Entry) error {
	id, err := toUUID(e.ID)
	if err != nil {
		return fmt.Errorf("audit id: %w", err)
	}
	// An unparseable actor id must never cost us the audit record: drop the
	// association rather than failing the write.
	actorID, actorErr := toNullUUID(e.ActorID)
	if actorErr != nil {
		actorID = pgtype.UUID{}
	}
	var payload []byte
	if len(e.Payload) > 0 {
		payload = e.Payload
	}
	if _, err := sqlcgen.New(r.pool).InsertAuditLog(ctx, sqlcgen.InsertAuditLogParams{
		ID:              id,
		OccurredAt:      toTimestamptz(e.OccurredAt),
		ActorID:         actorID,
		Channel:         string(e.Channel),
		EndpointOrEvent: e.EndpointOrEvent,
		HttpMethod:      toNullText(e.HTTPMethod),
		Action:          e.Action,
		Error:           toNullText(e.Error),
		Payload:         payload,
	}); err != nil {
		// The table's trigger raises restrict_violation on any attempt to
		// modify existing rows; surface that as the domain's immutability
		// error rather than an opaque driver failure.
		if pgErrorCode(err) == codeRestrictViolation {
			return audit.ErrImmutable
		}
		return fmt.Errorf("append audit log: %w", err)
	}
	return nil
}
