package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunpodAPI implements runpodAPI with configurable behavior per method,
// so command-layer tests don't need the real RunPod API.
type fakeRunpodAPI struct {
	ensureNetworkVolumeFn      func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error)
	createPodFn                func(ctx context.Context, p createPodParams) (pod, error)
	deletePodFn                func(ctx context.Context, id string) error
	patchPodArgsFn             func(ctx context.Context, id, args string) error
	restartPodFn               func(ctx context.Context, id string) error
	listNetworkVolumeCapableFn func(ctx context.Context) ([]dataCenter, error)
}

func (f *fakeRunpodAPI) ensureNetworkVolume(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
	return f.ensureNetworkVolumeFn(ctx, name, sizeGB, gpuTypeID)
}
func (f *fakeRunpodAPI) createPod(ctx context.Context, p createPodParams) (pod, error) {
	return f.createPodFn(ctx, p)
}
func (f *fakeRunpodAPI) deletePod(ctx context.Context, id string) error {
	return f.deletePodFn(ctx, id)
}
func (f *fakeRunpodAPI) patchPodArgs(ctx context.Context, id, args string) error {
	return f.patchPodArgsFn(ctx, id, args)
}
func (f *fakeRunpodAPI) restartPod(ctx context.Context, id string) error {
	return f.restartPodFn(ctx, id)
}
func (f *fakeRunpodAPI) listNetworkVolumeCapableGPUs(ctx context.Context) ([]dataCenter, error) {
	return f.listNetworkVolumeCapableFn(ctx)
}

// withFakeRunpodClient overrides runpodClientFactory to always return fake,
// restoring the original on test cleanup.
func withFakeRunpodClient(t *testing.T, fake *fakeRunpodAPI) {
	t.Helper()
	orig := runpodClientFactory
	t.Cleanup(func() { runpodClientFactory = orig })
	runpodClientFactory = func(apiKey string) runpodAPI { return fake }
}

func withFakeSecrets(t *testing.T, secrets map[string]string, err error) {
	t.Helper()
	orig := secretsLoader
	t.Cleanup(func() { secretsLoader = orig })
	secretsLoader = func(ctx context.Context) (map[string]string, error) { return secrets, err }
}

func withFakeHealthChecker(t *testing.T, err error) {
	t.Helper()
	orig := healthChecker
	t.Cleanup(func() { healthChecker = orig })
	healthChecker = func(baseURL string) error { return err }
}

func withFakeExec(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := execFunc
	t.Cleanup(func() { execFunc = orig })
	execFunc = func(argv0 string, argv []string, envv []string) error {
		calls = append(calls, append([]string{argv0}, argv...))
		return nil
	}
	return &calls
}

func validSecrets() map[string]string {
	return map[string]string{
		"RUNPOD_API_KEY": "rp-key",
		"VLLM_API_KEY":   "vllm-key",
		"MODEL_ID":       "org/model",
		"GPU_TYPE_ID":    "NVIDIA L40S",
		"POD_NAME":       "test-pod",
	}
}

func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	f()
	w.Close()
	os.Stderr = orig
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestUsage(t *testing.T) {
	out := captureStderr(t, usage)
	if !strings.Contains(out, "usage:") {
		t.Errorf("usage() wrote %q, want it to contain %q", out, "usage:")
	}
}

func TestVllmArgs_DefaultsToolCallParser(t *testing.T) {
	got := vllmArgs(map[string]string{"MODEL_ID": "m", "VLLM_API_KEY": "k"})
	if !strings.Contains(got, "--tool-call-parser qwen3_xml") {
		t.Errorf("vllmArgs() = %q, want default parser qwen3_xml", got)
	}
	if !strings.Contains(got, "--max-model-len 98304") {
		t.Errorf("vllmArgs() = %q, want max-model-len 98304", got)
	}
}

func TestVllmArgs_UsesConfiguredToolCallParser(t *testing.T) {
	got := vllmArgs(map[string]string{"MODEL_ID": "m", "VLLM_API_KEY": "k", "TOOL_CALL_PARSER": "custom_parser"})
	if !strings.Contains(got, "--tool-call-parser custom_parser") {
		t.Errorf("vllmArgs() = %q, want custom_parser", got)
	}
}

