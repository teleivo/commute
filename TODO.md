# TODO

## Phase 1 — G-Counter

* implement co HTTP server
  * `POST /types/counters/keys/{key}` with `{"increment": N}` body
  * `GET /types/counters/keys/{key}` returning `{"value": N}`
* HTTP integration tests over httptest.Server with real handler, no mocks
  * increment once, increment multiple, fetch empty key, fetch after increments
