#!/usr/bin/env bash
# Runs the Claude Code CLI pointed at the RunPod-hosted vLLM pod instead of
# Anthropic's API. Opt-in per invocation — doesn't touch ~/.claude/settings.json
# or your normal `claude` usage.
#
# Usage: scripts/claude-runpod.sh [any claude CLI args]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib.sh
source "$ROOT/scripts/lib.sh"

load_env

if [ ! -f "$ROOT/.runpod-pod.env" ]; then
  echo "Missing $ROOT/.runpod-pod.env — run scripts/deploy-pod.sh first." >&2
  exit 1
fi
set -a
# shellcheck disable=SC1091
source "$ROOT/.runpod-pod.env"
set +a

: "${ANTHROPIC_BASE_URL:?.runpod-pod.env is missing ANTHROPIC_BASE_URL}"
: "${VLLM_API_KEY:?Set VLLM_API_KEY in .env}"
: "${MODEL_ID:?Set MODEL_ID in .env}"

if ! command -v claude >/dev/null 2>&1; then
  echo "claude CLI not found on PATH." >&2
  exit 1
fi

echo "Waiting for vLLM at $ANTHROPIC_BASE_URL to report healthy..." >&2
for _ in $(seq 1 60); do
  if curl -sf "$ANTHROPIC_BASE_URL/health" >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

# vLLM's Anthropic-compatible endpoint mimics the real Anthropic API, which
# authenticates via the `x-api-key` header (ANTHROPIC_API_KEY), not a Bearer
# token. If your vLLM version expects Bearer auth instead, switch this to
# ANTHROPIC_AUTH_TOKEN.
exec env \
  ANTHROPIC_BASE_URL="$ANTHROPIC_BASE_URL" \
  ANTHROPIC_API_KEY="$VLLM_API_KEY" \
  ANTHROPIC_MODEL="$MODEL_ID" \
  ANTHROPIC_DEFAULT_HAIKU_MODEL="$MODEL_ID" \
  ANTHROPIC_DEFAULT_SONNET_MODEL="$MODEL_ID" \
  DISABLE_TELEMETRY=1 \
  DISABLE_ERROR_REPORTING=1 \
  claude "$@"
