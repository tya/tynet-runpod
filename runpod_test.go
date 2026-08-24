package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *runpodClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := newRunpodClient("test-key")
	c.baseURL = srv.URL
	return c
}

func TestPodProxyURL(t *testing.T) {
	got := podProxyURL("abc123", 8000)
	want := "https://abc123-8000.proxy.runpod.net"
	if got != want {
		t.Errorf("podProxyURL() = %q, want %q", got, want)
	}
}

func TestNewRunpodClient(t *testing.T) {
	c := newRunpodClient("my-key")
	if c.apiKey != "my-key" {
		t.Errorf("apiKey = %q, want %q", c.apiKey, "my-key")
	}
	if c.baseURL != runpodAPIBase {
		t.Errorf("baseURL = %q, want %q", c.baseURL, runpodAPIBase)
	}
}

func TestNewRunpodAPI(t *testing.T) {
	api := newRunpodAPI("k")
	if _, ok := api.(*runpodClient); !ok {
		t.Errorf("newRunpodAPI returned %T, want *runpodClient", api)
	}
}

func TestDo_SuccessSendsAuthHeaderAndDecodes(t *testing.T) {
	var gotAuth, gotMethod, gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	})
	c.apiKey = "secret-token"

	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(context.Background(), http.MethodGet, "/v2/thing", nil, &out); err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret-token")
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/v2/thing" {
		t.Errorf("path = %q, want /v2/thing", gotPath)
	}
	if out.ID != "abc" {
		t.Errorf("decoded ID = %q, want %q", out.ID, "abc")
	}
}

func TestDo_SendsBodyWithContentType(t *testing.T) {
	var gotContentType, gotBody string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	})

	err := c.do(context.Background(), http.MethodPost, "/v2/thing", map[string]string{"name": "x"}, nil)
	if err != nil {
		t.Fatalf("do() error = %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if !strings.Contains(gotBody, `"name":"x"`) {
		t.Errorf("body = %q, want it to contain name:x", gotBody)
	}
}

func TestDo_NoOutNoDecodeAttempted(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.do(context.Background(), http.MethodDelete, "/v2/pods/x", nil, nil); err != nil {
		t.Fatalf("do() error = %v", err)
	}
}

func TestDo_NonSuccessStatusReturnsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("nope"))
	})
	err := c.do(context.Background(), http.MethodGet, "/v2/thing", nil, nil)
	if err == nil {
		t.Fatal("do() error = nil, want error for 403 status")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("do() error = %v, want it to mention status and body", err)
	}
}

func TestDo_DecodeErrorOnMalformedJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	})
	var out struct{ ID string }
	err := c.do(context.Background(), http.MethodGet, "/v2/thing", nil, &out)
	if err == nil {
		t.Fatal("do() error = nil, want decode error")
	}
	if !strings.Contains(err.Error(), "decoding response") {
		t.Errorf("do() error = %v, want it to mention decoding", err)
	}
}

func TestDo_MarshalErrorOnUnsupportedBody(t *testing.T) {
	c := newRunpodClient("k")
	c.baseURL = "http://unused.invalid"
	err := c.do(context.Background(), http.MethodPost, "/x", map[string]interface{}{"bad": make(chan int)}, nil)
	if err == nil {
		t.Fatal("do() error = nil, want json.Marshal error for a channel value")
	}
}

func TestDo_InvalidMethodReturnsError(t *testing.T) {
	c := newRunpodClient("k")
	c.baseURL = "http://unused.invalid"
	err := c.do(context.Background(), "BAD METHOD\n", "/x", nil, nil)
	if err == nil {
		t.Fatal("do() error = nil, want error for an invalid HTTP method")
	}
}

func TestDo_ConnectionErrorReturnsError(t *testing.T) {
	c := newRunpodClient("k")
	c.baseURL = "http://127.0.0.1:1" // reserved, nothing listens here
	err := c.do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("do() error = nil, want a connection error")
	}
}

// erroringBody is an io.ReadCloser that always fails to read, to exercise
// do()'s io.ReadAll error branch.
type erroringBody struct{}

