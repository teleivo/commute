#!/usr/bin/env bash
set -euo pipefail

TARGET=1000000
KEY="vial"
PORTS=(8080 8081 8082)
PORT_COUNT=${#PORTS[@]}
BATCH=200     # requests per curl --parallel invocation
PARALLEL=10   # max concurrent curl processes
JOBS=$(( TARGET / BATCH ))
CFGDIR=$(mktemp --directory)
trap 'rm -rf "$CFGDIR"' EXIT

echo "Generating $JOBS config files (${BATCH} reqs each)..."
python3 - <<EOF
import os
cfgdir = '$CFGDIR'
ports = [$(IFS=,; echo "${PORTS[*]}")]
batch = $BATCH
jobs = $JOBS
key = '$KEY'
for job in range(jobs):
    with open(f'{cfgdir}/cfg_{job}', 'w') as f:
        start = job * batch
        for i in range(batch):
            port = ports[(start + i) % len(ports)]
            f.write(f'url = "http://localhost:{port}/counters/{key}"\nrequest = POST\nheader = "Content-Type: application/json"\ndata = "{{\\"increment\\":1}}"\noutput = /dev/null\n')
            if i < batch - 1:
                f.write('next\n')
EOF

echo "Sending $TARGET increments across $PORT_COUNT nodes (parallel=$PARALLEL, batch=$BATCH)..."
START=$(date +%s%N)

running=0
for job in $(seq 0 $((JOBS - 1))); do
  curl --silent --parallel --parallel-immediate --config "$CFGDIR/cfg_${job}" || true &
  running=$(( running + 1 ))
  if [ $running -ge $PARALLEL ]; then
    wait -n 2>/dev/null || true
    running=$(( running - 1 ))
  fi
done
wait 2>/dev/null || true

echo "All increments sent. Waiting for convergence..."

while true; do
  value=$(curl --silent "http://localhost:8080/counters/$KEY" | grep --only-matching '"value":[0-9-]*' | cut --delimiter=: --fields=2 || true)
  if [ "${value:-0}" -ge "$TARGET" ]; then
    break
  fi
  sleep 0.1
done

END=$(date +%s%N)
ELAPSED_MS=$(( (END - START) / 1000000 ))
echo "Reached $TARGET in ${ELAPSED_MS}ms ($((ELAPSED_MS / 1000))s)"
