# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

A small Go CLI (`main.go` + a few helper files, no subpackages) that runs the
Claude Code CLI against a **self-hosted model on a RunPod GPU pod** instead
of Anthropic's API. It talks to 1Password and RunPod's REST API directly —
no `runpodctl`, no local `.env` file, no shell scripts.

## Commands

```sh
make build    # go build -o tynet-runpod .
make test     # go test ./...
make cover    # go test -coverprofile=... then open an HTML coverage view
go vet ./...  # static analysis
gofmt -l .    # check formatting

make deploy   # go run . deploy  — create the pod, wait for it to boot
make run      # go run . run     — exec `claude` against it
make destroy  # go run . destroy — terminate the pod (keeps the cache volume)
make gpus     # go run . gpus    — list GPUs with stock in network-volume-capable DCs
make resize   # go run . resize  — re-apply vllmArgs to a running pod and restart it
make status   # go run . status  — print pod status and live CPU/GPU/memory utilization
make logs     # go run . logs    — stream container/system logs (Ctrl-C to stop)

make test-integration  # deploy a REAL pod, send it a chat request, destroy it
```

`main_test.go`/`runpod_test.go`/`secrets_test.go`/`state_test.go` cover
95.8%+ of statements via fakes/seams (`runpodClientFactory`, `secretsLoader`,
`healthChecker`, `execFunc`) — no live RunPod pod or 1Password session
needed to run them. `.github/workflows/ci.yml` runs build/vet/gofmt/test on
every push to main and every PR, and is a required status check before
merging.

