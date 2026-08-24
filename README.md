# tynet-runpod

Run the Claude Code CLI against a self-hosted model on a RunPod GPU pod,
instead of Anthropic's API.

## How it works

vLLM natively exposes an Anthropic-compatible `/v1/messages` endpoint
alongside its usual OpenAI-compatible one. So the setup is just:

1. A small Go CLI in this repo creates a RunPod GPU pod running
   `vllm serve <model>` with tool-calling enabled, using RunPod's REST API
   directly (no `runpodctl`).
2. RunPod exposes the pod's port 8000 over HTTPS at
   `https://<pod-id>-8000.proxy.runpod.net`.
3. The same CLI execs Claude Code, pointed at that URL via
   `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_MODEL`.

No translation proxy in between — vLLM speaks the protocol Claude Code
expects directly.

## Secrets

All secrets/config live in the **tynet-runpod** 1Password Environment. This
CLI reads it directly via the [1Password Go
SDK](https://github.com/1password/onepassword-sdk-go) using desktop-app
integration — the first run pops a Touch ID/password prompt in the
1Password app to approve the integration; approve it once and subsequent
runs go through without a prompt.

Before first use:
```sh
cp .env.local.example .env.local   # gitignored; fill in your account/Environment IDs
```
`OP_ACCOUNT_ID`/`OP_ENVIRONMENT_ID` just identify *which* 1Password account
and Environment to read — they're not secrets, but are kept out of source so
a public clone of this repo doesn't reveal whose account it is. The Makefile
sources `.env.local` automatically.

Then fill in via the 1Password app (Developer → Environments →
tynet-runpod):
- `RUNPOD_API_KEY` — from the RunPod console (Settings → API Keys)
- `HF_TOKEN` — a Hugging Face access token
  ([huggingface.co/settings/tokens](https://huggingface.co/settings/tokens)).
  Only needed if you switch `MODEL_ID` to a *gated* model (one requiring you
  to accept a license on its Hugging Face page before downloading, e.g.
  Llama) — vLLM needs the token to authenticate the download. The default
  `MODEL_ID` is public, so this can stay as a placeholder for now.

`VLLM_API_KEY` is already set to a generated random token; `MODEL_ID`,
`GPU_TYPE_ID`, `POD_NAME`, and `TOOL_CALL_PARSER` have sensible defaults.

## Quick start

Run `make` (or `make help`) any time to list every available target.

```sh
make deploy    # create the GPU pod, wait for it to boot
make run       # run Claude Code against it
make status    # show pod status and live CPU/GPU/memory utilization
make logs      # stream the pod's container/system logs (Ctrl-C to stop)
make destroy   # tear the pod down when done (billed hourly; keeps the cache volume)
```

`make run`/`make logs` pass through extra args, e.g. `make run ARGS="-p 'hello'"` or
`make logs ARGS="-tail 500 -source container"`.

Two more before you change `GPU_TYPE_ID` or a running pod's config:
`make gpus` lists GPU stock in data centers that also support the persistent
cache volume; `make resize` re-applies `vllm serve` args to an already-running
pod and restarts it, without a full destroy/deploy cycle.

## Development

```sh
make build             # build the tynet-runpod binary
make test              # run the unit test suite
make cover             # run tests, then open an HTML coverage report
make test-integration  # deploy a REAL pod, verify it replies, then destroy it
make clean             # remove build/test artifacts
```

`make test` runs the unit test suite (fakes/seams, no live pod or 1Password
session needed). `make test-integration` is separate and opt-in only: it
deploys a **real** pod, sends it a test chat request, and destroys it again —
costs GPU billing and a few minutes, so it never runs in `make test` or CI.

See `CLAUDE.md` for architecture details and things that are easy to get
wrong when changing this setup.
