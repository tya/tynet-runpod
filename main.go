package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "deploy":
		err = cmdDeploy(ctx)
	case "run":
		err = cmdRun(ctx)
	case "destroy":
		err = cmdDestroy(ctx)
	case "gpus":
		err = cmdGPUs(ctx)
	case "resize":
		err = cmdResize(ctx)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tynet-runpod <deploy|run|destroy|gpus|resize>")
}

// vllmArgs builds the `vllm serve` argument string from the Environment's
// secrets, applying the same defaults everywhere it's needed.
func vllmArgs(secrets map[string]string) string {
	toolCallParser := secrets["TOOL_CALL_PARSER"]
	if toolCallParser == "" {
		toolCallParser = "qwen3_xml"
	}
	return fmt.Sprintf(
		"%s --host 0.0.0.0 --port %d --api-key %s --enable-auto-tool-choice --tool-call-parser %s --max-model-len %d",
		secrets["MODEL_ID"], vllmPort, secrets["VLLM_API_KEY"], toolCallParser, maxModelLen,
	)
}

func cmdDeploy(ctx context.Context) error {
	secrets, err := loadSecrets(ctx)
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

	client := newRunpodClient(secrets["RUNPOD_API_KEY"])

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
	if err := waitHealthy(baseURL); err != nil {
		return err
	}
	fmt.Println("Pod is healthy. Run `make run` to start Claude Code against it.")
	return nil
}

func cmdRun(ctx context.Context) error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if state.PodID == "" {
		return fmt.Errorf("no pod found — run `make deploy` first")
	}
	secrets, err := loadSecrets(ctx)
	if err != nil {
		return err
	}

	fmt.Println("Waiting for the model server to be healthy...")
	if err := waitHealthy(state.BaseURL); err != nil {
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

	argv := append([]string{"claude"}, os.Args[2:]...)
	return syscall.Exec(claudePath, argv, env)
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
	secrets, err := loadSecrets(ctx)
	if err != nil {
		return err
	}
	if secrets["RUNPOD_API_KEY"] == "" {
		return fmt.Errorf("RUNPOD_API_KEY is empty in the tynet-runpod Environment")
	}

	client := newRunpodClient(secrets["RUNPOD_API_KEY"])
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
	secrets, err := loadSecrets(ctx)
	if err != nil {
		return err
	}
	client := newRunpodClient(secrets["RUNPOD_API_KEY"])
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
	secrets, err := loadSecrets(ctx)
	if err != nil {
		return err
	}
	client := newRunpodClient(secrets["RUNPOD_API_KEY"])
	if err := client.patchPodArgs(ctx, state.PodID, vllmArgs(secrets)); err != nil {
		return fmt.Errorf("patching pod args: %w", err)
	}
	fmt.Println("Args updated, restarting pod...")
	if err := client.restartPod(ctx, state.PodID); err != nil {
		return fmt.Errorf("restarting pod: %w", err)
	}
	fmt.Println("Waiting for the model server to become healthy...")
	return waitHealthy(state.BaseURL)
}

func findClaude() (string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude CLI not found on PATH: %w", err)
	}
	return path, nil
}

func waitHealthy(baseURL string) error {
	deadline := time.Now().Add(healthTimeout)
	client := &http.Client{Timeout: 10 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(healthInterval)
	}
	return fmt.Errorf("timed out waiting for %s/health after %s — the model may still be loading; check the RunPod console", baseURL, healthTimeout)
}