`make test-integration` runs `integration_test.go` (build tag `integration`,
so it's excluded from `go test ./...` and CI) — it exercises `cmdDeploy` /
`cmdDestroy` for real against the live RunPod API and a live pod, unlike the
unit tests which fake everything. It self-skips if secrets aren't reachable
(no 1Password session, missing values). If `.runpod-state.json` already
tracks a pod, the test reuses it instead of deploying a new one and leaves
it running afterward — `cmdDeploy` has no guard against double-deploying,
so calling it while a pod is already tracked would create an orphaned
second pod (state gets overwritten with the new pod's ID, and the original
keeps billing with nothing left pointing at it). A fresh deploy (and its
teardown on cleanup) only happens when the state file is empty; that path
costs real GPU-hour billing and takes several minutes.

## Architecture

**vLLM natively exposes an Anthropic-compatible `/v1/messages` endpoint**
alongside its usual OpenAI-compatible one, so there's no translation proxy —
Claude Code talks to the pod directly:

```
Claude Code CLI  --ANTHROPIC_BASE_URL-->  RunPod pod (vllm serve, port 8000)
                                           ├─ /v1/chat/completions  (OpenAI-compatible)
                                           └─ /v1/messages          (Anthropic-compatible) <- Claude Code uses this
```

RunPod exposes the pod's port 8000 over HTTPS automatically at
`https://<pod-id>-8000.proxy.runpod.net` — no manual ingress/tunnel setup.
`podProxyURL` in `runpod.go` constructs this from the pod ID; RunPod doesn't
return it from the API.

**Auth is `Authorization: Bearer`, not `x-api-key`.** vLLM's `--api-key` flag
only checks the `Bearer` header, on every route including `/v1/messages` —
confirmed empirically against a live pod, contradicting the Anthropic
protocol's usual `x-api-key` convention. This is why `cmdRun` in `main.go`
sets `ANTHROPIC_AUTH_TOKEN` (which makes Claude Code send `Bearer`), not
`ANTHROPIC_API_KEY` (which sends `x-api-key` and gets a 401).

**`CLAUDE_CODE_MAX_CONTEXT_TOKENS` must match `--max-model-len`.** Claude
Code doesn't recognize custom model names and defaults to assuming a 200k
window, so it requests output-token budgets that overflow a smaller
`--max-model-len`. `cmdRun` sets this env var from the `maxModelLen` constant
so Claude Code budgets correctly. If you change `maxModelLen` in `main.go`,
`cmdDeploy`'s `vllmArgs` picks it up automatically for the next deploy.

Model: the model must support tool calling, since Claude Code relies on it
for file edits and bash execution — this is why `vllm serve` is always
launched with `--enable-auto-tool-choice --tool-call-parser <parser>` (built
in `vllmArgs`). The parser name is model-family-specific (see vLLM's
[tool calling docs](https://docs.vllm.ai/en/stable/features/tool_calling/));
`TOOL_CALL_PARSER` must match whatever `MODEL_ID` you're running.

**`--enforce-eager` is on by default** (`enforceEager` in `main.go`) — it
skips `torch.compile` and CUDA graph capture, which were the bulk of a cold
deploy's ~5.3 minute startup (weight download is separate and already cached
by the network volume). Trade: somewhat slower steady-state decode
throughput, a good deal for an interactive single-session assistant. Set
`VLLM_ENFORCE_EAGER=false` in the tynet-runpod Environment to disable it and
get full compiled-graph throughput instead.

## Secrets: 1Password Environment, via the Go SDK

All config/secrets live in the **tynet-runpod** 1Password Environment,
identified by `OP_ACCOUNT_ID` and `OP_ENVIRONMENT_ID` env vars (not
themselves secrets — just identifiers — but kept out of source so a public
clone of this repo doesn't publish which 1Password account is whose; see
`.env.local.example`). `secrets.go` reads the Environment directly with
`github.com/1password/onepassword-sdk-go`'s `WithDesktopAppIntegration`,
which pops a native Touch ID/password prompt in the 1Password app on first
use. **There is no local `.env` file** for the RunPod/vLLM secrets
themselves — an
earlier version of this project mounted the Environment as a live FIFO via
1Password's "local .env file" feature, but that only resolves on a
directly-typed interactive shell command; any script or subprocess (even a
human-run one) reading it non-interactively gets a silent empty read. The
SDK's desktop-app integration is the mechanism that actually works from a
program.

Variables: `RUNPOD_API_KEY`, `HF_TOKEN`, `VLLM_API_KEY`, `MODEL_ID`,
`GPU_TYPE_ID`, `POD_NAME`, `TOOL_CALL_PARSER`, `VLLM_ENFORCE_EAGER`.
`HF_TOKEN` is a Hugging Face access token, only required when `MODEL_ID` is
switched to a *gated* model (one requiring license acceptance on its
Hugging Face page, e.g. Llama) — vLLM needs it to authenticate the weight
download. `VLLM_ENFORCE_EAGER` defaults to on (see Architecture above);
set it to `false` to disable.

Requires CGO to build (`WithDesktopAppIntegration` needs it; see the SDK's
README) — not an issue on a normal local `go build` on macOS, but relevant
if this is ever cross-compiled or run in a minimal container.

## RunPod: called directly via REST v2, no `runpodctl`

`runpod.go` is a small hand-written client for `https://api.runpod.io/v2`
(`Authorization: Bearer <RUNPOD_API_KEY>`). Key things that aren't obvious
from RunPod's docs:

- **Persistent storage must be a Network Volume, not `mounts.persistent`.**
  `mounts.persistent` is host-local (pinned to whatever machine the pod
  lands on) and doesn't survive a full pod recreation, which defeats the
  point of caching downloaded model weights across `destroy`/`deploy`
  cycles. `ensureNetworkVolume` creates (or reuses, by name) a Network
  Volume instead, mounted at `/root/.cache/huggingface`.
- **Network volumes are pinned to a specific data center at creation, and
  not every data center that offers a given GPU also supports network
  volumes.** `pickDataCenter` queries
  `/v2/catalog/datacenters?include=GPU_AVAILABILITY&networkVolumeTypes=STANDARD`
  and picks a DC that satisfies *both* constraints — querying GPU
  availability alone (`/v2/catalog/gpus/{id}`) isn't sufficient, since it
  will happily return DCs that don't support volumes. Run `make gpus` to
  see the current live intersection before picking a `GPU_TYPE_ID`.
  `NVIDIA A40` (this project's original default) turned out to have *zero*
  overlap with volume-capable DCs; `NVIDIA L40S` (same 48GB VRAM class) does.
- Once a volume exists, `cmdDeploy` reuses it and pins the pod to the
  volume's data center (`vol.DataCenter`) regardless of what `pickDataCenter`
  would choose fresh — so a volume created for one GPU type can strand you
  if you later switch to a `GPU_TYPE_ID` with no stock in that specific DC.
  Deleting the volume (or renaming `hfCacheVolume` in `main.go`) forces a
  fresh placement.
- **`GET /v2/pods/{id}/logs` is Server-Sent Events, not a plain JSON
  response.** `streamLogs` reads it as a stream of `data: {...}` lines
  (`{ts, source, line}` per event) rather than a single decoded body — this
  is why it doesn't go through the shared `do()` helper the other methods
  use. It uses a separate `*http.Client` (`c.stream`, no timeout) instead of
  `c.http` (30s timeout), since a log stream is meant to stay open
  indefinitely; `cmdLogs` cancels it via `signal.NotifyContext` on Ctrl-C.

## State

`.runpod-state.json` (gitignored) replaces the old `.runpod-pod.env` —
`{podId, baseURL, dataCenterId, volumeId}`, written by `deploy` and read by
`run`/`destroy`. `destroy` clears `podId`/`baseURL` but keeps `volumeId` so
the next `deploy` reuses the cached weights.

## Things That Are Easy to Miss

- Changing `MODEL_ID` almost always requires changing `TOOL_CALL_PARSER` and
  `GPU_TYPE_ID` to match — they're not independent knobs. Check `make gpus`
  for a `GPU_TYPE_ID` that's actually deployable before changing it.
- `cmdRun` polls `/health` for up to `healthTimeout` (10 minutes) before
  exec-ing `claude`. A cold deploy of a 30B-class model took about 5.3
  minutes in testing (weight download + `torch.compile` + CUDA graph
  capture); the original 5-minute timeout was cutting it close enough to
  fail intermittently, hence the bump.
- Switching to a gated model requires both setting `HF_TOKEN` *and*
  accepting that model's license on its Hugging Face page in a browser
  first — a valid token alone still gets a 403 on download if the license
  hasn't been accepted.
