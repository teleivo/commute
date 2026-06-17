#!/usr/bin/env sh
# Load generator for the local docker-compose setup.
#
# Sends POST increments to all commute nodes concurrently in a tight loop.
#
# Environment variables:
#   NODES        Space-separated list of node URLs (default: node-0:8080 node-1:8080 node-2:8080)
#   COUNTER_KEY  Counter key to increment (default: gopher-vs-crab)
#   INCREMENT    Increment value per request (default: 1)

set -eu

NODES="${NODES:-node-0:8080 node-1:8080 node-2:8080}"
COUNTER_KEY="${COUNTER_KEY:-gopher-vs-crab}"
INCREMENT="${INCREMENT:-1}"

echo "load: nodes=${NODES} key=${COUNTER_KEY} increment=${INCREMENT}"

while true; do
    for node in ${NODES}; do
        curl --silent --output /dev/null --fail-with-body \
            --header "Content-Type: application/json" \
            --data "{\"increment\": ${INCREMENT}}" \
            "http://${node}/counters/${COUNTER_KEY}" &
    done
    wait
done
