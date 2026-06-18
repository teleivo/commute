#!/usr/bin/env sh
# Manage the commute cluster on Fly.io.
#
# Usage: ./fly.sh <command> [--all]
#
# Commands:
#   deploy [--all]    build image and create or update machines if image or config changed, then start them
#                     --all deploys all 16 regions; default deploys only the 3 core nodes (ams, fra, lhr)
#   start             start all stopped machines
#   stop              stop all running machines
#   status            list all machines with their current state

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
    all=0
    if [ "${1:-}" = "--all" ]; then
        all=1
    fi

    commit=$(git rev-parse HEAD)
    build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    new_image=$(build_image "${commit}" "${build_time}")

    # Pass 1: create the 3 core nodes if they do not exist yet (without seed IDs since
    # IDs are not known until all machines exist).
    create_machine_if_missing node-0 ams "${new_image}" "${commit}"
    create_machine_if_missing node-1 fra "${new_image}" "${commit}"
    create_machine_if_missing node-2 lhr "${new_image}" "${commit}"

    # Fetch core IDs now that all core machines exist.
    id0=$(machine_id node-0)
    id1=$(machine_id node-1)
    id2=$(machine_id node-2)

    # Pass 2: update core nodes with seed IDs (own ID is injected by Fly as FLY_MACHINE_ID).
    update_machine node-0 "${new_image}" "${id1},${id2}" "${commit}"
    update_machine node-1 "${new_image}" "${id0},${id2}" "${commit}"
    update_machine node-2 "${new_image}" "${id0},${id1}" "${commit}"

    if [ "${all}" = "1" ]; then
        # Remaining 13 nodes seed from the 3 core nodes. No second pass needed since
        # core IDs are stable after pass 1. Bootstrap propagates the full peer list.
        core_seeds="${id0},${id1},${id2}"
        create_machine_if_missing node-3  iad "${new_image}" "${commit}"
        create_machine_if_missing node-4  ord "${new_image}" "${commit}"
        create_machine_if_missing node-5  dfw "${new_image}" "${commit}"
        create_machine_if_missing node-6  jnb "${new_image}" "${commit}"
        create_machine_if_missing node-7  lax "${new_image}" "${commit}"
        create_machine_if_missing node-8  cdg "${new_image}" "${commit}"
        create_machine_if_missing node-9  sjc "${new_image}" "${commit}"
        create_machine_if_missing node-10 gru "${new_image}" "${commit}"
        create_machine_if_missing node-11 ewr "${new_image}" "${commit}"
        create_machine_if_missing node-12 sin "${new_image}" "${commit}"
        create_machine_if_missing node-13 arn "${new_image}" "${commit}"
        create_machine_if_missing node-14 nrt "${new_image}" "${commit}"
        create_machine_if_missing node-15 yyz "${new_image}" "${commit}"
        update_machine node-3  "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-4  "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-5  "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-6  "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-7  "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-8  "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-9  "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-10 "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-11 "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-12 "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-13 "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-14 "${new_image}" "${core_seeds}" "${commit}"
        update_machine node-15 "${new_image}" "${core_seeds}" "${commit}"
    fi

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
            --env CO_COMMIT="${commit}" \
            --machine-config '{"metrics":{"port":8080,"path":"/metrics"}}'
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
        --machine-config '{"metrics":{"port":8080,"path":"/metrics"}}' \
        --yes
}

all_machine_ids() {
    machines_json | jq --raw-output '.[].id'
}

cmd_start() {
    # shellcheck disable=SC2046
    fly machine start --app "${APP}" $(all_machine_ids)
}

cmd_stop() {
    # shellcheck disable=SC2046
    fly machine stop --app "${APP}" $(all_machine_ids)
}

cmd_pause() {
    # shellcheck disable=SC2046
    fly machine suspend --app "${APP}" $(all_machine_ids)
}

cmd_status() {
    machines_json \
        | jq --raw-output \
            '.[] | [.name, .state, .region, (.config.env.CO_COMMIT // "unknown")] | @tsv' \
        | column --table --table-columns NAME,STATE,REGION,CO_COMMIT
}

case "${1:-}" in
    deploy) cmd_deploy "${2:-}" ;;
    start)  cmd_start ;;
    stop)   cmd_stop ;;
    pause)  cmd_pause ;;
    status) cmd_status ;;
    *)
        cat >&2 <<'EOF'
usage: ./fly.sh <command>

commands:
  deploy [--all]    build image and create or update machines if image or config changed, then start them
                    --all deploys all 16 regions; default deploys only the 3 core nodes (ams, fra, lhr)
  start             start all stopped machines
  stop              stop all running machines
  pause             suspend all machines (faster resume than stop)
  status            list all machines with their current state
EOF
        exit 1
        ;;
esac
