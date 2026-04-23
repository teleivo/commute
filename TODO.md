# TODO

* OR-Set: replace UUIDs with dotted version vectors, add client-side causal context to HTTP API
  * fix ORSet with DVVSet: go through all test cases
  * HTTP GET: return the element's opaque context alongside the value, i.e. base64(json(dvvset.Join()))
    in a `"context"` JSON field (Riak-style X-Riak-Vclock equivalent)
  * HTTP PUT: accept `"context"` from the request body, decode back to a VV, pass to OR-Set's
    Add/Remove which call DVVSet.Update(vv, op)
  * update readme and example
    * remove duplicate Shapiro citation (opening + Acknowledgments)
    * reword opening so it reflects that OR-Set follows the DVVSet paper, not Shapiro Spec 15
    * map node index to port (e.g. "node 0 is :8080, node 1 is :8081, node 2 is :8082")
    * mention gossip interval / add a `sleep` between curl write and curl read so the example
      doesn't race
    * optional: demonstrate concurrent add+remove converging to add (showcases observed-remove)
  * final rewview then merge to main

  * write less/equal (needed for anti-entropy, can defer until then)

* CRDT Map (map[Key]CRDT, merge delegates per-key)
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
