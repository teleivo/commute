# TODO

* Fly.io deployment: x nodes across regions
  * how could I demo this?
    * I think nodes being able to join would be key
  * should I work on metrics now? before adding more features

* per-round ack channel: a stale ack sitting in the shared buffer causes the real ack to be
  dropped, falling back to indirect probing unnecessarily; a fresh channel per round fixes this

* testing
  * can I add logs back? they did cause trouble with the synctest at some point. Was that due to
  syscalls being involved which interfere with the bubble noticing a durably blocked goroutine?
  Right now all use discard logger which is sad as passing t.Output() is pretty cool and useful
  * e2e style test so things like swim upd event passed to server does not remove member from server
    as it deals with http/tcp layer

* 4.3 of the paper — "Round-Robin Probe Target Selection" for direct pings

* Implement SWIM++ suspicion and refutation (incarnation numbers, Suspect state, alive refutation)
* dynamic join: bootstrap (new node announces itself to at least one known peer) and crash recovery
  gap (peers hold stale ack sequences; need sequence regression detection to fall back to full state);
  also fixes the cold-start race where a peer probed before it is reachable is permanently dropped
  * piggybacking: alive events received via piggybacking are silently dropped for now; revisit when
    SWIM++ adds incarnation numbers and alive refutation

## Phase 3 — Observability

* Prometheus metrics (gossip rounds, messages sent/received, convergence duration)
* Grafana dashboard (per-node value time-series, converged/diverged state)
* Delta accumulation efficiency: `Delta()` clones every ORSet entry when building a peer's
  delta-interval. In the common case each key appears in only one delta entry, so the clone is
  never needed. Measure whether skipping the clone when no collision occurs (defer until a second
  delta for the same key arrives) is worth the added complexity.
* Can maelstrom test my kv? or would I need to conform to its API? and if so how much work is it to
  build my own maelstrom or can it be configured?

### Metrics reference

Ranked from most to least commonly used across papers and Maelstrom challenges:

1. **Transmission bandwidth** (bytes/s or bytes/round) — total bytes sent per gossip cycle;
   primary way to compare delta vs. full-state replication. Almeida 2016 shows delta+BP+RR cuts
   it 18–65% vs. state-based. Maelstrom tracks `msgs-per-op` as a proxy.

2. **Messages per operation** (count) — inter-server messages per client op; Maelstrom reports
   this directly (`msgs-per-op`). Van Renesse uses it to compare reconciliation orderings
   (scuttle-depth vs. scuttle-breadth).

3. **Convergence time / stable latency** (ms or rounds) — time until every node has seen a
   write. Maelstrom calls it `stable-latencies` (p50/p95/p99/max). Birman defines it as
   O(log n) rounds for probabilistic gossip; Haeuppler proves O(D + log² n) deterministically.

4. **Staleness** (s or count of stale values) — how many values lag behind and by how much.
   Van Renesse defines staleness as time since last update per key; useful when toggling gossip
   interval.

5. **Metadata / memory overhead** (bytes per node, or ratio vs. payload) — cost of the causal
   bookkeeping (delta groups, version vectors). Enes 2019 measures 1.1×–3.9× overhead for delta
   groups; directly relevant to our DVVSet and delta-interval accumulation.

6. **CPU overhead** (% or factor vs. baseline) — processing cost of merge/join. Almeida 2016
   reports up to 7.9× for naive classic delta vs. 0.4–5.5× for optimised. Less critical for a
   small cluster but good to have for the full-vs-delta toggle demo.

7. **Gossip rounds to convergence** (count) — discrete rounds until all nodes agree. Haeuppler
   uses this as the primary metric; natural unit for the anti-entropy ticker.

**What to wire up:** bandwidth (bytes sent/received per gossip tick), msgs-per-op, stable
latency histogram, and metadata size. Toggle full vs. delta replication and plot all four to
reproduce Almeida 2016 §6 in miniature.

**Demo — counter race:** split N nodes in half; hammer one half with `inc`, the other with
`dec`. Graph running total per node vs. time — watch them diverge under load then converge
once you stop. Target sum = 0.

## Phase 4 — SWIM membership

* Implement lifeguard extensions from Hashicorp
* Garbage collect deltas acked by all neighbors (needs membership to distinguish "left for good"
  from "temporarily partitioned" before pruning deltas a slow neighbor still needs)

