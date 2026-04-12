# commute

A CRDT-based key-value store inspired by [Riak](https://riak.com/). Built for learning.

Based on Shapiro et al.,
[A comprehensive study of Convergent and Commutative Replicated Data Types](https://inria.hal.science/inria-00555588/document).

## Features

* PN-Counter (increment and decrement)
* LWW-Register (last-writer-wins, stores any JSON value)
* OR-Set (observed-remove set, stores unique strings)
* Full-state gossip to a random peer on a configurable interval

## Run

Start a 3-node cluster:

```sh
docker compose up --build
```

Increment a counter on node 0:

```sh
curl -X POST localhost:8080/counters/visitors -d '{"increment": 5}'
```

Read it back from node 1 (after gossip converges):

```sh
curl localhost:8081/counters/visitors
```

Decrement:

```sh
curl -X POST localhost:8080/counters/visitors -d '{"decrement": 2}'
```

Set a register on node 0:

```sh
curl -X PUT localhost:8080/registers/config -d '{"value": "dark-mode"}'
```

Read it from node 2:

```sh
curl localhost:8082/registers/config
```

Add to a set on node 0:

```sh
curl -X POST localhost:8080/sets/fruits -d '{"add": "apple"}'
curl -X POST localhost:8080/sets/fruits -d '{"add": "banana"}'
```

Read the set from node 1:

```sh
curl localhost:8081/sets/fruits
```

Remove from the set:

```sh
curl -X POST localhost:8080/sets/fruits -d '{"remove": "apple"}'
```

## Limitations

* Static membership: peers are configured at startup, no dynamic join/leave
* Full-state gossip: every round sends the entire store, no delta optimization
* No persistence: all state is in memory and lost on restart

## Disclaimer

I wrote this for my personal learning and it is provided as-is without warranty. Feel free to use it!

See [LICENSE](LICENSE) for full license terms.