func TestFindClaude_Found(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing fake claude binary: %v", err)
	}
	t.Setenv("PATH", dir)
	got, err := findClaude()
	if err != nil {
		t.Fatalf("findClaude() error = %v", err)
	}
	if got != claudePath {
		t.Errorf("findClaude() = %q, want %q", got, claudePath)
	}
}

func TestFindClaude_NotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := findClaude()
	if err == nil {
		t.Fatal("findClaude() error = nil, want error when claude isn't on PATH")
	}
}

func TestWaitHealthy_SucceedsOnFirstHealthyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitHealthy(srv.URL, time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("waitHealthy() error = %v", err)
	}
}

func TestWaitHealthy_RetriesUntilHealthy(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitHealthy(srv.URL, time.Second, time.Millisecond); err != nil {
		t.Fatalf("waitHealthy() error = %v", err)
	}
	if calls < 3 {
		t.Errorf("calls = %d, want at least 3", calls)
	}
}

func TestWaitHealthy_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	err := waitHealthy(srv.URL, 30*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("waitHealthy() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("waitHealthy() error = %v, want it to mention timing out", err)
	}
}

func TestWaitHealthy_TimesOutOnConnectionError(t *testing.T) {
	err := waitHealthy("http://127.0.0.1:1", 30*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("waitHealthy() error = nil, want timeout error when the server is unreachable")
	}
}

func TestRun_NoArgsPrintsUsage(t *testing.T) {
	out := captureStderr(t, func() {
		if code := run([]string{"tynet-runpod"}); code != 2 {
			t.Errorf("run() = %d, want 2", code)
		}
	})
	if !strings.Contains(out, "usage:") {
		t.Errorf("run() with no args wrote %q, want usage", out)
	}
}

func TestRun_UnknownCommandPrintsUsage(t *testing.T) {
	out := captureStderr(t, func() {
		if code := run([]string{"tynet-runpod", "bogus"}); code != 2 {
			t.Errorf("run() = %d, want 2", code)
		}
	})
	if !strings.Contains(out, "usage:") {
		t.Errorf("run() with unknown command wrote %q, want usage", out)
	}
}

func TestRun_CommandErrorPrintsErrorAndReturnsOne(t *testing.T) {
	withFakeSecrets(t, nil, errors.New("boom"))
	out := captureStderr(t, func() {
		if code := run([]string{"tynet-runpod", "gpus"}); code != 1 {
			t.Errorf("run() = %d, want 1", code)
		}
	})
	if !strings.Contains(out, "error:") || !strings.Contains(out, "boom") {
		t.Errorf("run() wrote %q, want it to print the error", out)
	}
}

func TestRun_DispatchesToEachKnownCommand(t *testing.T) {
	t.Chdir(t.TempDir())
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		listNetworkVolumeCapableFn: func(ctx context.Context) ([]dataCenter, error) { return nil, nil },
	})
	if code := run([]string{"tynet-runpod", "gpus"}); code != 0 {
		t.Errorf(`run(["gpus"]) = %d, want 0`, code)
	}
}

func TestRun_DispatchesDeploy(t *testing.T) {
	t.Chdir(t.TempDir())
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{ID: "vol-1", DataCenter: "EU-NL-1"}, nil
		},
		createPodFn: func(ctx context.Context, p createPodParams) (pod, error) { return pod{ID: "pod-1"}, nil },
	})
	if code := run([]string{"tynet-runpod", "deploy"}); code != 0 {
		t.Errorf(`run(["deploy"]) = %d, want 0`, code)
	}
}

func TestRun_DispatchesRun(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", BaseURL: "https://example.invalid"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, nil)
	withFakeExec(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing fake claude: %v", err)
	}
	t.Setenv("PATH", dir)
	if code := run([]string{"tynet-runpod", "run", "-p", "hi"}); code != 0 {
		t.Errorf(`run(["run"]) = %d, want 0`, code)
	}
}

func TestRun_DispatchesDestroy(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		deletePodFn: func(ctx context.Context, id string) error { return nil },
	})
	if code := run([]string{"tynet-runpod", "destroy"}); code != 0 {
		t.Errorf(`run(["destroy"]) = %d, want 0`, code)
	}
}

