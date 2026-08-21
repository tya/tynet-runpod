#!/usr/bin/env bash
# Shared helpers, sourced by the other scripts in this directory.

repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

load_env() {
  local root
  root="$(repo_root)"
  if [ ! -f "$root/.env" ]; then
    echo "Missing $root/.env — mount the tynet-runpod 1Password Environment" \
         "locally (see README.md), or copy .env.example to .env and fill" \
         "in values." >&2
    exit 1
  fi
  # 1Password writes unquoted values (e.g. "GPU_TYPE_ID=NVIDIA A40"), which
  # a plain `source` mis-splits on the space. Quote each value before
  # sourcing so it loads as a single word.
  set -a
  # shellcheck disable=SC1090
  source <(grep -v '^#' "$root/.env" | grep -v '^$' | \
            sed -E 's/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/\1="\2"/')
  set +a
}

# Detects whether this runpodctl build uses the older `runpodctl create pod`
# syntax or the newer noun-verb `runpodctl pod create` syntax.
runpodctl_create_cmd() {
  if runpodctl create pod --help >/dev/null 2>&1; then
    echo "create pod"
  else
    echo "pod create"
  fi
}

runpodctl_get_cmd() {
  if runpodctl get pod --help >/dev/null 2>&1; then
    echo "get pod"
  else
    echo "pod get"
  fi
}

require_runpodctl() {
  if ! command -v runpodctl >/dev/null 2>&1; then
    echo "runpodctl not found on PATH. Install it:" >&2
    echo "  https://github.com/runpod/runpodctl#installation" >&2
    exit 1
  fi
}
