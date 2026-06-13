# Fly.io Deployment

Runs a 3-node commute cluster across three European regions for on-demand demo and testing. Nodes
gossip state over a private IPv6 mesh with no public ports exposed. Stop machines when done. State
is in-memory only so nothing is lost that wasn't already ephemeral.

## What you get

* 3 nodes in Amsterdam (`node-0`), Frankfurt (`node-1`), London (`node-2`)
* CRDT key-value API (HTTP) and SWIM failure detection (UDP) over private 6PN networking
* Real WAN latency between regions
* Billing only while running (~$0.50/month at 2h/day)

## Prerequisites

* [flyctl](https://fly.io/docs/hands-on/install-flyctl/) installed and authenticated
* App created: `fly apps create commute`

## Managing the cluster

```sh
./fly.sh deploy    # create or update all machines to the latest image, then start them
./fly.sh start     # start all stopped machines
./fly.sh stop      # stop all running machines
./fly.sh status    # list machines and their current state
```

`deploy` is safe to re-run after code changes. It creates machines that do not exist yet and
updates existing ones to the latest image. Three machines:

| Machine | Region | Peers |
|---------|--------|-------|
| node-0 | ams | node-1, node-2 |
| node-1 | fra | node-0, node-2 |
| node-2 | lhr | node-0, node-1 |

Use `fly machine suspend` instead of `stop` for faster resume (milliseconds instead of seconds).

## Access the API

All traffic is private. Proxy a node's HTTP API to your local machine via WireGuard:

```sh
fly proxy 8080:8080 --select
```

Then use the API exactly as in the [docker-compose examples](README.md):

```sh
curl -X POST localhost:8080/counters/visitors -d '{"increment": 5}'
curl localhost:8080/counters/visitors
```

## How it works

Fly.io does not support CLI arguments. `fly-init.sh` runs as the entrypoint (via `Dockerfile.fly`)
and maps Fly environment variables to `co server` flags:

* `NODE_NAME` is set by `fly.sh` and becomes `--node-id` and `--advertise-addr`. Fly does not
  inject the machine name, so it must be passed explicitly.
* `PEERS` is set by `fly.sh` as a comma-separated list of peer names and gets expanded to
  `<name>.vm.commute.internal:<port>` for both HTTP (`--peers`) and SWIM (`--swim-peers`)
* `DEBUG=1` enables debug logging

Nodes communicate over **6PN** (Fly's IPv6 WireGuard mesh). Each machine is reachable at
`<name>.vm.commute.internal`. DNS names are stable across stop/start but reset on destroy/recreate.
