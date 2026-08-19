package rest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/audit"
	"github.com/mateusxis/cassino/internal/interfaces/rest"
)

// failingAuditRepo always errors on Append, to prove a broken audit sink
// never fails the request it is auditing.
type failingAuditRepo struct{ calls int }

func (f *failingAuditRepo) Append(context.Context, *audit.Entry) error {
	f.calls++
	return assert.AnError
}

func TestAuditMiddlewareDoesNotBreakRequestOnAppendFailure(t *testing.T) {
	t.Parallel()

	repo := &failingAuditRepo{}
	clk := fakeClock{now: time.Now()}
	ids := &fakeIDGen{}

	handler := rest.AuditMiddleware(repo, clk, ids, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/whatever", nil))

	assert.Equal(t, http.StatusOK, rec.Code, "the response must be unaffected by an audit write failure")
	assert.Equal(t, 1, repo.calls)
}

func TestAuditMiddlewareIsNoOpWithoutAnAuditRepo(t *testing.T) {
	t.Parallel()

	called := false
	handler := rest.AuditMiddleware(nil, fakeClock{now: time.Now()}, &fakeIDGen{}, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/whatever", nil))
	})
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}
