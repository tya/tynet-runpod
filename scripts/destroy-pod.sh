#!/usr/bin/env bash
# Terminates the pod created by deploy-pod.sh. GPU pods bill by the hour
# while running — run this when you're done for the session.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib.sh
source "$ROOT/scripts/lib.sh"

load_env
require_runpodctl

if [ ! -f "$ROOT/.runpod-pod.env" ]; then
  echo "No $ROOT/.runpod-pod.env found — nothing to destroy." >&2
  exit 0
fi
set -a
# shellcheck disable=SC1091
source "$ROOT/.runpod-pod.env"
set +a

: "${POD_ID:?.runpod-pod.env is missing POD_ID}"
: "${RUNPOD_API_KEY:?Set RUNPOD_API_KEY in .env}"

runpodctl config --apiKey "$RUNPOD_API_KEY" >/dev/null

echo "Terminating pod $POD_ID..." >&2
runpodctl remove pod "$POD_ID"

rm -f "$ROOT/.runpod-pod.env"
echo "Done." >&2
