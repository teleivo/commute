#!/usr/bin/env sh
# Manage the commute load generator machines on Fly.io.
#
# Usage: ./fly-load.sh <command>
#
# Commands:
#   deploy    build image and create or update load generator machines, then start them
#   start     start all stopped load generator machines
#   stop      stop all running load generator machines
#   status    list all load generator machines with their current state

set -eu

APP="commute"
LOAD_APP="commute-load"
LOAD_CONFIG="load/fly.toml"
COUNTER_KEY="${COUNTER_KEY:-gopher-vs-crab}"
INCREMENT="${INCREMENT:-1}"
RATE="${RATE:-200/s}"

# One load generator per region group, each targeting nearby commute nodes.
# Name maps to: region, owned REGIONS list (max 3 nodes each)
GENERATORS="
gen-europe-west  ams  ams,fra,lhr
gen-europe-east  cdg  cdg,arn
gen-us-east      iad  iad,ewr,yyz
gen-us-central   ord  ord,dfw
gen-us-west      sjc  sjc,lax
gen-asia         sin  sin,nrt
gen-southam      gru  gru
gen-africa       jnb  jnb
"

machines_json() {
    fly machine list --app "${LOAD_APP}" --json
}

machine_id() {
    machines_json \
        | jq --raw-output --arg name "${1}" '.[] | select(.name == $name) | .id'
}

machine_image() {
    machines_json \
        | jq --raw-output --arg name "${1}" '.[] | select(.name == $name) | .config.image'
}

machine_env() {
    machines_json \
        | jq --raw-output --arg name "${1}" --arg key "${2}" \
            '.[] | select(.name == $name) | .config.env[$key] // ""'
}


build_image() {
    build_output=$(fly deploy \
        --build-only \
        --push \
        --no-cache \
        --config "${LOAD_CONFIG}" \
        --app "${LOAD_APP}" 2>&1)
    echo "${build_output}" >&2
    new_image=$(echo "${build_output}" \
        | grep 'pushing manifest for' \
        | grep --only-matching 'registry\.fly\.io/[^@]*@sha256:[a-f0-9]*' \
        | tail --lines=1)
    if [ -z "${new_image}" ]; then
        echo "error: could not determine built image ref" >&2
        exit 1
    fi
    echo "${new_image}"
}

cmd_deploy() {
    new_image=$(build_image)

    while read -r name region regions; do
        id=$(machine_id "${name}")
        if [ -z "${id}" ]; then
            echo "${name}: creating in ${region}"
            fly machine create "${new_image}" \
                --app "${LOAD_APP}" \
                --name "${name}" \
                --region "${region}" \
                --vm-size shared-cpu-1x \
                --env APP="${APP}" \
                --env REGIONS="${regions}" \
                --env COUNTER_KEY="${COUNTER_KEY}" \
                --env INCREMENT="${INCREMENT}" \
                --env RATE="${RATE}" \
                --machine-config '{"metrics":{"port":8880,"path":"/metrics"}}'
        else
            current_image=$(machine_image "${name}")
            current_regions=$(machine_env "${name}" "REGIONS")
            current_rate=$(machine_env "${name}" "RATE")
            if [ "${current_image}" = "${new_image}" ] \
                && [ "${current_regions}" = "${regions}" ] \
                && [ "${current_rate}" = "${RATE}" ]; then
                echo "${name}: already up to date, skipping"
            else
                echo "${name}: updating (id=${id})"
                fly machine update "${id}" \
                    --app "${LOAD_APP}" \
                    --image "${new_image}" \
                    --env APP="${APP}" \
                    --env REGIONS="${regions}" \
                    --env COUNTER_KEY="${COUNTER_KEY}" \
                    --env INCREMENT="${INCREMENT}" \
                    --env RATE="${RATE}" \
                    --machine-config '{"metrics":{"port":8880,"path":"/metrics"}}' \
                    --yes
            fi
        fi
    done <<EOF
$(echo "${GENERATORS}" | grep -v '^\s*$')
EOF

    cmd_start
}

cmd_start() {
    ids=$(machines_json | jq --raw-output '.[].id' | tr '\n' ' ')
    if [ -z "${ids}" ]; then
        echo "error: no load generator machines found, run deploy first" >&2
        exit 1
    fi
    # shellcheck disable=SC2086
    fly machine start --app "${LOAD_APP}" ${ids}
}

cmd_stop() {
    ids=$(machines_json | jq --raw-output '.[].id' | tr '\n' ' ')
    if [ -z "${ids}" ]; then
        echo "error: no load generator machines found" >&2
        exit 1
    fi
    # shellcheck disable=SC2086
    fly machine stop --app "${LOAD_APP}" ${ids}
}

cmd_status() {
    machines_json \
        | jq --raw-output \
            '.[] | [.name, .state, .region, (.config.env.REGIONS // "unknown")] | @tsv' \
        | column --table --table-columns NAME,STATE,REGION,REGIONS
}

case "${1:-}" in
    deploy) cmd_deploy ;;
    start)  cmd_start ;;
    stop)   cmd_stop ;;
    status) cmd_status ;;
    *)
        cat >&2 <<'EOF'
usage: ./fly-load.sh <command>

commands:
  deploy    build image and create or update load generator machines, then start them
  start     start all stopped load generator machines
  stop      stop all running load generator machines
  status    list all load generator machines with their current state
EOF
        exit 1
        ;;
esac
