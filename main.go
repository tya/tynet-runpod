package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	vllmImage      = "vllm/vllm-openai:latest"
	vllmPort       = 8000
	hfCacheVolume  = "tynet-runpod-hf-cache"
	hfCacheSize    = 80
	hfCachePath    = "/root/.cache/huggingface"
	containerDisk  = 20
	maxModelLen    = 98304
	healthTimeout  = 10 * time.Minute
	healthInterval = 5 * time.Second
)

// execFunc replaces the running process image, as syscall.Exec does.
// Overridden in tests so cmdRun doesn't actually exec anything.
var execFunc = syscall.Exec

// healthChecker wraps waitHealthy with the production timeout/interval.
// Overridden in tests so command-level tests don't need a real health
// endpoint to poll for minutes.
var healthChecker = func(baseURL string) error {
	return waitHealthy(baseURL, healthTimeout, healthInterval)
}

// notifyContext is overridden in tests to hand cmdLogs an already-canceled
// context, so the Ctrl-C shutdown path is testable without sending a real
// SIGINT to the test process.
var notifyContext = signal.NotifyContext

func main() {
	os.Exit(run(os.Args))
}

// run dispatches on args[1] and returns the process exit code. Split out
// from main so the dispatch logic is testable without an os.Exit in the way.
func run(args []string) int {
	if len(args) < 2 {
		usage()
		return 2
	}

	ctx := context.Background()
	var err error
	switch args[1] {
	case "deploy":
		err = cmdDeploy(ctx)
	case "run":
		err = cmdRun(ctx, args[2:])
	case "destroy":
		err = cmdDestroy(ctx)
	case "gpus":
		err = cmdGPUs(ctx)
	case "resize":
		err = cmdResize(ctx)
	case "status":
		err = cmdStatus(ctx)
	case "logs":
		err = cmdLogs(ctx, args[2:])
	default:
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tynet-runpod <deploy|run|destroy|gpus|resize|status|logs>")
}

// vllmArgs builds the `vllm serve` argument string from the Environment's
// secrets, applying the same defaults everywhere it's needed.
func vllmArgs(secrets map[string]string) string {
	toolCallParser := secrets["TOOL_CALL_PARSER"]
	if toolCallParser == "" {
		toolCallParser = "qwen3_xml"
	}
	args := fmt.Sprintf(
		"%s --host 0.0.0.0 --port %d --api-key %s --enable-auto-tool-choice --tool-call-parser %s --max-model-len %d",
		secrets["MODEL_ID"], vllmPort, secrets["VLLM_API_KEY"], toolCallParser, maxModelLen,
	)
	if enforceEager(secrets) {
		args += " --enforce-eager"
	}
	return args
}

// enforceEager reports whether vLLM should skip torch.compile and CUDA
// graph capture for faster startup, at the cost of slower steady-state
// decode throughput — a good trade for interactive single-session use.
// Defaults to true; set VLLM_ENFORCE_EAGER=false in the tynet-runpod
// Environment to disable it and get full compiled-graph throughput instead.
func enforceEager(secrets map[string]string) bool {
	v := strings.ToLower(strings.TrimSpace(secrets["VLLM_ENFORCE_EAGER"]))
	return v != "false" && v != "0"
}

func cmdDeploy(ctx context.Context) error {
	secrets, err := secretsLoader(ctx)
	if err != nil {
		return err
	}
	if secrets["RUNPOD_API_KEY"] == "" {
		return fmt.Errorf("RUNPOD_API_KEY is empty in the tynet-runpod Environment")
	}
	if secrets["VLLM_API_KEY"] == "" {
		return fmt.Errorf("VLLM_API_KEY is empty in the tynet-runpod Environment")
	}
	if secrets["MODEL_ID"] == "" {
		return fmt.Errorf("MODEL_ID is empty in the tynet-runpod Environment")
	}
	if secrets["GPU_TYPE_ID"] == "" {
		return fmt.Errorf("GPU_TYPE_ID is empty in the tynet-runpod Environment")
	}
	podName := secrets["POD_NAME"]
	if podName == "" {
		podName = "tynet-runpod-vllm"
	}

	client := runpodClientFactory(secrets["RUNPOD_API_KEY"])

	fmt.Println("Ensuring persistent Hugging Face cache volume...")
	vol, err := client.ensureNetworkVolume(ctx, hfCacheVolume, hfCacheSize, secrets["GPU_TYPE_ID"])
	if err != nil {
		return fmt.Errorf("ensuring network volume: %w", err)
	}
	fmt.Printf("Using volume %s in %s\n", vol.ID, vol.DataCenter)

	env := map[string]string{"VLLM_API_KEY": secrets["VLLM_API_KEY"]}
	if hf := secrets["HF_TOKEN"]; hf != "" && hf != "REPLACE_ME_OPTIONAL" {
		env["HF_TOKEN"] = hf
	}

	fmt.Println("Creating pod...")
	p, err := client.createPod(ctx, createPodParams{
		Name:         podName,
		Image:        vllmImage,
		Args:         vllmArgs(secrets),
		GPUTypeID:    secrets["GPU_TYPE_ID"],
		DataCenterID: vol.DataCenter,
		VolumeID:     vol.ID,
		VolumePath:   hfCachePath,
		Env:          env,
		Ports:        []string{fmt.Sprintf("%d/http", vllmPort), "22/tcp"},
		Disk:         containerDisk,
	})
	if err != nil {
		return fmt.Errorf("creating pod: %w", err)
	}

	baseURL := podProxyURL(p.ID, vllmPort)
	if err := saveState(podState{
		PodID:        p.ID,
		BaseURL:      baseURL,
		DataCenterID: vol.DataCenter,
		VolumeID:     vol.ID,
	}); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	fmt.Printf("Pod %s created. Base URL: %s\n", p.ID, baseURL)
	fmt.Println("Waiting for the model server to become healthy (this can take several minutes while weights download/load)...")
	if err := healthChecker(baseURL); err != nil {
		return err
	}
	fmt.Println("Pod is healthy. Run `make run` to start Claude Code against it.")
	return nil
}

func cmdRun(ctx context.Context, extraArgs []string) error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if state.PodID == "" {
		return fmt.Errorf("no pod found — run `make deploy` first")
	}
	secrets, err := secretsLoader(ctx)
	if err != nil {
		return err
	}

	fmt.Println("Waiting for the model server to be healthy...")
	if err := healthChecker(state.BaseURL); err != nil {
		return err
	}

	claudePath, err := findClaude()
	if err != nil {
		return err
	}

	env := append(os.Environ(),
		"ANTHROPIC_BASE_URL="+state.BaseURL,
		// vLLM's --api-key only checks the `Authorization: Bearer` header,
		// not `x-api-key` — so this must be ANTHROPIC_AUTH_TOKEN, not
		// ANTHROPIC_API_KEY, even though the Anthropic protocol
		// conventionally uses x-api-key.
		"ANTHROPIC_AUTH_TOKEN="+secrets["VLLM_API_KEY"],
		"ANTHROPIC_MODEL="+secrets["MODEL_ID"],
		"ANTHROPIC_DEFAULT_HAIKU_MODEL="+secrets["MODEL_ID"],
		"ANTHROPIC_DEFAULT_SONNET_MODEL="+secrets["MODEL_ID"],
		// Claude Code doesn't recognize this model and defaults to assuming
		// a 200k window, so it requests output-token budgets that overflow
		// our --max-model-len. This tells it the real ceiling.
		fmt.Sprintf("CLAUDE_CODE_MAX_CONTEXT_TOKENS=%d", maxModelLen),
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
	)

	argv := append([]string{"claude"}, extraArgs...)
	return execFunc(claudePath, argv, env)
}

