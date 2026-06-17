#!/usr/bin/env sh
# Load generator for the commute demo cluster.
#
# Runs inside the Fly 6PN mesh. Discovers commute nodes via DNS
# https://fly.io/docs/networking/private-networking, filters to the regions this generator is
# responsible for, and sends POST increments to each node concurrently. Re-resolves the node list
# every 10 seconds so newly joined demo nodes are picked up automatically.
#
# Environment variables:
#   APP          Fly app name to query (default: commute)
#   REGIONS      Comma-separated list of Fly region codes this generator owns
#   COUNTER_KEY  Counter key to increment (default: visitors)
#   INCREMENT    Increment value per request (default: 5)
#   INTERVAL     Seconds between re-resolution of the node list (default: 10)

set -eu

APP="${APP:-commute}"
REGIONS="${REGIONS:-}"
COUNTER_KEY="${COUNTER_KEY:-gopher-vs-crab}"
INCREMENT="${INCREMENT:-1}"
INTERVAL="${INTERVAL:-10}"

if [ -z "${REGIONS}" ]; then
    echo "error: REGIONS must be set (comma-separated Fly region codes, e.g. ams,fra,lhr)" >&2
    exit 1
fi

echo "load: app=${APP} regions=${REGIONS} key=${COUNTER_KEY} increment=${INCREMENT} re-resolve every ${INTERVAL}s"

# Resolves vms.<app>.internal TXT record and filters to the given regions.
# Prints one URL per line.
resolve_nodes() {
    raw=$(dig +short txt "vms.${APP}.internal" | tr -d '"' | tr ',' '\n' | tr ' ' '\n')
    # raw lines alternate: <machine_id> <region> (space-separated pairs in the TXT record)
    # dig returns the whole value as one quoted string, so after tr we get pairs like:
    #   <id> <region> <id> <region> ...
    # Process pairs: read id then region, emit URL if region matches.
    echo "${raw}" | awk -v regions="${REGIONS}" -v app="${APP}" '
        NR % 2 == 1 { id = $0; next }
        {
            region = $0
            n = split(regions, arr, ",")
            for (i = 1; i <= n; i++) {
                if (arr[i] == region) {
                    print "http://" id ".vm." app ".internal:8080"
                    break
                }
            }
        }
    '
}

send_increments() {
    nodes="${1}"
    if [ -z "${nodes}" ]; then
        echo "load: no nodes found for regions=${REGIONS}, will retry" >&2
        return
    fi
    for node in ${nodes}; do
        curl --silent --output /dev/null --fail-with-body \
            --header "Content-Type: application/json" \
            --data "{\"increment\": ${INCREMENT}}" \
            "${node}/counters/${COUNTER_KEY}" &
    done
    wait
}

nodes=""
last_resolve=0

while true; do
    now=$(date +%s)
    if [ $((now - last_resolve)) -ge "${INTERVAL}" ]; then
        nodes=$(resolve_nodes)
        last_resolve="${now}"
        count=$(echo "${nodes}" | grep -c "http" || true)
        echo "load: resolved ${count} node(s) in regions=${REGIONS}"
    fi
    send_increments "${nodes}"
done