func TestRun_DispatchesResize(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", BaseURL: "https://example.invalid"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		patchPodArgsFn: func(ctx context.Context, id, args string) error { return nil },
		restartPodFn:   func(ctx context.Context, id string) error { return nil },
	})
	if code := run([]string{"tynet-runpod", "resize"}); code != 0 {
		t.Errorf(`run(["resize"]) = %d, want 0`, code)
	}
}

// TestHealthChecker_DefaultWrapsWaitHealthy exercises the *real* healthChecker
// closure (every other test overrides it) so its one line of glue is covered.
func TestHealthChecker_DefaultWrapsWaitHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := healthChecker(srv.URL); err != nil {
		t.Fatalf("healthChecker() error = %v", err)
	}
}

func TestCmdDeploy_Success(t *testing.T) {
	t.Chdir(t.TempDir())
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{ID: "vol-1", DataCenter: "EU-NL-1"}, nil
		},
		createPodFn: func(ctx context.Context, p createPodParams) (pod, error) {
			if p.Name != "test-pod" {
				t.Errorf("createPod Name = %q, want test-pod", p.Name)
			}
			return pod{ID: "pod-1"}, nil
		},
	})

	if err := cmdDeploy(context.Background()); err != nil {
		t.Fatalf("cmdDeploy() error = %v", err)
	}

	st, err := loadState()
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if st.PodID != "pod-1" || st.VolumeID != "vol-1" {
		t.Errorf("saved state = %+v, want PodID pod-1 and VolumeID vol-1", st)
	}
}

func TestCmdDeploy_HFTokenPassedWhenNotPlaceholder(t *testing.T) {
	t.Chdir(t.TempDir())
	secrets := validSecrets()
	secrets["HF_TOKEN"] = "real-token"
	withFakeSecrets(t, secrets, nil)
	withFakeHealthChecker(t, nil)
	var gotEnv map[string]string
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{ID: "vol-1", DataCenter: "EU-NL-1"}, nil
		},
		createPodFn: func(ctx context.Context, p createPodParams) (pod, error) {
			gotEnv = p.Env
			return pod{ID: "pod-1"}, nil
		},
	})
	if err := cmdDeploy(context.Background()); err != nil {
		t.Fatalf("cmdDeploy() error = %v", err)
	}
	if gotEnv["HF_TOKEN"] != "real-token" {
		t.Errorf("pod env HF_TOKEN = %q, want real-token", gotEnv["HF_TOKEN"])
	}
}

func TestCmdDeploy_HFTokenOmittedWhenPlaceholder(t *testing.T) {
	t.Chdir(t.TempDir())
	secrets := validSecrets()
	secrets["HF_TOKEN"] = "REPLACE_ME_OPTIONAL"
	withFakeSecrets(t, secrets, nil)
	withFakeHealthChecker(t, nil)
	var gotEnv map[string]string
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{ID: "vol-1", DataCenter: "EU-NL-1"}, nil
		},
		createPodFn: func(ctx context.Context, p createPodParams) (pod, error) {
			gotEnv = p.Env
			return pod{ID: "pod-1"}, nil
		},
	})
	if err := cmdDeploy(context.Background()); err != nil {
		t.Fatalf("cmdDeploy() error = %v", err)
	}
	if _, ok := gotEnv["HF_TOKEN"]; ok {
		t.Errorf("pod env = %+v, want no HF_TOKEN for the placeholder value", gotEnv)
	}
}

func TestCmdDeploy_DefaultsPodNameWhenUnset(t *testing.T) {
	t.Chdir(t.TempDir())
	secrets := validSecrets()
	delete(secrets, "POD_NAME")
	withFakeSecrets(t, secrets, nil)
	withFakeHealthChecker(t, nil)
	var gotName string
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{ID: "vol-1", DataCenter: "EU-NL-1"}, nil
		},
		createPodFn: func(ctx context.Context, p createPodParams) (pod, error) {
			gotName = p.Name
			return pod{ID: "pod-1"}, nil
		},
	})
	if err := cmdDeploy(context.Background()); err != nil {
		t.Fatalf("cmdDeploy() error = %v", err)
	}
	if gotName != "tynet-runpod-vllm" {
		t.Errorf("pod Name = %q, want default tynet-runpod-vllm", gotName)
	}
}