func cmdDestroy(ctx context.Context) error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if state.PodID == "" {
		fmt.Println("No pod recorded in .runpod-state.json — nothing to do.")
		return nil
	}
	secrets, err := secretsLoader(ctx)
	if err != nil {
		return err
	}
	if secrets["RUNPOD_API_KEY"] == "" {
		return fmt.Errorf("RUNPOD_API_KEY is empty in the tynet-runpod Environment")
	}

	client := runpodClientFactory(secrets["RUNPOD_API_KEY"])
	fmt.Printf("Terminating pod %s...\n", state.PodID)
	if err := client.deletePod(ctx, state.PodID); err != nil {
		return err
	}

	// Keep VolumeID so a future deploy reuses the cached model weights;
	// only the pod itself is torn down.
	if err := saveState(podState{VolumeID: state.VolumeID, DataCenterID: state.DataCenterID}); err != nil {
		return err
	}
	fmt.Println("Pod terminated.")
	return nil
}

// cmdGPUs lists, for every RunPod data center that supports network volumes,
// the GPUs currently in stock there. GPU_TYPE_ID must be one of these or
// deploy's volume placement has nowhere valid to put the pod.
func cmdGPUs(ctx context.Context) error {
	secrets, err := secretsLoader(ctx)
	if err != nil {
		return err
	}
	client := runpodClientFactory(secrets["RUNPOD_API_KEY"])
	dcs, err := client.listNetworkVolumeCapableGPUs(ctx)
	if err != nil {
		return err
	}
	for _, dc := range dcs {
		for _, g := range dc.GPUAvailability {
			if g.Availability == "NONE" {
				continue
			}
			fmt.Printf("%-10s %-8s %s\n", dc.ID, g.Availability, g.ID)
		}
	}
	return nil
}

