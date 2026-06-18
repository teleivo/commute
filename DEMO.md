# Demo

Show a PNCounter diverging and converging across a live multi-region cluster on Fly.io. Nodes in
Amsterdam, Frankfurt, and London each accept increments independently. Fly.io's managed Prometheus
scrapes all nodes automatically and Grafana at fly-metrics.net graphs the per-node GCounter slots
as a stacked area chart — the stack height is the true total. The audience watches the areas shift
as gossip propagates increments across regions, then stabilize once writes stop.

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

| Name   | Region |
|--------|--------|
| node-0 | ams    |
| node-1 | fra    |
| node-2 | lhr    |

### 2. Start the load generator

```sh
./fly-load.sh start
```

The load generator discovers all commute nodes via DNS and fires increments at each node via
vegeta. To override the default rate of 1000/s:

```sh
RATE=2000/s ./fly-load.sh deploy
```

### 3. Watch in Grafana

Open Grafana at <https://fly-metrics.net>. The dashboard auto-refreshes every 15s (Fly's scrape
interval).

Watch the stacked areas grow. Stop writes to show convergence:

```sh
./fly-load.sh stop
```

### 4. Show a new node joining (optional)

Wake a pre-created demo node in a new region:

```sh
fly machine start <demo-node-id> --app commute
```

Within a few seconds SWIM discovers it, the bootstrap loop completes, and a new area appears in the
stacked chart as gossip delivers the counter state to the new node.

### 5. Wind down

```sh
./fly-load.sh stop
./fly.sh pause
fly machine suspend <demo-node-id>
```

## Local testing (no Fly.io)

Bring up a 3-node local cluster together with the monitoring stack:

```sh
docker compose -f docker-compose.yml -f docker-compose.metrics.yml up --build
```

The load generator starts automatically at 1000/s. To override:

```sh
RATE=5000/s docker compose -f docker-compose.yml -f docker-compose.metrics.yml up --build
```

Open Grafana at <http://127.0.0.1:3000/d/commute>.

To reset Prometheus and Grafana state between runs:

```sh
docker compose -f docker-compose.yml -f docker-compose.metrics.yml down --volumes
```

## Key configuration

| Parameter                  | Default | Where              |
|----------------------------|---------|--------------------|
| Load rate                  | 1000/s  | `RATE` env var     |
| Gossip interval            | 5s      | `-gossip-interval` |
| Prometheus scrape interval | 15s     | Fly.io managed     |
