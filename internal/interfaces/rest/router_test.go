package rest_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/interfaces/rest"
)

func TestHealthReturnsOKWithNoDependencies(t *testing.T) {
	t.Parallel()

	router := rest.NewRouter(rest.RouterOptions{Version: "test"})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, "test", body["version"])
}

func TestHealthReportsHealthyDependencies(t *testing.T) {
	t.Parallel()

	router := rest.NewRouter(rest.RouterOptions{
		Dependencies: []rest.Dependency{
			{Name: "postgres", Check: func(*http.Request) error { return nil }},
			{Name: "redis", Check: func(*http.Request) error { return nil }},
		},
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status       string            `json:"status"`
		Dependencies map[string]string `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ok", body.Status)
	assert.Equal(t, "ok", body.Dependencies["postgres"])
	assert.Equal(t, "ok", body.Dependencies["redis"])
}

// An unreachable dependency must produce 503 so orchestrators stop routing
// traffic to this instance.
func TestHealthReturns503WhenADependencyIsDown(t *testing.T) {
	t.Parallel()

	router := rest.NewRouter(rest.RouterOptions{
		Dependencies: []rest.Dependency{
			{Name: "postgres", Check: func(*http.Request) error { return nil }},
			{Name: "redis", Check: func(*http.Request) error { return errors.New("connection refused") }},
		},
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body struct {
		Status       string            `json:"status"`
		Dependencies map[string]string `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "degraded", body.Status)
	assert.Equal(t, "ok", body.Dependencies["postgres"])
	assert.Contains(t, body.Dependencies["redis"], "connection refused")
}

func TestUnknownRouteIs404(t *testing.T) {
	t.Parallel()

	router := rest.NewRouter(rest.RouterOptions{})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
