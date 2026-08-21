# tynet-runpod

Run the Claude Code CLI against a self-hosted model on a RunPod GPU pod,
instead of Anthropic's API.

## How it works

vLLM natively exposes an Anthropic-compatible `/v1/messages` endpoint
alongside its usual OpenAI-compatible one. So the setup is just:

1. A RunPod GPU pod runs `vllm serve <model>` with tool-calling enabled.
2. RunPod exposes the pod's port 8000 over HTTPS at
   `https://<pod-id>-8000.proxy.runpod.net`.
3. The Claude Code CLI is pointed at that URL via `ANTHROPIC_BASE_URL` /
   `ANTHROPIC_API_KEY` / `ANTHROPIC_MODEL`.

No translation proxy in between — vLLM speaks the protocol Claude Code
expects directly.

## Secrets

All secrets/config live in the **tynet-runpod** 1Password Environment,
mounted locally as `.env` at the repo root (kept in sync by the 1Password
app — don't edit it directly). See `.env.example` for the variable names if
you're not using 1Password.

Before first use, fill in via the 1Password app:
- `RUNPOD_API_KEY` — from the RunPod console (Settings → API Keys)
- `HF_TOKEN` — a Hugging Face access token
  ([huggingface.co/settings/tokens](https://huggingface.co/settings/tokens)).
  Only needed if you switch `MODEL_ID` to a *gated* model (one requiring you
  to accept a license on its Hugging Face page before downloading, e.g.
  Llama) — vLLM needs the token to authenticate the download. The default
  `MODEL_ID` is public, so this can stay as the placeholder for now.

`VLLM_API_KEY` is already set to a generated random token; `MODEL_ID`,
`GPU_TYPE_ID`, `POD_NAME`, and `TOOL_CALL_PARSER` have sensible defaults.

## Quick start

```sh
scripts/deploy-pod.sh          # create the GPU pod, wait for it to boot
scripts/claude-runpod.sh       # run Claude Code against it
scripts/destroy-pod.sh         # tear the pod down when done (billed hourly)
```

See `CLAUDE.md` for architecture details and things that are easy to get
wrong when changing this setup.
