package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
			path:       "/counters/visitors",
			wantStatus: http.StatusNotFound,
		},
		"IncrementByOne": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `{"increment": 1}`,
			wantStatus: http.StatusOK,
		},
		"IncrementByN": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `{"increment": 5}`,
			wantStatus: http.StatusOK,
		},
		"DecrementByOne": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `{"decrement": 1}`,
			wantStatus: http.StatusOK,
		},
		"DecrementByN": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `{"decrement": 3}`,
			wantStatus: http.StatusOK,
		},
		"MissingBody": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
		"InvalidJSON": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
		},
		"BothIncrementAndDecrement": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `{"increment": 5, "decrement": 3}`,
			wantStatus: http.StatusBadRequest,
		},
		"IncrementZero": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `{"increment": 0}`,
			wantStatus: http.StatusBadRequest,
		},
		"DecrementZero": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `{"decrement": 0}`,
			wantStatus: http.StatusBadRequest,
		},
		"NeitherIncrementNorDecrement": {
			method:     http.MethodPost,
			path:       "/counters/visitors",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		"MethodNotAllowed": {
			method:     http.MethodDelete,
			path:       "/counters/visitors",
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

	post(t, srv, "/counters/visitors", `{"increment": 3}`, http.StatusOK)
	post(t, srv, "/counters/visitors", `{"increment": 7}`, http.StatusOK)

	rec := get(t, srv, "/counters/visitors", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], any(float64(10)))
}

func TestCounterDecrementAndFetch(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/counters/visitors", `{"increment": 10}`, http.StatusOK)
	post(t, srv, "/counters/visitors", `{"decrement": 3}`, http.StatusOK)

	rec := get(t, srv, "/counters/visitors", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], any(float64(7)))
}

func TestCounterDecrementBelowZero(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/counters/visitors", `{"increment": 2}`, http.StatusOK)
	post(t, srv, "/counters/visitors", `{"decrement": 5}`, http.StatusOK)

	rec := get(t, srv, "/counters/visitors", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], any(float64(-3)))
}

func TestCounterSeparateKeys(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/counters/a", `{"increment": 2}`, http.StatusOK)
	post(t, srv, "/counters/b", `{"increment": 5}`, http.StatusOK)

	recA := get(t, srv, "/counters/a", http.StatusOK)
	var gotA map[string]any
	err := json.NewDecoder(recA.Body).Decode(&gotA)
	require.NoError(t, err)
	assert.EqualValues(t, gotA["value"], any(float64(2)))

	recB := get(t, srv, "/counters/b", http.StatusOK)
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
			path:       "/registers/config",
			wantStatus: http.StatusNotFound,
		},
		"SetString": {
			method:     http.MethodPut,
			path:       "/registers/config",
			body:       `{"value": "dark-mode"}`,
			wantStatus: http.StatusOK,
		},
		"SetNumber": {
			method:     http.MethodPut,
			path:       "/registers/config",
			body:       `{"value": 42}`,
			wantStatus: http.StatusOK,
		},
		"SetObject": {
			method:     http.MethodPut,
			path:       "/registers/config",
			body:       `{"value": {"theme": "dark"}}`,
			wantStatus: http.StatusOK,
		},
		"MissingBody": {
			method:     http.MethodPut,
			path:       "/registers/config",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
		"InvalidJSON": {
			method:     http.MethodPut,
			path:       "/registers/config",
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
		},
		"MissingValue": {
			method:     http.MethodPut,
			path:       "/registers/config",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		"MethodNotAllowed": {
			method:     http.MethodDelete,
			path:       "/registers/config",
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

	put(t, srv, "/registers/config", `{"value": "dark-mode"}`, http.StatusOK)

	rec := get(t, srv, "/registers/config", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], "dark-mode")
}

func TestRegisterOverwrite(t *testing.T) {
	srv := newTestServer(t)

	put(t, srv, "/registers/config", `{"value": "v1"}`, http.StatusOK)
	put(t, srv, "/registers/config", `{"value": "v2"}`, http.StatusOK)

	rec := get(t, srv, "/registers/config", http.StatusOK)

	var got map[string]any
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got["value"], "v2")
}

func TestRegisterSeparateKeys(t *testing.T) {
	srv := newTestServer(t)

	put(t, srv, "/registers/a", `{"value": "alpha"}`, http.StatusOK)
	put(t, srv, "/registers/b", `{"value": "beta"}`, http.StatusOK)

	recA := get(t, srv, "/registers/a", http.StatusOK)
	var gotA map[string]any
	err := json.NewDecoder(recA.Body).Decode(&gotA)
	require.NoError(t, err)
	assert.EqualValues(t, gotA["value"], "alpha")

	recB := get(t, srv, "/registers/b", http.StatusOK)
	var gotB map[string]any
	err = json.NewDecoder(recB.Body).Decode(&gotB)
	require.NoError(t, err)
	assert.EqualValues(t, gotB["value"], "beta")
}