func TestCmdDeploy_SecretsLoadError(t *testing.T) {
	withFakeSecrets(t, nil, errors.New("op down"))
	if err := cmdDeploy(context.Background()); err == nil {
		t.Fatal("cmdDeploy() error = nil, want secrets error propagated")
	}
}

func TestCmdDeploy_MissingRequiredSecrets(t *testing.T) {
	tests := []struct {
		name   string
		remove string
	}{
		{"RUNPOD_API_KEY", "RUNPOD_API_KEY"},
		{"VLLM_API_KEY", "VLLM_API_KEY"},
		{"MODEL_ID", "MODEL_ID"},
		{"GPU_TYPE_ID", "GPU_TYPE_ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secrets := validSecrets()
			delete(secrets, tt.remove)
			withFakeSecrets(t, secrets, nil)
			err := cmdDeploy(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.remove) {
				t.Errorf("cmdDeploy() error = %v, want it to mention %s", err, tt.remove)
			}
		})
	}
}

func TestCmdDeploy_EnsureVolumeError(t *testing.T) {
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{}, errors.New("no stock")
		},
	})
	err := cmdDeploy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ensuring network volume") {
		t.Errorf("cmdDeploy() error = %v, want it to wrap the volume error", err)
	}
}

func TestCmdDeploy_CreatePodError(t *testing.T) {
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{ID: "vol-1", DataCenter: "EU-NL-1"}, nil
		},
		createPodFn: func(ctx context.Context, p createPodParams) (pod, error) {
			return pod{}, errors.New("capacity exceeded")
		},
	})
	err := cmdDeploy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "creating pod") {
		t.Errorf("cmdDeploy() error = %v, want it to wrap the create-pod error", err)
	}
}

func TestCmdDeploy_HealthCheckError(t *testing.T) {
	t.Chdir(t.TempDir())
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, errors.New("never became healthy"))
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{ID: "vol-1", DataCenter: "EU-NL-1"}, nil
		},
		createPodFn: func(ctx context.Context, p createPodParams) (pod, error) {
			return pod{ID: "pod-1"}, nil
		},
	})
	err := cmdDeploy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "never became healthy") {
		t.Errorf("cmdDeploy() error = %v, want the health check error", err)
	}
}

func TestCmdDeploy_SaveStateError(t *testing.T) {
	// Chdir into a path component that doesn't exist so saveState's
	// os.WriteFile fails, without touching saveState/loadState themselves.
	t.Chdir(t.TempDir())
	if err := os.Mkdir("readonly", 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions, so the write-failure case can't be exercised here")
	}
	t.Chdir("readonly")
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		ensureNetworkVolumeFn: func(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
			return networkVolume{ID: "vol-1", DataCenter: "EU-NL-1"}, nil
		},
		createPodFn: func(ctx context.Context, p createPodParams) (pod, error) {
			return pod{ID: "pod-1"}, nil
		},
	})
	err := cmdDeploy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "saving state") {
		t.Errorf("cmdDeploy() error = %v, want it to wrap the save-state error", err)
	}
}

func TestCmdRun_Success(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", BaseURL: "https://example.invalid"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, nil)
	calls := withFakeExec(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing fake claude: %v", err)
	}
	t.Setenv("PATH", dir)

	if err := cmdRun(context.Background(), []string{"-p", "hello"}); err != nil {
		t.Fatalf("cmdRun() error = %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("execFunc called %d times, want 1", len(*calls))
	}
	got := (*calls)[0]
	want := []string{filepath.Join(dir, "claude"), "claude", "-p", "hello"}
	if len(got) != len(want) || got[1] != "claude" || got[2] != "-p" || got[3] != "hello" {
		t.Errorf("execFunc argv = %v, want it to start with %v", got, want)
	}
}

func TestCmdRun_NoStateReturnsError(t *testing.T) {
	t.Chdir(t.TempDir())
	err := cmdRun(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "make deploy") {
		t.Errorf("cmdRun() error = %v, want it to suggest make deploy", err)
	}
}

func writeMalformedState(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(stateFile, []byte("not json"), 0o600); err != nil {
		t.Fatalf("writing malformed state file: %v", err)
	}
}

func TestCmdRun_LoadStateErrorPropagates(t *testing.T) {
	t.Chdir(t.TempDir())
	writeMalformedState(t)
	if err := cmdRun(context.Background(), nil); err == nil {
		t.Fatal("cmdRun() error = nil, want the loadState decode error propagated")
	}
}

func TestCmdRun_SecretsLoadError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", BaseURL: "https://example.invalid"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, nil, errors.New("op down"))
	if err := cmdRun(context.Background(), nil); err == nil {
		t.Fatal("cmdRun() error = nil, want secrets error propagated")
	}
}