// cmdResize re-applies the current vllmArgs (e.g. after changing MODEL_ID,
// TOOL_CALL_PARSER, or the maxModelLen constant) to an already-running pod
// and restarts it, without a full destroy/deploy cycle.
func cmdResize(ctx context.Context) error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if state.PodID == "" {
		return fmt.Errorf("no pod found — run `make deploy` first")
	}
	secrets, err := secretsLoader(ctx)
	if err != nil {
		return err
	}
	client := runpodClientFactory(secrets["RUNPOD_API_KEY"])
	if err := client.patchPodArgs(ctx, state.PodID, vllmArgs(secrets)); err != nil {
		return fmt.Errorf("patching pod args: %w", err)
	}
	fmt.Println("Args updated, restarting pod...")
	if err := client.restartPod(ctx, state.PodID); err != nil {
		return fmt.Errorf("restarting pod: %w", err)
	}
	fmt.Println("Waiting for the model server to become healthy...")
	return healthChecker(state.BaseURL)
}

// cmdStatus prints the pod's current status and live resource utilization.
func cmdStatus(ctx context.Context) error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if state.PodID == "" {
		return fmt.Errorf("no pod found — run `make deploy` first")
	}
	secrets, err := secretsLoader(ctx)
	if err != nil {
		return err
	}
	client := runpodClientFactory(secrets["RUNPOD_API_KEY"])
	p, err := client.getPod(ctx, state.PodID)
	if err != nil {
		return err
	}

	fmt.Printf("Pod:      %s (%s)\n", p.ID, p.Name)
	fmt.Printf("Status:   %s\n", p.Status)
	fmt.Printf("Uptime:   %s\n", (time.Duration(p.Runtime.Uptime) * time.Second).Round(time.Second))
	fmt.Printf("Cost:     $%.2f/hr\n", p.Cost)
	fmt.Printf("CPU:      %.0f%%\n", p.Runtime.CPU.Util)
	fmt.Printf("Memory:   %.0f%%\n", p.Runtime.Memory.Util)
	for i, g := range p.Runtime.GPUs {
		fmt.Printf("GPU[%d]:   util=%.0f%% mem=%.0f%%\n", i, g.Util, g.MemoryUtil)
	}
	fmt.Printf("Base URL: %s\n", state.BaseURL)
	return nil
}

// cmdLogs streams the pod's container/system logs until interrupted
// (Ctrl-C) or the connection closes.
func cmdLogs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	tail := fs.Int("tail", 100, "number of historical lines to backfill (0-5000)")
	source := fs.String("source", "", "log source to stream: container, system, or empty for both")
	if err := fs.Parse(args); err != nil {
		return err
	}

	state, err := loadState()
	if err != nil {
		return err
	}
	if state.PodID == "" {
		return fmt.Errorf("no pod found — run `make deploy` first")
	}
	secrets, err := secretsLoader(ctx)
	if err != nil {
		return err
	}
	client := runpodClientFactory(secrets["RUNPOD_API_KEY"])

	ctx, stop := notifyContext(ctx, os.Interrupt)
	defer stop()
	err = client.streamLogs(ctx, state.PodID, *tail, *source, os.Stdout)
	if ctx.Err() != nil {
		return nil // stopped by Ctrl-C, not a real error
	}
	return err
}

func findClaude() (string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude CLI not found on PATH: %w", err)
	}
	return path, nil
}

func waitHealthy(baseURL string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 10 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("timed out waiting for %s/health after %s — the model may still be loading; check the RunPod console", baseURL, timeout)
}