## Later

* HTTP layer hardening
  * return more detailed errors? revisit my error handling in general
  * `http.MaxBytesReader` on every handler that calls `io.ReadAll(r.Body)` (incl. `postSet`,
    where `contexts` can be arbitrarily large)
  * cap on max element string length in OR-Set Add/Remove
  * cap on max number of Adds/Removes per request
  * decide and document what valid causal-context base64 / JSON looks like (size, shape, and
    bounds on `C(r)` so a malicious client can't wipe siblings via a bogus-high own-id counter)
* CRDT Map (`map[Key]CRDT`, merge delegates per-key)
* Property tests with [`rapid`](https://github.com/flyingmutant/rapid) (commutativity, associativity, idempotency)
* Replace wall clock with hybrid logical clock (HLC) for LWW-Register?
* binary encoding like automerge-perf for crdt gossip?
* testing using antithesis
* branching
* design a Zombie game backed by the KV store inspired by tigerbeetle ❤️
  * Debug endpoints (pause/resume gossip, inject/heal partitions, state dump, peers)

## Zombie Game

**Inspiration**: TigerBeetle's browser game runs their VOPR simulator (compiled to WASM) and lets
players inject faults — network partitions, crashes — then watch the cluster recover. The visual
maps directly to what the DB is doing internally.

**Goal**: Do the same for CRDTs — make gossip and eventual consistency *visible* and *playable*.

### Concept

A top-down 2D grid where each cell is a KV node (person). One node gets infected with a vial:
its zombie state propagates to neighbours via gossip. Players can build walls between nodes to
create network partitions, slowing or blocking propagation. Partitioned zombies cannot infect
across a wall until it is removed — at which point diverged state merges and the infection spreads.

### Mechanics

* **Nodes (people)**: placed on a grid, each represents a running KV node reachable via HTTP.
* **Infection**: clicking a node with a vial writes a zombie key via the OR-Set or LWW-Register
  API. Gossip carries the state to neighbours over time.
* **Walls**: player places a wall between two adjacent nodes; the game calls the debug endpoint
  `POST /internal/partition` (to be added) to pause gossip between those two nodes.
* **Convergence indicator**: each node shows a colour that transitions from healthy → infected as
  its local state converges. A node that has not yet received gossip stays partially healthy.
* **Healing**: removing a wall resumes gossip; the game calls `DELETE /internal/partition` and the
  state merges — visually the remaining healthy nodes quickly flip.
* **Win / lose**: the player wins by isolating all zombies behind walls before they infect every
  node; the cluster wins if gossip converges fully before walls are placed.

### CRDTs to use

| Concept | CRDT | Reason |
|---|---|---|
| Zombie state per node | **LWW-Register** `"zombie": bool` | Simple; timestamp determines last write; concurrent writes resolve deterministically |
| Infection event log | **OR-Set** of event tokens | Lets the UI reconstruct who infected whom even after merges; concurrent adds survive removes |
| Vial inventory (how many vials the player holds) | **PN-Counter** | Natural increment/decrement; demonstrates counter semantics |
| Partition state (which walls are up) | **OR-Set** of `"nodeA-nodeB"` strings | Idempotent add/remove; walls survive concurrent edits |

### What it demonstrates

* **Gossip propagation speed**: watch state spread hop by hop.
* **Network partitions**: walls literally cut gossip paths; diverged state is visible.
* **CRDT merge on heal**: when a wall drops, OR-Set and LWW-Register merge instantly and
  correctly without coordination.
* **Delta vs full-state replication**: a toggle can switch modes and the player sees convergence
  speed change (ties into the Prometheus/Grafana metrics goal).

### Implementation sketch

* **Game client**: Love2D (Lua) or Go with a simple 2D library; either works over plain HTTP.
* **API calls**: game reads node state by polling `GET /registers/{key}` and `GET /sets/{key}`;
  writes infection via `PUT /registers/zombie` or `POST /sets/infected`.
* **Debug endpoints needed** (Phase 3 TODO): pause/resume gossip per peer pair to simulate walls;
  state dump for the convergence indicator.
* **No game server needed**: the KV nodes *are* the game state; the client is a thin visualiser.

