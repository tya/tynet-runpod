//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestIntegration_DeployRunDestroy sends a chat request to a real RunPod pod
// over the same Anthropic-compatible endpoint `claude` uses.
//
// cmdDeploy never checks for an existing pod before creating one — running
// it while .runpod-state.json already tracks a pod would create a second,
// unrelated pod and overwrite the state file with its ID, orphaning the
// original (still billing, no longer referenced anywhere). So this test
// checks state first: if a pod is already tracked, it's reused as-is —
// no new pod is deployed, and cleanup does *not* destroy it, since the test
// didn't create it and tearing down infrastructure it doesn't own would be
// its own surprise. A fresh pod is only deployed (and destroyed on cleanup)
// when .runpod-state.json is empty.
//
// This costs real GPU-hour billing and can take several minutes (weight
// download + torch.compile on a cold cache) on the fresh-deploy path. It
// never runs as part of `go test ./...` or CI — only via
// `make test-integration`, which passes -tags integration explicitly. It's
// skipped automatically if the tynet-runpod 1Password Environment isn't
// reachable or is missing required values, so it's safe to leave in the
// normal test tree.
func TestIntegration_DeployRunDestroy(t *testing.T) {
	ctx := context.Background()

	secrets, err := loadSecrets(ctx)
	if err != nil {
		t.Skipf("skipping integration test, couldn't load secrets: %v", err)
	}
	for _, k := range []string{"RUNPOD_API_KEY", "VLLM_API_KEY", "MODEL_ID", "GPU_TYPE_ID"} {
		if secrets[k] == "" {
			t.Skipf("skipping integration test: %s is empty in the tynet-runpod Environment", k)
		}
	}

	state, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}

	if state.PodID != "" {
		t.Logf("Pod %s is already tracked in .runpod-state.json — reusing it instead of deploying a new one.", state.PodID)
	} else {
		t.Cleanup(func() {
			t.Log("Tearing down the integration-test pod...")
			if err := cmdDestroy(context.Background()); err != nil {
				t.Errorf("cleanup: cmdDestroy: %v", err)
			}
		})

		t.Log("Deploying a real pod (this can take several minutes)...")
		if err := cmdDeploy(ctx); err != nil {
			t.Fatalf("cmdDeploy: %v", err)
		}

		state, err = loadState()
		if err != nil {
			t.Fatalf("loadState: %v", err)
		}
	}

	t.Log("Waiting for the pod to be healthy...")
	if err := waitHealthy(state.BaseURL, healthTimeout, healthInterval); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}

	t.Log("Sending a test chat request...")
	reply, err := sendTestMessage(ctx, state.BaseURL, secrets["VLLM_API_KEY"], secrets["MODEL_ID"])
	if err != nil {
		t.Fatalf("sendTestMessage: %v", err)
	}
	if reply == "" {
		t.Fatal("sendTestMessage: got an empty reply from the pod")
	}
	t.Logf("Got reply: %q", reply)
}

// sendTestMessage sends a minimal request to the pod's Anthropic-compatible
// /v1/messages endpoint (the same one `claude` talks to) and returns the
// assistant's text reply.
func sendTestMessage(ctx context.Context, baseURL, apiKey, model string) (string, error) {
	body, err := json.Marshal(map[string]interface{}{
		"model":      model,
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with exactly one word: pong"},
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	// Matches cmdRun's ANTHROPIC_AUTH_TOKEN convention — vLLM's --api-key
	// only checks the Bearer header, not x-api-key.
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST /v1/messages: %s: %s", resp.Status, string(respBody))
	}

	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decoding /v1/messages response: %w", err)
	}
	for _, c := range out.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}
	return "", nil
}
