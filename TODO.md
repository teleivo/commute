# TODO

* LWW-Register (inject Clock interface)
* OR-Set (causal context, dotted version vectors)
* CRDT Map (map[Key]CRDT, merge delegates per-key)
* Property tests with `rapid` (commutativity, associativity, idempotency)

## Phase 3 — Observability

* Prometheus metrics (gossip rounds, messages sent/received, convergence duration)
* Grafana dashboard (per-node value time-series, converged/diverged state)
* Debug endpoints (pause/resume gossip, inject/heal partitions, state dump, peers)

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
