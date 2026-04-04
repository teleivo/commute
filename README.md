# commute

A CRDT-based key-value store inspired by [Riak](https://riak.com/). Built for learning.

Based on Shapiro et al.,
[A comprehensive study of Convergent and Commutative Replicated Data Types](https://inria.hal.science/inria-00555588/document).

## Features

* PN-Counter (increment and decrement)
* Full-state gossip to a random peer on a configurable interval

## Run

Start a 3-node cluster:

```sh
docker compose up --build
```

Increment a counter on node 0:

```sh
curl -X POST localhost:8080/types/counters/keys/visitors -d '{"increment": 5}'
```

Read it back from node 1 (after gossip converges):

```sh
curl localhost:8081/types/counters/keys/visitors
```

Decrement:

```sh
curl -X POST localhost:8080/types/counters/keys/visitors -d '{"decrement": 2}'
```

## Limitations

* Static membership: peers are configured at startup, no dynamic join/leave
* Full-state gossip: every round sends the entire store, no delta optimization
* No persistence: all state is in memory and lost on restart

## Disclaimer

I wrote this for my personal learning and it is provided as-is without warranty. Feel free to use it!

See [LICENSE](LICENSE) for full license terms.