func (erroringBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (erroringBody) Close() error             { return nil }

type erroringRoundTripper struct{}

func (erroringRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: erroringBody{}, Header: make(http.Header)}, nil
}

func TestDo_ReadBodyErrorReturnsError(t *testing.T) {
	c := newRunpodClient("k")
	c.baseURL = "http://unused.invalid"
	c.http.Transport = erroringRoundTripper{}
	err := c.do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("do() error = nil, want the body read error")
	}
}

func TestPickDataCenter_PicksHighestAvailability(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"dataCenters": []dataCenter{
				{ID: "DC-LOW", GPUAvailability: []resourceAvailability{{ID: "GPU-X", Availability: "LOW"}}},
				{ID: "DC-HIGH", GPUAvailability: []resourceAvailability{{ID: "GPU-X", Availability: "HIGH"}}},
				{ID: "DC-OTHER-GPU", GPUAvailability: []resourceAvailability{{ID: "GPU-Y", Availability: "HIGH"}}},
			},
		})
	})
	dc, err := c.pickDataCenter(context.Background(), "GPU-X")
	if err != nil {
		t.Fatalf("pickDataCenter() error = %v", err)
	}
	if dc != "DC-HIGH" {
		t.Errorf("pickDataCenter() = %q, want %q", dc, "DC-HIGH")
	}
}

func TestPickDataCenter_NoStockReturnsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"dataCenters": []dataCenter{
				{ID: "DC-NONE", GPUAvailability: []resourceAvailability{{ID: "GPU-X", Availability: "NONE"}}},
			},
		})
	})
	_, err := c.pickDataCenter(context.Background(), "GPU-X")
	if err == nil {
		t.Fatal("pickDataCenter() error = nil, want error when no DC has stock")
	}
}

func TestPickDataCenter_ListError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := c.pickDataCenter(context.Background(), "GPU-X")
	if err == nil {
		t.Fatal("pickDataCenter() error = nil, want error propagated from do()")
	}
}

func TestEnsureNetworkVolume_FindsExistingByName(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected only a GET when the volume already exists, got %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"networkVolumes": []networkVolume{
				{ID: "vol-1", Name: "tynet-runpod-hf-cache", DataCenter: "EU-NL-1", Size: 80},
			},
		})
	})
	vol, err := c.ensureNetworkVolume(context.Background(), "tynet-runpod-hf-cache", 80, "GPU-X")
	if err != nil {
		t.Fatalf("ensureNetworkVolume() error = %v", err)
	}
	if vol.ID != "vol-1" || vol.DataCenter != "EU-NL-1" {
		t.Errorf("ensureNetworkVolume() = %+v, want existing vol-1/EU-NL-1", vol)
	}
}

func TestEnsureNetworkVolume_CreatesWhenNotFound(t *testing.T) {
	var createBody map[string]interface{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/catalog/datacenters"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"dataCenters": []dataCenter{
					{ID: "EU-NL-1", GPUAvailability: []resourceAvailability{{ID: "GPU-X", Availability: "HIGH"}}},
				},
			})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"networkVolumes": []networkVolume{}})
		case r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_ = json.NewEncoder(w).Encode(networkVolume{ID: "vol-new", Name: "tynet-runpod-hf-cache", DataCenter: "EU-NL-1", Size: 80})
		}
	})
	vol, err := c.ensureNetworkVolume(context.Background(), "tynet-runpod-hf-cache", 80, "GPU-X")
	if err != nil {
		t.Fatalf("ensureNetworkVolume() error = %v", err)
	}
	if vol.ID != "vol-new" {
		t.Errorf("ensureNetworkVolume() = %+v, want new vol-new", vol)
	}
	if createBody["name"] != "tynet-runpod-hf-cache" {
		t.Errorf("create body = %+v, want name=tynet-runpod-hf-cache", createBody)
	}
}

func TestEnsureNetworkVolume_ListErrorPropagates(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := c.ensureNetworkVolume(context.Background(), "x", 80, "GPU-X")
	if err == nil {
		t.Fatal("ensureNetworkVolume() error = nil, want error from list")
	}
}

