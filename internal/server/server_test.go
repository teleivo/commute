package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/assertive/require"
	"github.com/teleivo/commute/internal/server"
)

func TestCounterAPI(t *testing.T) {
	tests := map[string]struct {
		method     string
		path       string
		body       string
		wantStatus int
	}{
		"FetchNonExistent": {
			method:     http.MethodGet,
			path:       "/types/counters/keys/visitors",
			wantStatus: http.StatusNotFound,
		},
		"IncrementByOne": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `{"increment": 1}`,
			wantStatus: http.StatusOK,
		},
		"IncrementByN": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `{"increment": 5}`,
			wantStatus: http.StatusOK,
		},
		"DecrementByOne": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `{"decrement": 1}`,
			wantStatus: http.StatusOK,
		},
		"DecrementByN": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `{"decrement": 3}`,
			wantStatus: http.StatusOK,
		},
		"MissingBody": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
		"InvalidJSON": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
		},
		"BothIncrementAndDecrement": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `{"increment": 5, "decrement": 3}`,
			wantStatus: http.StatusBadRequest,
		},
		"IncrementZero": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `{"increment": 0}`,
			wantStatus: http.StatusBadRequest,
		},
		"DecrementZero": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `{"decrement": 0}`,
			wantStatus: http.StatusBadRequest,
		},
		"NeitherIncrementNorDecrement": {
			method:     http.MethodPost,
			path:       "/types/counters/keys/visitors",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		"MethodNotAllowed": {
			method:     http.MethodDelete,
			path:       "/types/counters/keys/visitors",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(t)

			var req *http.Request
			if tc.body != "" {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			assert.EqualValues(t, rec.Code, tc.wantStatus)
		})
	}
}

func TestCounterIncrementAndFetch(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/types/counters/keys/visitors", `{"increment": 3}`, http.StatusOK)
	post(t, srv, "/types/counters/keys/visitors", `{"increment": 7}`, http.StatusOK)

	rec := get(t, srv, "/types/counters/keys/visitors", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], any(float64(10)))
}

func TestCounterDecrementAndFetch(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/types/counters/keys/visitors", `{"increment": 10}`, http.StatusOK)
	post(t, srv, "/types/counters/keys/visitors", `{"decrement": 3}`, http.StatusOK)

	rec := get(t, srv, "/types/counters/keys/visitors", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], any(float64(7)))
}

func TestCounterDecrementBelowZero(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/types/counters/keys/visitors", `{"increment": 2}`, http.StatusOK)
	post(t, srv, "/types/counters/keys/visitors", `{"decrement": 5}`, http.StatusOK)

	rec := get(t, srv, "/types/counters/keys/visitors", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], any(float64(-3)))
}

func TestCounterSeparateKeys(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/types/counters/keys/a", `{"increment": 2}`, http.StatusOK)
	post(t, srv, "/types/counters/keys/b", `{"increment": 5}`, http.StatusOK)

	recA := get(t, srv, "/types/counters/keys/a", http.StatusOK)
	var gotA map[string]any
	err := json.NewDecoder(recA.Body).Decode(&gotA)
	require.NoError(t, err)
	assert.EqualValues(t, gotA["value"], any(float64(2)))

	recB := get(t, srv, "/types/counters/keys/b", http.StatusOK)
	var gotB map[string]any
	err = json.NewDecoder(recB.Body).Decode(&gotB)
	require.NoError(t, err)
	assert.EqualValues(t, gotB["value"], any(float64(5)))
}

func TestRegisterAPI(t *testing.T) {
	tests := map[string]struct {
		method     string
		path       string
		body       string
		wantStatus int
	}{
		"FetchNonExistent": {
			method:     http.MethodGet,
			path:       "/types/registers/keys/config",
			wantStatus: http.StatusNotFound,
		},
		"SetString": {
			method:     http.MethodPut,
			path:       "/types/registers/keys/config",
			body:       `{"value": "dark-mode"}`,
			wantStatus: http.StatusOK,
		},
		"SetNumber": {
			method:     http.MethodPut,
			path:       "/types/registers/keys/config",
			body:       `{"value": 42}`,
			wantStatus: http.StatusOK,
		},
		"SetObject": {
			method:     http.MethodPut,
			path:       "/types/registers/keys/config",
			body:       `{"value": {"theme": "dark"}}`,
			wantStatus: http.StatusOK,
		},
		"MissingBody": {
			method:     http.MethodPut,
			path:       "/types/registers/keys/config",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
		"InvalidJSON": {
			method:     http.MethodPut,
			path:       "/types/registers/keys/config",
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
		},
		"MissingValue": {
			method:     http.MethodPut,
			path:       "/types/registers/keys/config",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		"MethodNotAllowed": {
			method:     http.MethodDelete,
			path:       "/types/registers/keys/config",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(t)

			var req *http.Request
			if tc.body != "" {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			assert.EqualValues(t, rec.Code, tc.wantStatus)
		})
	}
}

func TestRegisterSetAndFetch(t *testing.T) {
	srv := newTestServer(t)

	put(t, srv, "/types/registers/keys/config", `{"value": "dark-mode"}`, http.StatusOK)

	rec := get(t, srv, "/types/registers/keys/config", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], "dark-mode")
}

func TestRegisterOverwrite(t *testing.T) {
	srv := newTestServer(t)

	put(t, srv, "/types/registers/keys/config", `{"value": "v1"}`, http.StatusOK)
	put(t, srv, "/types/registers/keys/config", `{"value": "v2"}`, http.StatusOK)

	rec := get(t, srv, "/types/registers/keys/config", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], "v2")
}

func TestRegisterSeparateKeys(t *testing.T) {
	srv := newTestServer(t)

	put(t, srv, "/types/registers/keys/a", `{"value": "alpha"}`, http.StatusOK)
	put(t, srv, "/types/registers/keys/b", `{"value": "beta"}`, http.StatusOK)

	recA := get(t, srv, "/types/registers/keys/a", http.StatusOK)
	var gotA map[string]any
	err := json.NewDecoder(recA.Body).Decode(&gotA)
	require.NoError(t, err)
	assert.EqualValues(t, gotA["value"], "alpha")

	recB := get(t, srv, "/types/registers/keys/b", http.StatusOK)
	var gotB map[string]any
	err = json.NewDecoder(recB.Body).Decode(&gotB)
	require.NoError(t, err)
	assert.EqualValues(t, gotB["value"], "beta")
}

func newTestServer(t *testing.T) *server.Server {
	t.Helper()
	srv, err := server.New(server.Config{
		NodeID:         "test-node",
		Peers:          "127.0.0.1:9999",
		GossipInterval: 1 * time.Second,
		Stderr:         io.Discard,
	})
	require.NoError(t, err)
	return srv
}

func post(t *testing.T, h *server.Server, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.EqualValues(t, rec.Code, wantStatus)
	return rec
}

func put(t *testing.T, h *server.Server, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.EqualValues(t, rec.Code, wantStatus)
	return rec
}

func get(t *testing.T, h *server.Server, path string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.EqualValues(t, rec.Code, wantStatus)
	return rec
}
