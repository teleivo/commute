#!/usr/bin/env sh
# Fly.io-specific entrypoint wrapper (see Dockerfile.fly). Maps Fly.io
# environment variables to co server flags. Fly.io does not support CLI
# arguments, so this script reads env vars and builds the flag list before
# exec-ing the binary.
#
# Required env vars (set via --env on fly machine run):
#   NODE_NAME        this node's name, e.g. node-0; used as node-id and DNS label
#   PEERS            comma-separated peer machine names, e.g. node-1,node-2
#
# Injected automatically by Fly.io:
#   FLY_APP_NAME     app name, used to build *.vm.<app>.internal DNS names

set -eu

: "${NODE_NAME:?NODE_NAME is required}"
: "${PEERS:?PEERS is required}"
: "${FLY_APP_NAME:?FLY_APP_NAME is required}"

HTTP_PORT="${HTTP_PORT:-8080}"
SWIM_PORT="${SWIM_PORT:-7946}"

# Build peer lists from bare names to fully-qualified internal DNS addresses.
http_peers=""
swim_peers=""
peer_hosts=""
for name in $(echo "$PEERS" | tr ',' ' '); do
    host="${name}.vm.${FLY_APP_NAME}.internal"
    http_peers="${http_peers:+${http_peers},}${host}:${HTTP_PORT}"
    swim_peers="${swim_peers:+${swim_peers},}${host}:${SWIM_PORT}"
    peer_hosts="${peer_hosts} ${host}"
done

# Wait until all peer hostnames resolve. On a cold start peers register their
# DNS entries only after they are up, so we retry until all are reachable.
for host in ${peer_hosts}; do
    echo "waiting for ${host} to resolve..."
    until nslookup "${host}" >/dev/null 2>&1; do
        sleep 2
    done
    echo "${host} resolved"
done

advertise_addr="${NODE_NAME}.vm.${FLY_APP_NAME}.internal:${HTTP_PORT}"

exec /bin/co server \
    --node-id="${NODE_NAME}" \
    --addr=":${HTTP_PORT}" \
    --advertise-addr="${advertise_addr}" \
    --peers="${http_peers}" \
    --swim-addr=":${SWIM_PORT}" \
    --swim-peers="${swim_peers}" \
    ${DEBUG:+--debug}