func TestEnsureNetworkVolume_PickDataCenterErrorPropagates(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "network-volumes") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"networkVolumes": []networkVolume{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"dataCenters": []dataCenter{}})
	})
	_, err := c.ensureNetworkVolume(context.Background(), "x", 80, "GPU-X")
	if err == nil {
		t.Fatal("ensureNetworkVolume() error = nil, want error when no DC has stock")
	}
}

func TestEnsureNetworkVolume_CreateErrorPropagates(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "network-volumes"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"networkVolumes": []networkVolume{}})
		case strings.Contains(r.URL.Path, "/catalog/datacenters"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"dataCenters": []dataCenter{{ID: "EU-NL-1", GPUAvailability: []resourceAvailability{{ID: "GPU-X", Availability: "HIGH"}}}},
			})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	_, err := c.ensureNetworkVolume(context.Background(), "x", 80, "GPU-X")
	if err == nil {
		t.Fatal("ensureNetworkVolume() error = nil, want error from create call")
	}
}

func TestCreatePod_Success(t *testing.T) {
	var gotBody map[string]interface{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(pod{ID: "pod-1", Name: "n", Status: "RUNNING"})
	})
	p, err := c.createPod(context.Background(), createPodParams{
		Name: "n", Image: "img", GPUTypeID: "GPU-X", DataCenterID: "DC-1",
		VolumeID: "vol-1", VolumePath: "/mnt", Env: map[string]string{"A": "B"},
		Ports: []string{"8000/http"}, Disk: 20,
	})
	if err != nil {
		t.Fatalf("createPod() error = %v", err)
	}
	if p.ID != "pod-1" {
		t.Errorf("createPod() = %+v, want ID pod-1", p)
	}
	if gotBody["name"] != "n" || gotBody["image"] != "img" {
		t.Errorf("createPod() request body = %+v", gotBody)
	}
}

func TestCreatePod_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	_, err := c.createPod(context.Background(), createPodParams{Name: "n"})
	if err == nil {
		t.Fatal("createPod() error = nil, want error on 400")
	}
}

func TestDeletePod(t *testing.T) {
	var gotMethod, gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.deletePod(context.Background(), "pod-1"); err != nil {
		t.Fatalf("deletePod() error = %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v2/pods/pod-1" {
		t.Errorf("deletePod() sent %s %s, want DELETE /v2/pods/pod-1", gotMethod, gotPath)
	}
}

func TestPatchPodArgs(t *testing.T) {
	var gotBody map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
	})
	if err := c.patchPodArgs(context.Background(), "pod-1", "new args"); err != nil {
		t.Fatalf("patchPodArgs() error = %v", err)
	}
	if gotBody["args"] != "new args" {
		t.Errorf("patchPodArgs() body = %+v, want args=new args", gotBody)
	}
}

func TestRestartPod(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
	})
	if err := c.restartPod(context.Background(), "pod-1"); err != nil {
		t.Fatalf("restartPod() error = %v", err)
	}
	if gotPath != "/v2/pods/pod-1/action" || gotBody["action"] != "restart" {
		t.Errorf("restartPod() sent path=%q body=%+v", gotPath, gotBody)
	}
}

func TestListNetworkVolumeCapableGPUs(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"dataCenters": []dataCenter{{ID: "DC-1"}, {ID: "DC-2"}},
		})
	})
	dcs, err := c.listNetworkVolumeCapableGPUs(context.Background())
	if err != nil {
		t.Fatalf("listNetworkVolumeCapableGPUs() error = %v", err)
	}
	if len(dcs) != 2 {
		t.Errorf("listNetworkVolumeCapableGPUs() returned %d DCs, want 2", len(dcs))
	}
}

func TestListNetworkVolumeCapableGPUs_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := c.listNetworkVolumeCapableGPUs(context.Background())
	if err == nil {
		t.Fatal("listNetworkVolumeCapableGPUs() error = nil, want error")
	}
}

