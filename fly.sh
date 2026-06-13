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

cmd_deploy() {
    deploy_machine node-0 ams node-1,node-2
    deploy_machine node-1 fra node-0,node-2
    deploy_machine node-2 lhr node-0,node-1
    cmd_start
}

deploy_machine() {
    name="${1}"
    region="${2}"
    peers="${3}"

    id=$(machine_id "${name}")
    if [ -n "${id}" ]; then
        echo "${name}: updating (id=${id})"
        fly machine update "${id}" --app "${APP}" \
            --env NODE_NAME="${name}" \
            --env PEERS="${peers}" \
            --yes
    else
        echo "${name}: creating in ${region}"
        fly machine run . --app "${APP}" --name "${name}" --region "${region}" \
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
        echo "usage: ./fly.sh <deploy|start|stop|status>" >&2
        exit 1
        ;;
esac
