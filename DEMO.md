# Demo

Show a PNCounter diverging and converging across a live multi-region cluster on Fly.io. Nodes in
Amsterdam, Frankfurt, and London each accept increments independently. A local Prometheus + Grafana
stack scrapes one node via `fly proxy` and graphs the per-node GCounter slots as a stacked area
chart — the stack height is the true total. The audience watches the areas shift as gossip
propagates increments across regions, then stabilize once writes stop.

## What is shown

The Grafana dashboard displays `commute_gcounter_node_increments` as a stacked area chart with one
area per node. Each area is that node's own counter slot in the GCounter — the value it alone
increments. Gossip merges foreign slots into every node's view, so scraping any single node
eventually shows the full picture. The stack height at any moment equals `GCounter.Value()`, the
cluster-wide sum.

Divergence is visible when one node's area has not yet received gossip from others: its view of the
total lags. Convergence is visible when all areas stabilize and stop shifting.

## Prerequisites

* `fly` CLI authenticated (`fly auth login`)
* Docker + Docker Compose for the local monitoring stack

## Running the demo

### 1. Deploy or wake the cluster

First deploy:

```sh
./fly.sh deploy
```

Subsequent runs (nodes already exist, just wake them):

```sh
./fly.sh start
```

Check all three are running:

```sh
./fly.sh status
```

The three base nodes are:

| Name   | Region     |
|--------|------------|
| node-0 | ams        |
| node-1 | fra        |
| node-2 | lhr        |

### 2. Start the local monitoring stack

In a separate terminal, proxy the Amsterdam node so Prometheus can scrape it:

```sh
fly proxy 8080:8080 --app commute --bind-addr 0.0.0.0 --select
```

Select `node-0` (ams) when prompted. `--bind-addr 0.0.0.0` is required so the Prometheus container
can reach the proxy via the Docker bridge network.

Then start Prometheus and Grafana:

```sh
docker compose -f docker-compose.metrics.yml -f docker-compose.metrics.fly.yml up
```

Open Grafana at <http://127.0.0.1:3000/d/commute>. The dashboard auto-refreshes every 1s.

### 3. Send increments

Start the load generator machine — it discovers all commute nodes via DNS and fires increments at
each node concurrently:

```sh
./fly-load.sh start
```

Watch the stacked areas grow in Grafana. Stop writes to show convergence:

```sh
./fly-load.sh stop
```

The counter key is `gopher-vs-crab` by default. The load generator re-resolves node addresses every
10 seconds, so any newly joined demo node is picked up automatically.

### 4. Show a new node joining (optional)

Wake a pre-created demo node in a new region:

```sh
fly machine start <demo-node-id> --app commute
```

Within a few seconds SWIM discovers it, the bootstrap loop completes, and a new area appears in the
stacked chart as gossip delivers the counter state to the new node.

### 5. Wind down

```sh
./fly.sh pause
docker compose -f docker-compose.metrics.yml -f docker-compose.metrics.fly.yml down
```

## Local testing (no Fly.io)

Bring up a 3-node local cluster together with the monitoring stack:

```sh
docker compose -f docker-compose.yml -f docker-compose.metrics.yml up --build
```

Send increments to the local nodes:

```sh
curl -X POST localhost:8080/counters/hits -d '{"increment": 5}'
curl -X POST localhost:8081/counters/hits -d '{"increment": 3}'
curl -X POST localhost:8082/counters/hits -d '{"increment": 7}'
```

Open Grafana at <http://127.0.0.1:3000/d/commute>, select key `hits`.

To reset the Prometheus and Grafana state between runs:

```sh
docker compose -f docker-compose.yml -f docker-compose.metrics.yml down -v
```

## Key configuration

| Parameter | Default | Flag |
|-----------|---------|------|
| Gossip interval | 5s | `-gossip-interval` |
| SWIM protocol period | — | `-swim-protocol-period` |
| Prometheus scrape interval | 1s | `prometheus.yml` |
