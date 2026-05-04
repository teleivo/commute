# TODO

* Delta-state CRDTs (paper "Delta State Replicated Data Types", Algorithm 2)
  * Convert mutators to delta-mutator form (option B): pure functions of state that return a delta
    in the same lattice; store calls `Merge(delta)` into both `X_i` and `D_i`
  * GCounter, PNCounter, LWWRegister, ORSet (DVVSet-based)
  * Store: per-key delta buffer `D_i`, sequence counter `c_i`, ack map `A_i`, neighbor set
  * Anti-entropy: periodic ship of delta-interval since `A_i(j)`, full-state fallback when
    `D_i = {}` or `min dom D_i > A_i(j)` (also covers post-restart with volatile state)
  * No durable state (option 3): all of `X_i`, `c_i`, `D_i`, `A_i` volatile; restart = fresh node
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

## Phase 4 — SWIM membership

* Ping/ack direct failure detection over UDP
* Ping-req indirect probing
* Suspicion and dead/leave states
* Membership events piggybacked on gossip messages
* Garbage collect deltas acked by all neighbors (needs membership to distinguish "left for good"
  from "temporarily partitioned" before pruning deltas a slow neighbor still needs)
* Fly.io deployment: 3 nodes across regions (can also be done earlier for fun)
## Optional

* Replace wall clock with hybrid logical clock (HLC) for LWW-Register
* binary encoding like automerge-perf
* branching

