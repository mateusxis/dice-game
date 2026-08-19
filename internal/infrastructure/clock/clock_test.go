package clock_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mateusxis/cassino/internal/infrastructure/clock"
)

func TestSystemClockReportsUTC(t *testing.T) {
	t.Parallel()

	now := clock.NewSystem().Now()
	assert.Equal(t, time.UTC, now.Location(), "the backend clock is the authority and always speaks UTC")
	assert.WithinDuration(t, time.Now().UTC(), now, time.Second)
}

func TestFakeClockAdvancesAndSets(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	assert.Equal(t, start, fake.Now())

	fake.Advance(15 * time.Second)
	assert.Equal(t, start.Add(15*time.Second), fake.Now())

	fake.Set(start)
	assert.Equal(t, start, fake.Now())
}

func TestFakeClockNormalizesToUTC(t *testing.T) {
	t.Parallel()

	saoPaulo := time.FixedZone("BRT", -3*60*60)
	local := time.Date(2026, 1, 1, 9, 0, 0, 0, saoPaulo)

	fake := clock.NewFake(local)
	assert.Equal(t, time.UTC, fake.Now().Location())
	assert.True(t, fake.Now().Equal(local))
}
