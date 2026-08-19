package audit_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/audit"
)

var occurredAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestNewEntrySuccess(t *testing.T) {
	t.Parallel()

	actor := "player-1"
	method := "POST"
	payload := json.RawMessage(`{"amount":1000}`)

	entry, err := audit.NewEntry("audit-1", occurredAt, &actor, audit.ChannelREST, "/wallet/deposit", "wallet.deposit", &method, nil, payload)
	require.NoError(t, err)

	assert.True(t, entry.Succeeded())
	assert.Nil(t, entry.Error)
	assert.Equal(t, audit.ChannelREST, entry.Channel)
	require.NotNil(t, entry.HTTPMethod)
	assert.Equal(t, "POST", *entry.HTTPMethod)
	assert.JSONEq(t, `{"amount":1000}`, string(entry.Payload))
}

func TestNewEntryRecordsFailure(t *testing.T) {
	t.Parallel()

	opErr := errors.New("insufficient balance")
	entry, err := audit.NewEntry("audit-2", occurredAt, nil, audit.ChannelWS, "bet.place", "game.bet", nil, opErr, nil)
	require.NoError(t, err)

	assert.False(t, entry.Succeeded())
	require.NotNil(t, entry.Error)
	assert.Equal(t, "insufficient balance", *entry.Error)
	assert.Nil(t, entry.ActorID, "an anonymous or pre-auth call has no actor")
	assert.Nil(t, entry.HTTPMethod, "websocket entries carry no HTTP method")
}

func TestNewEntryValidation(t *testing.T) {
	t.Parallel()

	_, err := audit.NewEntry("audit-3", occurredAt, nil, audit.Channel("grpc"), "/x", "action", nil, nil, nil)
	assert.ErrorIs(t, err, audit.ErrInvalidChannel)

	_, err = audit.NewEntry("audit-3", occurredAt, nil, audit.ChannelREST, "  ", "action", nil, nil, nil)
	assert.ErrorIs(t, err, audit.ErrInvalidEndpoint)

	_, err = audit.NewEntry("audit-3", occurredAt, nil, audit.ChannelREST, "/x", "", nil, nil, nil)
	assert.ErrorIs(t, err, audit.ErrInvalidAction)
}

func TestChannelValid(t *testing.T) {
	t.Parallel()

	assert.True(t, audit.ChannelREST.Valid())
	assert.True(t, audit.ChannelWS.Valid())
	assert.False(t, audit.Channel("").Valid())
	assert.Equal(t, "rest", audit.ChannelREST.String())
}

func TestNewEntryNormalizesToUTC(t *testing.T) {
	t.Parallel()

	saoPaulo := time.FixedZone("BRT", -3*60*60)
	local := occurredAt.In(saoPaulo)

	entry, err := audit.NewEntry("audit-4", local, nil, audit.ChannelREST, "/health", "health.check", nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, time.UTC, entry.OccurredAt.Location())
	assert.True(t, entry.OccurredAt.Equal(occurredAt))
}
