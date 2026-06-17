#!/usr/bin/env sh
# Manage the 3-node commute cluster on Fly.io.
#
# Usage: ./fly.sh <command>
#
# Commands:
#   deploy    build image and create or update machines if image or config changed, then start them
#   start     start all stopped machines
#   stop      stop all running machines
#   status    list all machines with their current state

set -eu

APP="commute"

machines_json() {
    fly machine list --app "${APP}" --json
}

# Returns the machine ID for the given name, or empty string if not found.
machine_id() {
    machines_json \
        | jq --raw-output --arg name "${1}" '.[] | select(.name == $name) | .id'
}

# Returns the full image ref (including digest) for the given machine name,
# or empty string if not found.
machine_image() {
    machines_json \
        | jq --raw-output --arg name "${1}" '.[] | select(.name == $name) | .config.image'
}

# Returns the env value for the given machine name and key, or empty string.
machine_env() {
    machines_json \
        | jq --raw-output --arg name "${1}" --arg key "${2}" \
            '.[] | select(.name == $name) | .config.env[$key] // ""'
}

# Builds and pushes the image, prints build output to stderr, and prints the
# image ref (registry.fly.io/...@sha256:...) to stdout.
build_image() {
    commit="${1}"
    build_time="${2}"
    build_output=$(fly deploy --build-only --push --no-cache --app "${APP}" \
        --build-arg CO_COMMIT="${commit}" \
        --build-arg BUILD_TIME="${build_time}" 2>&1)
    echo "${build_output}" >&2
    new_image=$(echo "${build_output}" \
        | grep 'pushing manifest for' \
        | grep -o 'registry\.fly\.io/[^@]*@sha256:[a-f0-9]*' \
        | tail --lines=1)
    if [ -z "${new_image}" ]; then
        echo "error: could not determine built image ref" >&2
        exit 1
    fi
    echo "${new_image}"
}

cmd_deploy() {
    commit=$(git rev-parse HEAD)
    build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    new_image=$(build_image "${commit}" "${build_time}")

    # Pass 1: create any machines that do not exist yet (without seed IDs since
    # IDs are not known until all machines exist).
    create_machine_if_missing node-0 ams "${new_image}" "${commit}"
    create_machine_if_missing node-1 fra "${new_image}" "${commit}"
    create_machine_if_missing node-2 lhr "${new_image}" "${commit}"

    # Fetch IDs now that all machines exist.
    id0=$(machine_id node-0)
    id1=$(machine_id node-1)
    id2=$(machine_id node-2)

    # Pass 2: update each machine with seed IDs (own ID is injected by Fly as FLY_MACHINE_ID).
    update_machine node-0 "${new_image}" "${id1},${id2}" "${commit}"
    update_machine node-1 "${new_image}" "${id0},${id2}" "${commit}"
    update_machine node-2 "${new_image}" "${id0},${id1}" "${commit}"

    cmd_start
}

create_machine_if_missing() {
    name="${1}"
    region="${2}"
    new_image="${3}"
    commit="${4}"

    id=$(machine_id "${name}")
    if [ -z "${id}" ]; then
        echo "${name}: creating in ${region}"
        fly machine create "${new_image}" --app "${APP}" --name "${name}" --region "${region}" \
            --env CO_NODE_NAME="${name}" \
            --env CO_COMMIT="${commit}"
    fi
}

update_machine() {
    name="${1}"
    new_image="${2}"
    seed_ids="${3}"
    commit="${4}"

    id=$(machine_id "${name}")
    current_image=$(machine_image "${name}")
    current_seed_ids=$(machine_env "${name}" "CO_SEED_IDS")
    current_commit=$(machine_env "${name}" "CO_COMMIT")

    if [ "${current_image}" = "${new_image}" ] \
        && [ "${current_seed_ids}" = "${seed_ids}" ] \
        && [ "${current_commit}" = "${commit}" ]; then
        echo "${name}: already up to date, skipping"
        return
    fi

    echo "${name}: updating (id=${id})"
    fly machine update "${id}" --app "${APP}" \
        --image "${new_image}" \
        --env CO_NODE_NAME="${name}" \
        --env CO_SEED_IDS="${seed_ids}" \
        --env CO_COMMIT="${commit}" \
        --yes
}

cmd_start() {
    fly machine start --app "${APP}" \
        "$(machine_id node-0)" "$(machine_id node-1)" "$(machine_id node-2)"
}

cmd_stop() {
    fly machine stop --app "${APP}" \
        "$(machine_id node-0)" "$(machine_id node-1)" "$(machine_id node-2)"
}

cmd_pause() {
    fly machine suspend --app "${APP}" \
        "$(machine_id node-0)" "$(machine_id node-1)" "$(machine_id node-2)"
}

cmd_status() {
    machines_json \
        | jq --raw-output \
            '.[] | [.name, .state, .region, (.config.env.CO_COMMIT // "unknown")] | @tsv' \
        | column --table --table-columns NAME,STATE,REGION,CO_COMMIT
}

case "${1:-}" in
    deploy) cmd_deploy ;;
    start)  cmd_start ;;
    stop)   cmd_stop ;;
    pause)  cmd_pause ;;
    status) cmd_status ;;
    *)
        cat >&2 <<'EOF'
usage: ./fly.sh <command>

commands:
  deploy    build image and create or update machines if image or config changed, then start them
  start     start all stopped machines
  stop      stop all running machines
  pause     suspend all machines (faster resume than stop)
  status    list all machines with their current state
EOF
        exit 1
        ;;
esac
