# commute

A CRDT-based key-value store inspired by [Riak](https://riak.com/). Built for learning.

Counters and registers follow Shapiro et al.'s CRDT specifications. The OR-Set tracks causality
per element with a Dotted Version Vector Set (DVVSet) instead of unique tags, so replicas can
detect concurrent operations precisely and clients can supply a causal context on writes.

## Features

* PN-Counter (increment and decrement)
* LWW-Register (last-writer-wins)
* OR-Set (observed-remove set with per-element causal contexts, stores strings)
* Delta-state gossip to a random peer on a configurable interval (default 5s)
* SWIM failure detection: dead peers are removed from the gossip pool automatically

## Run

Start a 3-node cluster. Each container listens on `:8080`; the host maps a unique port per node:

| Node   | Host port |
| ------ | --------- |
| node-0 | 8080      |
| node-1 | 8081      |
| node-2 | 8082      |

```sh
docker compose up --build
```

The examples below write on one node and read from another. Each gossip round pushes state to one
random peer, so a specific node may need several rounds before it sees an update. If a read returns
404 or stale state, wait a few seconds and retry.

### Counter

Increment on node-0, then read from node-1 once gossip has propagated:

```sh
curl -X POST localhost:8080/counters/visitors -d '{"increment": 5}'
# wait for gossip, then read from node-1
curl localhost:8081/counters/visitors
```

Decrement:

```sh
curl -X POST localhost:8080/counters/visitors -d '{"decrement": 2}'
```

### Register

```sh
curl -X PUT localhost:8080/registers/config -d '{"value": "dark-mode"}'
# wait for gossip, then read from node-2
curl localhost:8082/registers/config
```

### Set

The set API uses arrays (so you can add or remove multiple elements at once) and Riak-style
opaque causal contexts.

Add elements:

```sh
curl -X POST localhost:8080/sets/fruits -d '{"add": ["apple", "banana"]}'
```

Read the set. The response includes `values` and a `contexts` map with one base64-encoded
context per element:

```sh
# wait for gossip, then read from node-1
curl localhost:8081/sets/fruits
# {
#   "values": ["apple", "banana"],
#   "contexts": {
#     "apple":  "<base64 blob>",
#     "banana": "<base64 blob>"
#   }
# }
```

#### Observed-remove in action

Add `apple` on node-0 and let it propagate, then remove from node-1 with a stale context while
node-2 concurrently re-adds. The remove only drops the dot it observed; the concurrent add
survives, so `apple` remains in the set everywhere after gossip:

```sh
curl -X POST localhost:8080/sets/fruits -d '{"add": ["apple"]}'

# wait for gossip, then node-1 reads and captures the context, and node-2 re-adds.
ctx=$(curl -s localhost:8081/sets/fruits | jq -r '.contexts.apple')
curl -X POST localhost:8082/sets/fruits -d '{"add": ["apple"]}'

# node-1 removes with the stale context. The remove is concurrent with node-2's re-add.
curl -X POST localhost:8081/sets/fruits -d "{\"remove\": [\"apple\"], \"contexts\": {\"apple\": \"$ctx\"}}"

# wait for gossip; apple is still in the set on every node.
curl localhost:8080/sets/fruits
```

## Limitations

* No persistence: state is in memory. A single node that restarts is rehydrated by gossip, but
  if all nodes are down at once the data is lost.
* Static initial membership: peers are discovered at startup via a bootstrap loop that contacts
  configured seed addresses over HTTP using a push/pull exchange: the joining node sends its current
  peer list so the seed learns new members (push), and the seed returns its own list so the joiner
  can discover indirect peers (pull). Seeds that are unreachable are retried with exponential
  backoff and never enter the failure detector, so a cold-starting cluster avoids the race where
  nodes mark each other dead before DNS resolves. SWIM removes dead peers from the gossip pool
  automatically, but a node that leaves cannot rejoin without a restart of the cluster. HTTP is a
  pragmatic choice for the initial join: a pure UDP subprotocol would require retransmission logic
  and message framing for large member lists, and QUIC would address that but adds an external
  dependency. HTTP gives reliable delivery with no extra dependency since the server is already in
  place, and fits the time constraints of this learning project.
* No delta garbage collection: the delta buffer grows unboundedly; it will be garbage collected
  once join/leave is supported, since GC requires knowing which peers have left for good vs. are
  temporarily partitioned.
* Delta-state gossip: deltas are propagated using the delta-interval anti-entropy algorithm
  (Algorithm 2) from Almeida et al.,
  [Delta State Replicated Data Types](https://arxiv.org/abs/1603.01529), which satisfies the
  causal delta-merging condition and is equivalent to a standard state-based CRDT. Enes et al.,
  [Efficient Synchronization of State-based CRDTs](https://arxiv.org/abs/1803.02750), show that
  this classic algorithm can propagate as much redundant state as full state-based synchronization,
  performing no better and incurring unnecessary CPU overhead. Two optimizations address this: BP
  (avoid back-propagating received deltas to their origin) and RR (strip already-seen
  join-irreducible states from received delta-groups using join decomposition). Neither is
  implemented here.
* Trusted network, no Byzantine tolerance: gossip and the HTTP API are unauthenticated and peers
  are assumed to follow the protocol. Anyone who can reach a node can forge contexts or corrupt
  convergence.
* JSON only: all values (register, set elements) and internal gossip messages use JSON. Binary
  values are not supported, and a more efficient wire format (e.g. protobuf) is not used.

## Acknowledgments

* Shapiro et al., [A comprehensive study of Convergent and Commutative Replicated Data
Types](https://inria.hal.science/inria-00555588/document). CRDT specifications this project
implements.
* Almeida et al., [Scalable and Accurate Causality Tracking for Eventually Consistent
Stores](https://inria.hal.science/hal-01287733). DVVSet design used for causality tracking.
* Almeida, Shoker & Baquero, [Delta State Replicated Data Types](https://arxiv.org/abs/1603.01529).
Delta-mutator framework and the delta-interval anti-entropy algorithm (Algorithm 2) used for
delta-state gossip.
* Gonçalves & Almeida,
[Dotted-Version-Vectors](https://github.com/ricardobcl/Dotted-Version-Vectors). Reference Erlang
implementation of DVVSet used as a guide and test source.
* Das, Gupta & Motivala, [SWIM: Scalable Weakly-consistent Infection-style Process Group Membership
Protocol](https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf). Failure detection
protocol implemented for peer liveness monitoring.

## Disclaimer

I wrote this for my personal learning and it is provided as-is without warranty. Feel free to use it!

See [LICENSE](LICENSE) for full license terms.
