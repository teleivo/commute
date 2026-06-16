#!/usr/bin/env sh
# Fly.io-specific entrypoint wrapper (see Dockerfile.fly). Maps Fly.io
# environment variables to co server flags. Fly.io does not support CLI
# arguments, so this script reads env vars and builds the flag list before
# exec-ing the binary.
#
# Required env vars (set via --env on fly machine run/update):
#   CO_NODE_NAME        this node's name (e.g. node-0); used as node-id for CRDT identity
#   CO_SEED_IDS         comma-separated seed machine IDs; used to build bootstrap addresses
#
# Injected automatically by Fly.io:
#   FLY_APP_NAME     app name, used to build <id>.vm.<app>.internal DNS names
#   FLY_MACHINE_ID   this machine's unique ID; used to build advertise-addr
#
# Fly registers <machine-id>.vm.<app>.internal in DNS, not <machine-name>.vm.<app>.internal,
# so all network addresses use machine IDs while CO_NODE_NAME is used only as the CRDT node identity.

set -eu

: "${CO_NODE_NAME:?CO_NODE_NAME is required}"
: "${CO_SEED_IDS:?CO_SEED_IDS is required}"
: "${FLY_APP_NAME:?FLY_APP_NAME is required}"
: "${FLY_MACHINE_ID:?FLY_MACHINE_ID is required}"

HTTP_PORT="${HTTP_PORT:-8080}"
SWIM_PORT="${SWIM_PORT:-7946}"
SWIM_JOIN_PORT="${SWIM_JOIN_PORT:-7947}"

# Build peer lists using machine-ID-based DNS names (<id>.vm.<app>.internal).
http_peers=""
swim_seeds=""
for id in $(echo "$CO_SEED_IDS" | tr ',' ' '); do
    host="${id}.vm.${FLY_APP_NAME}.internal"
    http_peers="${http_peers:+${http_peers},}${host}:${HTTP_PORT}"
    swim_seeds="${swim_seeds:+${swim_seeds},}${host}:${SWIM_JOIN_PORT}"
done

advertise_host="${FLY_MACHINE_ID}.vm.${FLY_APP_NAME}.internal"

exec /bin/co server \
    --node-id="${CO_NODE_NAME}" \
    --addr=":${HTTP_PORT}" \
    --advertise-addr="${advertise_host}:${HTTP_PORT}" \
    --peers="${http_peers}" \
    --swim-addr=":${SWIM_PORT}" \
    --swim-join-addr=":${SWIM_JOIN_PORT}" \
    --swim-seeds="${swim_seeds}" \
    ${DEBUG:+--debug}
