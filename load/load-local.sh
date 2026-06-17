#!/usr/bin/env sh
# Load generator for the local docker-compose setup.
#
# Sends POST increments to all commute nodes via vegeta at a controlled rate.
#
# Environment variables:
#   NODES        Space-separated list of node host:port (default: node-0:8080 node-1:8080 node-2:8080)
#   COUNTER_KEY  Counter key to increment (default: gopher-vs-crab)
#   INCREMENT    Increment value per request (default: 1)
#   RATE         Requests per second across all targets (default: 10/s)
#   REPORT_EVERY Report latency stats every N seconds (default: 10s)

set -eu

NODES="${NODES:-node-0:8080 node-1:8080 node-2:8080}"
COUNTER_KEY="${COUNTER_KEY:-gopher-vs-crab}"
INCREMENT="${INCREMENT:-1}"
RATE="${RATE:-10/s}"
REPORT_EVERY="${REPORT_EVERY:-10s}"

body=$(mktemp)
targets=$(mktemp)

printf '{"increment": %s}' "${INCREMENT}" > "${body}"

for node in ${NODES}; do
    printf 'POST http://%s/counters/%s\nContent-Type: application/json\n@%s\n\n' \
        "${node}" "${COUNTER_KEY}" "${body}"
done > "${targets}"

echo "load: nodes=${NODES} key=${COUNTER_KEY} increment=${INCREMENT} rate=${RATE}"

vegeta attack \
    --targets="${targets}" \
    --rate="${RATE}" \
    --duration=0 \
    | vegeta report --every="${REPORT_EVERY}"
