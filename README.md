# commute

A CRDT-based key-value store inspired by [Riak](https://riak.com/). Built for learning.

Counters and registers follow Shapiro et al.'s CRDT specifications. The OR-Set tracks causality
per element with a Dotted Version Vector Set (DVVSet) instead of unique tags, so replicas can
detect concurrent operations precisely and clients can supply a causal context on writes.

## Features

* PN-Counter (increment and decrement)
* LWW-Register (last-writer-wins, stores any JSON value)
* OR-Set (observed-remove set with per-element causal contexts, stores strings)
* Full-state gossip to a random peer on a configurable interval (default 5s)

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

The examples below write on one node and read from another. Allow at least one gossip round (5s
by default) for state to propagate before reading; otherwise the second node may still return
404 or stale state. Gossip pushes state to one random peer per round, so a specific node may
need several rounds before it sees an update; rerun the read step if it returns stale state.

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

Remove an element. Echo back the contexts you got from the previous read so the server knows
which adds your remove observed; concurrent adds from other replicas are not affected:

```sh
curl -X POST localhost:8081/sets/fruits -d '{
  "remove": ["apple"],
  "contexts": {"apple": "<base64 blob from the previous read>"}
}'
```

A POST also returns the same `values` + `contexts` shape, so a client can chain writes without
an extra GET.

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

* Static membership: peers are configured at startup, no dynamic join/leave
* Full-state gossip: every round sends the entire store, no delta optimization
* No persistence: state is in memory. A single node that restarts is rehydrated by gossip, but
  if all nodes are down at once the data is lost.
* Trusted network, no Byzantine tolerance: gossip and the HTTP API are unauthenticated and peers
  are assumed to follow the protocol. Anyone who can reach a node can forge contexts or corrupt
  convergence.

## Acknowledgments

* Shapiro et al., [A comprehensive study of Convergent and Commutative Replicated Data Types](https://inria.hal.science/inria-00555588/document). CRDT specifications this project implements.
* Almeida et al., [Scalable and Accurate Causality Tracking for Eventually Consistent Stores](https://inria.hal.science/hal-01287733). DVVSet design used for causality tracking.
* Gonçalves & Almeida, [Dotted-Version-Vectors](https://github.com/ricardobcl/Dotted-Version-Vectors). Reference Erlang implementation of DVVSet used as a guide and test source.

## Disclaimer

I wrote this for my personal learning and it is provided as-is without warranty. Feel free to use it!

See [LICENSE](LICENSE) for full license terms.
