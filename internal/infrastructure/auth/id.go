package auth

import (
	"github.com/google/uuid"

	"github.com/mateusxis/cassino/internal/application/ports"
)

// UUIDGenerator implements ports.IDGenerator with random (v4) UUIDs. IDs are
// minted in the application rather than by the database default so a use case
// can reference an entity before the INSERT lands — for instance, writing a
// ledger entry that points at the bet it settles.
type UUIDGenerator struct{}

var _ ports.IDGenerator = UUIDGenerator{}

// NewUUIDGenerator returns the production id generator.
func NewUUIDGenerator() UUIDGenerator { return UUIDGenerator{} }

// NewID returns a fresh v4 UUID string.
func (UUIDGenerator) NewID() string { return uuid.NewString() }