func TestCmdRun_HealthCheckError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", BaseURL: "https://example.invalid"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, errors.New("unhealthy"))
	if err := cmdRun(context.Background(), nil); err == nil {
		t.Fatal("cmdRun() error = nil, want health check error propagated")
	}
}

func TestCmdRun_ClaudeNotFound(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", BaseURL: "https://example.invalid"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, nil)
	t.Setenv("PATH", t.TempDir())
	if err := cmdRun(context.Background(), nil); err == nil {
		t.Fatal("cmdRun() error = nil, want error when claude isn't on PATH")
	}
}

func TestCmdDestroy_Success(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", VolumeID: "vol-1", DataCenterID: "EU-NL-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	var deletedID string
	withFakeRunpodClient(t, &fakeRunpodAPI{
		deletePodFn: func(ctx context.Context, id string) error {
			deletedID = id
			return nil
		},
	})
	if err := cmdDestroy(context.Background()); err != nil {
		t.Fatalf("cmdDestroy() error = %v", err)
	}
	if deletedID != "pod-1" {
		t.Errorf("deletePod called with %q, want pod-1", deletedID)
	}
	st, err := loadState()
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if st.PodID != "" || st.VolumeID != "vol-1" {
		t.Errorf("state after destroy = %+v, want PodID cleared and VolumeID kept", st)
	}
}

func TestCmdDestroy_NoStateIsNoop(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := cmdDestroy(context.Background()); err != nil {
		t.Fatalf("cmdDestroy() error = %v, want nil when there's nothing to destroy", err)
	}
}

func TestCmdDestroy_LoadStateErrorPropagates(t *testing.T) {
	t.Chdir(t.TempDir())
	writeMalformedState(t)
	if err := cmdDestroy(context.Background()); err == nil {
		t.Fatal("cmdDestroy() error = nil, want the loadState decode error propagated")
	}
}

func TestCmdDestroy_SaveStateErrorAfterDelete(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", VolumeID: "vol-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions, so the write-failure case can't be exercised here")
	}
	// Read-only (not write-only) so the initial loadState (a read) still
	// succeeds, but the post-delete saveState overwrite fails.
	if err := os.Chmod(stateFile, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		deletePodFn: func(ctx context.Context, id string) error { return nil },
	})
	err := cmdDestroy(context.Background())
	if err == nil {
		t.Fatal("cmdDestroy() error = nil, want the post-delete saveState error propagated")
	}
}

func TestCmdDestroy_SecretsLoadError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, nil, errors.New("op down"))
	if err := cmdDestroy(context.Background()); err == nil {
		t.Fatal("cmdDestroy() error = nil, want secrets error propagated")
	}
}

func TestCmdDestroy_MissingAPIKey(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	secrets := validSecrets()
	delete(secrets, "RUNPOD_API_KEY")
	withFakeSecrets(t, secrets, nil)
	err := cmdDestroy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "RUNPOD_API_KEY") {
		t.Errorf("cmdDestroy() error = %v, want it to mention RUNPOD_API_KEY", err)
	}
}

func TestCmdDestroy_DeletePodError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		deletePodFn: func(ctx context.Context, id string) error { return errors.New("already gone") },
	})
	if err := cmdDestroy(context.Background()); err == nil {
		t.Fatal("cmdDestroy() error = nil, want deletePod error propagated")
	}
}

