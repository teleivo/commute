# TODO

* what load can the kv-store handle well?
  * locally
  * on fly

* wire up metrics on fly.io managed prometheus/grafana
  * how to configure grafana dashboards on fly?
  * do I then need to adapt my local setup?

* Pre-create 15 suspended demo nodes (one per non-base Fly region) each with `CO_SEED_IDS`
  pointing at the base three (node-0 ams, node-1 fra, node-2 lhr). Wake any subset during the
  demo to show a node joining the cluster live.

* fine-tune gossip interval (default 5s), scrape interval (1s), and SWIM protocol period for the
  Fly.io demo — slow enough to see divergence, fast enough to not bore the audience

* start fresh on fly.io and go through all steps
  * document
  * twice

nice to have
* let a new node join?
* show divergence across nodes: deploy Prometheus inside the Fly 6PN mesh so it can scrape all
  nodes directly, then proxy only Prometheus (9090:9090) locally — one tunnel, full multi-node
  view, one panel per node in Grafana

## Demo

### Demo flow

1. `./fly.sh start` — wake the 3 base nodes
2. `fly machine start <demo-node>` — show a new region joining (bootstrap loop, SWIM membership)
3. `fly proxy 8080:8080 --app commute --select` — proxy ams node for Prometheus scrape
4. Open Grafana, start load generator machine
5. Watch counter on ams diverge from the true total as gossip lags
6. Stop load generator — watch convergence on the Grafana panel
7. `./fly.sh pause` + suspend demo node when done

## SWIM

* Rethink ports and the swim/server relationship. Currently `AppPort` in `swim.Config` leaks an
  application-layer concern (the HTTP API port) into the SWIM layer. Consul/memberlist solve this
  cleanly via an opaque `Meta []byte` blob that the application embeds in every membership message
  and decodes itself — SWIM propagates it without interpreting it. Consider replacing `AppPort`
  with a `Meta []byte` field on `Config` and `Peer` so the SWIM layer stays protocol-agnostic.

* 4.3 of the paper — "Round-Robin Probe Target Selection" for direct pings

* per-round ack channel: a stale ack sitting in the shared buffer causes the real ack to be
  dropped, falling back to indirect probing unnecessarily; a fresh channel per round fixes this

* suspicion

* Alive events: two halves of the same feature, both needed together. Not needed for the demo
  since the bootstrap loop already handles peer discovery with a 1s retry interval.
  * Emit: `JoinHandler` should push an Alive event onto the queue so it gets piggybacked on
    outgoing UDP messages and propagates the new peer to the rest of the cluster. Without this,
    other nodes only learn about a joiner via their own bootstrap loops contacting the same seed.
  * Receive: `Listen` currently silently drops all non-Dead piggybacked events (`swim.go:239`).
    It must handle Alive events by adding the peer if not already known and not dead.
  Once both land, `JoinHandler` can also `delete(deadPeers, req.Peer)` before adding so a
  restarted node at the same address can re-join and the rejoin propagates to the cluster. Until
  then, dead peers are blocked at the join handler to avoid re-admission loops where the dead node
  keeps posting, getting re-added, and being declared dead again. Revisit incarnation numbers
  alongside this to prevent a stale alive from overriding a newer dead (SWIM++ §5).
* Crash recovery / rejoin identity: a restarting node that was previously marked dead needs a
  new identity so peers accept it. Corrosion does this by changing the foca identity on restart.
  Revisit alongside sequence regression detection.

* Implement SWIM++ suspicion and refutation (incarnation numbers, Suspect state, alive refutation)

## Testing

* unexport most methods in Server so the API is as clean as Member. I think I just have StartGossip
  and so on exported because it was easier to test at first.
* can I add logs back? they did cause trouble with the synctest at some point. Was that due to
syscalls being involved which interfere with the bubble noticing a durably blocked goroutine?
Right now all use discard logger which is sad as passing t.Output() is pretty cool and useful
* extract common test setup like the fake network?
* e2e style test so things like swim upd event passed to server does not remove member from server
  as it deals with http/tcp layer
* e2e test gap for bootstrap / cold-start: unit tests use a fake in-process network with
  synchronous resolution so they cannot reproduce the Fly.io race where DNS propagates
  asynchronously while multiple binaries start concurrently. The cold-start bug was only caught
  during deploy, not during development, because no test exercises real DNS + real UDP sockets +
  simultaneous process startup. An e2e test that spins up multiple `co server` processes (or
  docker-compose) and asserts they converge without any startup ordering would catch this class
  of bug. The bootstrap loop unit tests can cover retry logic and member propagation in
  isolation, but cannot substitute for this.
* can I use maelstrom?
* how can I use antithesis?

## Phase 3 — Observability

* Prometheus metrics (gossip rounds, messages sent/received, convergence duration)
* Grafana dashboard (per-node value time-series, converged/diverged state)
* Delta accumulation efficiency: `Delta()` clones every ORSet entry when building a peer's
  delta-interval. In the common case each key appears in only one delta entry, so the clone is
  never needed. Measure whether skipping the clone when no collision occurs (defer until a second
  delta for the same key arrives) is worth the added complexity.

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
* branching
* design a Zombie game backed by the KV store inspired by tigerbeetle ❤️
  * Debug endpoints (pause/resume gossip, inject/heal partitions, state dump, peers)

## Ideas

### Zombie Game

**Inspiration**: TigerBeetle's browser game runs their VOPR simulator (compiled to WASM) and lets
players inject faults — network partitions, crashes — then watch the cluster recover. The visual
maps directly to what the DB is doing internally.

**Goal**: Do the same for CRDTs — make gossip and eventual consistency *visible* and *playable*.

#### Concept

A top-down 2D grid where each cell is a KV node (person). One node gets infected with a vial:
its zombie state propagates to neighbours via gossip. Players can build walls between nodes to
create network partitions, slowing or blocking propagation. Partitioned zombies cannot infect
across a wall until it is removed — at which point diverged state merges and the infection spreads.

#### Mechanics

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

#### CRDTs to use

| Concept | CRDT | Reason |
|---|---|---|
| Zombie state per node | **LWW-Register** `"zombie": bool` | Simple; timestamp determines last write; concurrent writes resolve deterministically |
| Infection event log | **OR-Set** of event tokens | Lets the UI reconstruct who infected whom even after merges; concurrent adds survive removes |
| Vial inventory (how many vials the player holds) | **PN-Counter** | Natural increment/decrement; demonstrates counter semantics |
| Partition state (which walls are up) | **OR-Set** of `"nodeA-nodeB"` strings | Idempotent add/remove; walls survive concurrent edits |

#### What it demonstrates

* **Gossip propagation speed**: watch state spread hop by hop.
* **Network partitions**: walls literally cut gossip paths; diverged state is visible.
* **CRDT merge on heal**: when a wall drops, OR-Set and LWW-Register merge instantly and
  correctly without coordination.
* **Delta vs full-state replication**: a toggle can switch modes and the player sees convergence
  speed change (ties into the Prometheus/Grafana metrics goal).

#### Implementation sketch

* **Game client**: Love2D (Lua) or Go with a simple 2D library; either works over plain HTTP.
* **API calls**: game reads node state by polling `GET /registers/{key}` and `GET /sets/{key}`;
  writes infection via `PUT /registers/zombie` or `POST /sets/infected`.
* **Debug endpoints needed** (Phase 3 TODO): pause/resume gossip per peer pair to simulate walls;
  state dump for the convergence indicator.
* **No game server needed**: the KV nodes *are* the game state; the client is a thin visualiser.