func TestSetAPI(t *testing.T) {
	tests := map[string]struct {
		method     string
		path       string
		body       string
		wantStatus int
	}{
		"FetchNonExistent": {
			method:     http.MethodGet,
			path:       "/sets/fruits",
			wantStatus: http.StatusNotFound,
		},
		"AddString": {
			method:     http.MethodPost,
			path:       "/sets/fruits",
			body:       `{"add": "apple"}`,
			wantStatus: http.StatusOK,
		},
		"RemoveString": {
			method:     http.MethodPost,
			path:       "/sets/fruits",
			body:       `{"remove": "apple"}`,
			wantStatus: http.StatusOK,
		},
		"MissingBody": {
			method:     http.MethodPost,
			path:       "/sets/fruits",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
		"InvalidJSON": {
			method:     http.MethodPost,
			path:       "/sets/fruits",
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
		},
		"NeitherAddNorRemove": {
			method:     http.MethodPost,
			path:       "/sets/fruits",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		"BothAddAndRemove": {
			method:     http.MethodPost,
			path:       "/sets/fruits",
			body:       `{"add": "apple", "remove": "banana"}`,
			wantStatus: http.StatusOK,
		},
		"AddEmptyStringRejected": {
			method:     http.MethodPost,
			path:       "/sets/fruits",
			body:       `{"add": ""}`,
			wantStatus: http.StatusBadRequest,
		},
		"RemoveEmptyStringRejected": {
			method:     http.MethodPost,
			path:       "/sets/fruits",
			body:       `{"remove": ""}`,
			wantStatus: http.StatusBadRequest,
		},
		"MethodNotAllowed": {
			method:     http.MethodDelete,
			path:       "/sets/fruits",
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

func TestSetAddAndFetch(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)
	post(t, srv, "/sets/fruits", `{"add": "banana"}`, http.StatusOK)

	resp := getSet(t, srv, "fruits")

	slices.Sort(resp.Value)
	assert.EqualValues(t, resp.Value, []string{"apple", "banana"})
}

func TestSetAddDuplicate(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)
	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)

	resp := getSet(t, srv, "fruits")

	assert.EqualValues(t, resp.Value, []string{"apple"})
}

func TestSetRemove(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)
	post(t, srv, "/sets/fruits", `{"add": "banana"}`, http.StatusOK)

	post(t, srv, "/sets/fruits", `{"remove": "apple"}`, http.StatusOK)

	resp := getSet(t, srv, "fruits")
	assert.EqualValues(t, resp.Value, []string{"banana"})
}

func TestSetRemoveAllElements(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)

	post(t, srv, "/sets/fruits", `{"remove": "apple"}`, http.StatusOK)

	// Set exists but is empty.
	rec := get(t, srv, "/sets/fruits", http.StatusOK)
	var got setResponse
	err := json.NewDecoder(rec.Body).Decode(&got)
	require.NoError(t, err)
	assert.EqualValues(t, got.Value, []string{})
}

func TestSetRemoveNonExistentElement(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)

	// Remove an element that doesn't exist in the set.
	post(t, srv, "/sets/fruits", `{"remove": "cherry"}`, http.StatusOK)

	// The set should still contain apple.
	resp := getSet(t, srv, "fruits")
	assert.EqualValues(t, resp.Value, []string{"apple"})
}

func TestSetAddAndRemoveDifferentElements(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)
	post(t, srv, "/sets/fruits", `{"add": "banana"}`, http.StatusOK)

	// Remove banana and add cherry in one request.
	post(t, srv, "/sets/fruits", `{"add": "cherry", "remove": "banana"}`, http.StatusOK)

	resp := getSet(t, srv, "fruits")

	slices.Sort(resp.Value)
	assert.EqualValues(t, resp.Value, []string{"apple", "cherry"})
}

func TestSetAddAndRemoveSameElement(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)

	// Remove and re-add apple in one request. Remove is applied
	// first, then add, so apple should be present with a fresh tag.
	post(t, srv, "/sets/fruits", `{"add": "apple", "remove": "apple"}`, http.StatusOK)

	resp := getSet(t, srv, "fruits")

	assert.EqualValues(t, resp.Value, []string{"apple"})
}

func TestSetSeparateKeys(t *testing.T) {
	srv := newTestServer(t)

	post(t, srv, "/sets/fruits", `{"add": "apple"}`, http.StatusOK)
	post(t, srv, "/sets/colors", `{"add": "red"}`, http.StatusOK)

	respFruits := getSet(t, srv, "fruits")
	assert.EqualValues(t, respFruits.Value, []string{"apple"})

	respColors := getSet(t, srv, "colors")
	assert.EqualValues(t, respColors.Value, []string{"red"})
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

type setResponse struct {
	Value []string `json:"value"`
}

func getSet(t *testing.T, h *server.Server, key string) setResponse {
	t.Helper()
	rec := get(t, h, "/sets/"+key, http.StatusOK)
	var resp setResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	return resp
}