func TestCmdGPUs_Success(t *testing.T) {
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		listNetworkVolumeCapableFn: func(ctx context.Context) ([]dataCenter, error) {
			return []dataCenter{
				{ID: "DC-1", GPUAvailability: []resourceAvailability{
					{ID: "GPU-X", Availability: "HIGH"},
					{ID: "GPU-Y", Availability: "NONE"},
				}},
			}, nil
		},
	})
	if err := cmdGPUs(context.Background()); err != nil {
		t.Fatalf("cmdGPUs() error = %v", err)
	}
}

func TestCmdGPUs_SecretsLoadError(t *testing.T) {
	withFakeSecrets(t, nil, errors.New("op down"))
	if err := cmdGPUs(context.Background()); err == nil {
		t.Fatal("cmdGPUs() error = nil, want secrets error propagated")
	}
}

func TestCmdGPUs_ListError(t *testing.T) {
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		listNetworkVolumeCapableFn: func(ctx context.Context) ([]dataCenter, error) { return nil, errors.New("api down") },
	})
	if err := cmdGPUs(context.Background()); err == nil {
		t.Fatal("cmdGPUs() error = nil, want list error propagated")
	}
}

func TestCmdResize_Success(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", BaseURL: "https://example.invalid"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, nil)
	var patched, restarted bool
	withFakeRunpodClient(t, &fakeRunpodAPI{
		patchPodArgsFn: func(ctx context.Context, id, args string) error { patched = true; return nil },
		restartPodFn:   func(ctx context.Context, id string) error { restarted = true; return nil },
	})
	if err := cmdResize(context.Background()); err != nil {
		t.Fatalf("cmdResize() error = %v", err)
	}
	if !patched || !restarted {
		t.Errorf("patched=%v restarted=%v, want both true", patched, restarted)
	}
}

func TestCmdResize_NoStateReturnsError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := cmdResize(context.Background()); err == nil {
		t.Fatal("cmdResize() error = nil, want error when no pod is deployed")
	}
}

func TestCmdResize_LoadStateErrorPropagates(t *testing.T) {
	t.Chdir(t.TempDir())
	writeMalformedState(t)
	if err := cmdResize(context.Background()); err == nil {
		t.Fatal("cmdResize() error = nil, want the loadState decode error propagated")
	}
}

func TestCmdResize_SecretsLoadError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, nil, errors.New("op down"))
	if err := cmdResize(context.Background()); err == nil {
		t.Fatal("cmdResize() error = nil, want secrets error propagated")
	}
}

func TestCmdResize_PatchError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		patchPodArgsFn: func(ctx context.Context, id, args string) error { return errors.New("bad args") },
	})
	err := cmdResize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "patching pod args") {
		t.Errorf("cmdResize() error = %v, want it to wrap the patch error", err)
	}
}

func TestCmdResize_RestartError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeRunpodClient(t, &fakeRunpodAPI{
		patchPodArgsFn: func(ctx context.Context, id, args string) error { return nil },
		restartPodFn:   func(ctx context.Context, id string) error { return errors.New("restart failed") },
	})
	err := cmdResize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "restarting pod") {
		t.Errorf("cmdResize() error = %v, want it to wrap the restart error", err)
	}
}

func TestCmdResize_HealthCheckError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "pod-1", BaseURL: "https://example.invalid"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	withFakeSecrets(t, validSecrets(), nil)
	withFakeHealthChecker(t, errors.New("never healthy"))
	withFakeRunpodClient(t, &fakeRunpodAPI{
		patchPodArgsFn: func(ctx context.Context, id, args string) error { return nil },
		restartPodFn:   func(ctx context.Context, id string) error { return nil },
	})
	err := cmdResize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "never healthy") {
		t.Errorf("cmdResize() error = %v, want the health check error", err)
	}
}

// Sanity check that fakeRunpodAPI actually satisfies runpodAPI at compile
// time (a nil-method call in a test would otherwise panic, not fail to build).
var _ runpodAPI = (*fakeRunpodAPI)(nil)

// main() itself just calls os.Exit(run(os.Args)) and isn't separately unit
// tested — os.Exit would kill the test process. run()'s dispatch logic is
// covered exhaustively above.
