# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Infrastructure scripts for running the Claude Code CLI against a **self-hosted
model on a RunPod GPU pod** instead of Anthropic's API. There is no
application code here — it's a small set of shell scripts plus secrets
management.

## Architecture

The key fact that makes this simple: **vLLM natively exposes an
Anthropic-compatible `/v1/messages` endpoint alongside its usual
OpenAI-compatible one.** So there's no translation proxy or shim — Claude
Code talks to the pod directly.

```
Claude Code CLI  --ANTHROPIC_BASE_URL-->  RunPod pod (vllm serve, port 8000)
                                           ├─ /v1/chat/completions  (OpenAI-compatible)
                                           └─ /v1/messages          (Anthropic-compatible) <- Claude Code uses this
```

RunPod exposes the pod's port 8000 over HTTPS automatically at
`https://<pod-id>-8000.proxy.runpod.net` — no manual ingress/tunnel setup.

Auth: vLLM's `--api-key` flag protects both endpoints. Claude Code is
configured to send that same key. The Anthropic protocol conventionally
authenticates via the `x-api-key` header, which maps to Claude Code's
`ANTHROPIC_API_KEY` env var (not `ANTHROPIC_AUTH_TOKEN`, which sends a
`Bearer` token instead) — `scripts/claude-runpod.sh` uses `ANTHROPIC_API_KEY`
for this reason. If a given vLLM version expects Bearer auth instead, swap
which env var is set there.

Model: the model must support tool calling, since Claude Code relies on it
for file edits and bash execution — this is why `vllm serve` is always
launched with `--enable-auto-tool-choice --tool-call-parser <parser>`. The
parser name is model-family-specific (see vLLM's
[tool calling docs](https://docs.vllm.ai/en/stable/features/tool_calling/));
`TOOL_CALL_PARSER` in `.env` must match whatever `MODEL_ID` you're running.

## Secrets: 1Password Environment

All config/secrets live in the **tynet-runpod** 1Password Environment
(account `C5FUHLGUOBGJRA2PNDVTXY2MOU`), not in this repo. It's mounted
locally as `.env` at the repo root — 1Password keeps that file in sync, so
**never edit `.env` by hand**; edits get overwritten. Change values via the
1Password app instead. `.env.example` documents the variable names for
anyone not using 1Password.

`HF_TOKEN` is a Hugging Face access token, only required when `MODEL_ID` is
switched to a *gated* model (one requiring license acceptance on its
Hugging Face page, e.g. Llama) — vLLM needs it to authenticate the weight
download. The default `MODEL_ID` is public, so it's a placeholder until you
change models.

Two files are gitignored and never committed:
- `.env` — the 1Password-managed secrets (`RUNPOD_API_KEY`, `HF_TOKEN`,
  `VLLM_API_KEY`, `MODEL_ID`, `GPU_TYPE_ID`, `POD_NAME`, `TOOL_CALL_PARSER`)
- `.runpod-pod.env` — written by `deploy-pod.sh` after each pod launch
  (`POD_ID`, `ANTHROPIC_BASE_URL`). This is regenerated per-deploy and
  deliberately kept separate from the 1Password-synced `.env` so the two
  don't fight over ownership of the same file.

## Scripts

- `scripts/deploy-pod.sh` — creates the GPU pod via `runpodctl`, running
  `vllm/vllm-openai:latest` with `--docker-args` set to the full `vllm serve`
  invocation. Writes `.runpod-pod.env` with the resulting pod ID and base
  URL. Mounts a persistent volume at `/root/.cache/huggingface` so
  redeploys don't re-download model weights.
- `scripts/claude-runpod.sh` — thin wrapper that waits for the pod's
  `/health` endpoint, then `exec`s `claude` with `ANTHROPIC_BASE_URL` /
  `ANTHROPIC_API_KEY` / `ANTHROPIC_MODEL` set for that invocation only.
  Deliberately does not touch `~/.claude/settings.json` — this is opt-in per
  run, not a global override of normal Claude Code usage.
- `scripts/destroy-pod.sh` — terminates the pod. GPU pods bill hourly while
  running; there's no idle auto-shutdown configured, so this has to be run
  explicitly.
- `scripts/lib.sh` — shared helpers, notably `load_env`, which is why `.env`
  parsing isn't a plain `source .env`: 1Password writes unquoted values
  (e.g. `GPU_TYPE_ID=NVIDIA A40`), which a bare `source` mis-splits on the
  space. `load_env` quotes each value before sourcing.

`runpodctl`'s CLI syntax has changed across versions (`runpodctl create pod`
vs. newer noun-verb `runpodctl pod create`); `lib.sh`'s
`runpodctl_create_cmd`/`runpodctl_get_cmd` probe which form is installed
rather than assuming one.

## Things That Are Easy to Miss

- Changing `MODEL_ID` almost always requires changing `TOOL_CALL_PARSER` and
  `GPU_TYPE_ID` (VRAM headroom) to match — they're not independent knobs.
- The persistent volume (`--volume-in-gb 80` at `/root/.cache/huggingface`)
  is what makes redeploys fast. If you change `--volume-mount-path` or drop
  the volume, every deploy re-downloads the full model from Hugging Face.
- `scripts/claude-runpod.sh` polls `/health` for up to 5 minutes before
  running `claude`. If it times out, the model is probably still loading —
  check with `runpodctl <get pod|pod get> $POD_ID` or the RunPod console
  rather than assuming the deploy failed.
- Switching to a gated model requires both setting `HF_TOKEN` *and*
  accepting that model's license on its Hugging Face page in a browser
  first — a valid token alone still gets a 403 on download if the license
  hasn't been accepted.
