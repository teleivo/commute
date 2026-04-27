# TODO

* DVVSet: write `Less`/`Equal` (needed for anti-entropy and delta gossip, can defer until then)
* CRDT Map (`map[Key]CRDT`, merge delegates per-key)
* HTTP layer hardening
  * `http.MaxBytesReader` on every handler that calls `io.ReadAll(r.Body)` (incl. `postSet`,
    where `contexts` can be arbitrarily large)
  * cap on max element string length in OR-Set Add/Remove
  * cap on max number of Adds/Removes per request
  * decide and document what valid causal-context base64 / JSON looks like (size, shape, and
    bounds on `C(r)` so a malicious client can't wipe siblings via a bogus-high own-id counter)
* Property tests with [`rapid`](https://github.com/flyingmutant/rapid) (commutativity, associativity, idempotency)

## Phase 3 — Observability

* Prometheus metrics (gossip rounds, messages sent/received, convergence duration)
* Grafana dashboard (per-node value time-series, converged/diverged state)
* Debug endpoints (pause/resume gossip, inject/heal partitions, state dump, peers)

## Optional

* Replace wall clock with hybrid logical clock (HLC) for LWW-Register

## Phase 4 — Delta-state gossip

* Track seen state per peer via dotted version vectors
* Send only deltas since last sync instead of full state
* Periodic full-state anti-entropy for catch-up after missed rounds

## Phase 5 — SWIM membership

* Ping/ack direct failure detection over UDP
* Ping-req indirect probing
* Suspicion and dead/leave states
* Membership events piggybacked on gossip messages
* Fly.io deployment: 3 nodes across regions (can also be done earlier for fun)