func TestGetPod_Success(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{
			"id": "pod-1", "name": "n", "status": "RUNNING", "cost": 0.5,
			"runtime": {
				"uptime": 3661,
				"cpu": {"util": 12.5},
				"memory": {"util": 40},
				"gpus": [{"util": 80, "memoryUtil": 60}]
			}
		}`))
	})
	p, err := c.getPod(context.Background(), "pod-1")
	if err != nil {
		t.Fatalf("getPod() error = %v", err)
	}
	if gotPath != "/v2/pods/pod-1" {
		t.Errorf("getPod() requested path = %q, want /v2/pods/pod-1", gotPath)
	}
	if p.Status != "RUNNING" || p.Runtime.Uptime != 3661 || len(p.Runtime.GPUs) != 1 {
		t.Errorf("getPod() = %+v, want RUNNING/3661/1 gpu", p)
	}
	if p.Runtime.GPUs[0].Util != 80 || p.Runtime.GPUs[0].MemoryUtil != 60 {
		t.Errorf("getPod() GPU = %+v, want util=80 memoryUtil=60", p.Runtime.GPUs[0])
	}
}

func TestGetPod_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.getPod(context.Background(), "pod-1")
	if err == nil {
		t.Fatal("getPod() error = nil, want error on 404")
	}
}

func TestStreamLogs_Success(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 1\ndata: {\"ts\":\"2026-01-01T00:00:00Z\",\"source\":\"container\",\"line\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte(": comment, should be ignored\n\n"))
		_, _ = w.Write([]byte("data: {\"ts\":\"2026-01-01T00:00:01Z\",\"source\":\"system\",\"line\":\"world\"}\n\n"))
	})
	var buf strings.Builder
	err := c.streamLogs(context.Background(), "pod-1", 50, "container", &buf)
	if err != nil {
		t.Fatalf("streamLogs() error = %v", err)
	}
	if !strings.Contains(gotPath, "/v2/pods/pod-1/logs") || !strings.Contains(gotPath, "tail=50") || !strings.Contains(gotPath, "source=container") {
		t.Errorf("streamLogs() requested %q, want tail=50 and source=container", gotPath)
	}
	got := buf.String()
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Errorf("streamLogs() wrote %q, want both log lines", got)
	}
}

func TestStreamLogs_OmitsSourceWhenEmpty(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
	})
	var buf strings.Builder
	if err := c.streamLogs(context.Background(), "pod-1", 0, "", &buf); err != nil {
		t.Fatalf("streamLogs() error = %v", err)
	}
	if strings.Contains(gotPath, "source=") {
		t.Errorf("streamLogs() requested %q, want no source param when unset", gotPath)
	}
}

func TestStreamLogs_NonSuccessStatusReturnsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("nope"))
	})
	var buf strings.Builder
	err := c.streamLogs(context.Background(), "pod-1", 100, "", &buf)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("streamLogs() error = %v, want it to mention 403", err)
	}
}

func TestStreamLogs_ConnectionErrorReturnsError(t *testing.T) {
	c := newRunpodClient("k")
	c.baseURL = "http://127.0.0.1:1"
	var buf strings.Builder
	if err := c.streamLogs(context.Background(), "pod-1", 100, "", &buf); err == nil {
		t.Fatal("streamLogs() error = nil, want a connection error")
	}
}

func TestStreamLogs_InvalidRequestReturnsError(t *testing.T) {
	c := newRunpodClient("k")
	c.baseURL = "http://unused.invalid"
	err := c.streamLogs(context.Background(), "pod\n1", 100, "", &strings.Builder{})
	if err == nil {
		t.Fatal("streamLogs() error = nil, want an invalid-request error for a control character in the URL")
	}
}

func TestStreamLogs_SkipsMalformedEvent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {not valid json\n\n"))
		_, _ = w.Write([]byte("data: {\"ts\":\"2026-01-01T00:00:00Z\",\"source\":\"container\",\"line\":\"ok\"}\n\n"))
	})
	var buf strings.Builder
	if err := c.streamLogs(context.Background(), "pod-1", 100, "", &buf); err != nil {
		t.Fatalf("streamLogs() error = %v", err)
	}
	if !strings.Contains(buf.String(), "ok") {
		t.Errorf("streamLogs() wrote %q, want the malformed event skipped and the valid one kept", buf.String())
	}
}
