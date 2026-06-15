#!/usr/bin/env sh
# Manage the 3-node commute cluster on Fly.io.
#
# Usage: ./fly.sh <command>
#
# Commands:
#   deploy    create or update all machines to the latest image, then start them
#   start     start all stopped machines
#   stop      stop all running machines
#   status    list all machines with their current state

set -eu

APP="commute"

# Returns the machine ID for the given name, or empty string if not found.
machine_id() {
    fly machine list --app "${APP}" --json \
        | jq --raw-output --arg name "${1}" '.[] | select(.name == $name) | .id'
}

# Returns the full image ref (including digest) for the given machine name,
# or empty string if not found.
machine_image() {
    fly machine list --app "${APP}" --json \
        | jq --raw-output --arg name "${1}" '.[] | select(.name == $name) | .config.image'
}

# Returns the env value for the given machine name and key, or empty string.
machine_env() {
    fly machine list --app "${APP}" --json \
        | jq --raw-output --arg name "${1}" --arg key "${2}" \
            '.[] | select(.name == $name) | .config.env[$key] // ""'
}

cmd_deploy() {
    build_output=$(fly deploy --build-only --push --app "${APP}" 2>&1)
    echo "${build_output}"
    new_image=$(echo "${build_output}" \
        | grep 'pushing manifest for' \
        | grep -o 'registry\.fly\.io/[^@]*@sha256:[a-f0-9]*' \
        | tail --lines=1)
    if [ -z "${new_image}" ]; then
        echo "error: could not determine built image ref" >&2
        exit 1
    fi
    echo "built image: ${new_image}"

    deploy_machine node-0 ams node-1,node-2 "${new_image}"
    deploy_machine node-1 fra node-0,node-2 "${new_image}"
    deploy_machine node-2 lhr node-0,node-1 "${new_image}"
    cmd_start
}

deploy_machine() {
    name="${1}"
    region="${2}"
    peers="${3}"
    new_image="${4}"

    id=$(machine_id "${name}")
    if [ -n "${id}" ]; then
        current_image=$(machine_image "${name}")
        current_node_name=$(machine_env "${name}" "NODE_NAME")
        current_peers=$(machine_env "${name}" "PEERS")
        if [ "${current_image}" = "${new_image}" ] \
            && [ "${current_node_name}" = "${name}" ] \
            && [ "${current_peers}" = "${peers}" ]; then
            echo "${name}: already up to date, skipping"
            return
        fi
        echo "${name}: updating (id=${id})"
        fly machine update "${id}" --app "${APP}" \
            --image "${new_image}" \
            --env NODE_NAME="${name}" \
            --env PEERS="${peers}" \
            --yes
    else
        echo "${name}: creating in ${region}"
        fly machine run . --app "${APP}" --name "${name}" --region "${region}" \
            --image "${new_image}" \
            --env NODE_NAME="${name}" \
            --env PEERS="${peers}"
    fi
}

cmd_start() {
    fly machine start --app "${APP}" \
        "$(machine_id node-0)" "$(machine_id node-1)" "$(machine_id node-2)"
}

cmd_stop() {
    fly machine stop --app "${APP}" \
        "$(machine_id node-0)" "$(machine_id node-1)" "$(machine_id node-2)"
}

cmd_status() {
    fly machine list --app "${APP}"
}

case "${1:-}" in
    deploy) cmd_deploy ;;
    start)  cmd_start ;;
    stop)   cmd_stop ;;
    status) cmd_status ;;
    *)
        cat >&2 <<'EOF'
usage: ./fly.sh <command>

commands:
  deploy    build image and create or update machines if image or config changed, then start them
  start     start all stopped machines
  stop      stop all running machines
  status    list all machines with their current state
EOF
        exit 1
        ;;
esac
