#!/usr/bin/env sh
# Load generator for the commute demo cluster.
#
# Runs inside the Fly 6PN mesh. Discovers commute nodes via DNS
# https://fly.io/docs/networking/private-networking, filters to the regions this generator is
# responsible for, and sends POST increments to each node via vegeta. Re-resolves the node list
# every INTERVAL seconds so newly joined demo nodes are picked up automatically.
#
# Environment variables:
#   APP          Fly app name to query (default: commute)
#   REGIONS      Comma-separated list of Fly region codes this generator owns
#   COUNTER_KEY  Counter key to increment (default: gopher-vs-crab)
#   INCREMENT    Increment value per request (default: 1)
#   RATE            Requests per second across all targets (default: 1000/s)
#   REPORT_EVERY    Report latency stats every N seconds (default: 10s)
#   PROMETHEUS_ADDR Address to expose Prometheus metrics on (default: 0.0.0.0:8880)
#   INTERVAL        Seconds between re-resolution of the node list (default: 10)

set -eu

APP="${APP:-commute}"
REGIONS="${REGIONS:-}"
COUNTER_KEY="${COUNTER_KEY:-gopher-vs-crab}"
INCREMENT="${INCREMENT:-1}"
RATE="${RATE:-1000/s}"
REPORT_EVERY="${REPORT_EVERY:-10s}"
PROMETHEUS_ADDR="${PROMETHEUS_ADDR:-0.0.0.0:8880}"
INTERVAL="${INTERVAL:-10}"

if [ -z "${REGIONS}" ]; then
    echo "error: REGIONS must be set (comma-separated Fly region codes, e.g. ams,fra,lhr)" >&2
    exit 1
fi

echo "load: app=${APP} regions=${REGIONS} key=${COUNTER_KEY} increment=${INCREMENT} rate=${RATE} re-resolve every ${INTERVAL}s"

body=$(mktemp)
targets=$(mktemp)
printf '{"increment": %s}' "${INCREMENT}" > "${body}"

# Resolves vms.<app>.internal TXT record and filters to the given regions.
# Prints one host:port per line.
resolve_nodes() {
    raw=$(dig +short txt "vms.${APP}.internal" | tr -d '"' | tr ',' '\n' | tr ' ' '\n')
    # raw lines alternate: <machine_id> <region> (space-separated pairs in the TXT record)
    # dig returns the whole value as one quoted string, so after tr we get pairs like:
    #   <id> <region> <id> <region> ...
    # Process pairs: read id then region, emit host if region matches.
    echo "${raw}" | awk -v regions="${REGIONS}" -v app="${APP}" '
        NR % 2 == 1 { id = $0; next }
        {
            region = $0
            n = split(regions, arr, ",")
            for (i = 1; i <= n; i++) {
                if (arr[i] == region) {
                    print id ".vm." app ".internal:8080"
                    break
                }
            }
        }
    '
}

last_resolve=0
last_nodes=""
vegeta_pid=""

while true; do
    now=$(date +%s)
    if [ $((now - last_resolve)) -ge "${INTERVAL}" ]; then
        nodes=$(resolve_nodes)
        last_resolve="${now}"
        count=$(echo "${nodes}" | grep -c '.' || true)

        if [ "${nodes}" != "${last_nodes}" ]; then
            echo "load: resolved ${count} node(s) in regions=${REGIONS} (changed, restarting vegeta)"
            last_nodes="${nodes}"

            if [ -n "${vegeta_pid}" ]; then
                kill "${vegeta_pid}" 2>/dev/null || true
                vegeta_pid=""
            fi

            if [ -z "${nodes}" ]; then
                echo "load: no nodes found for regions=${REGIONS}, will retry" >&2
            else
                for node in ${nodes}; do
                    printf 'POST http://%s/counters/%s\nContent-Type: application/json\n@%s\n\n' \
                        "${node}" "${COUNTER_KEY}" "${body}"
                done > "${targets}"

                vegeta attack \
                    --targets="${targets}" \
                    --rate="${RATE}" \
                    --duration=0 \
                    --prometheus-addr="${PROMETHEUS_ADDR}" \
                    | vegeta report --every="${REPORT_EVERY}" &
                vegeta_pid=$!
            fi
        else
            echo "load: resolved ${count} node(s) in regions=${REGIONS} (unchanged)"
        fi
    fi
    sleep 1
done
