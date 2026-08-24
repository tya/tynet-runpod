package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const runpodAPIBase = "https://api.runpod.io"

// runpodAPI is the subset of runpodClient's methods the command layer
// depends on, so tests can substitute a fake instead of hitting the real
// RunPod API.
type runpodAPI interface {
	ensureNetworkVolume(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error)
	createPod(ctx context.Context, p createPodParams) (pod, error)
	deletePod(ctx context.Context, id string) error
	patchPodArgs(ctx context.Context, id, args string) error
	restartPod(ctx context.Context, id string) error
	listNetworkVolumeCapableGPUs(ctx context.Context) ([]dataCenter, error)
}

type runpodClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func newRunpodClient(apiKey string) *runpodClient {
	return &runpodClient{apiKey: apiKey, baseURL: runpodAPIBase, http: &http.Client{Timeout: 30 * time.Second}}
}

// runpodClientFactory is overridden in tests to return a fake runpodAPI.
var runpodClientFactory = func(apiKey string) runpodAPI {
	return newRunpodClient(apiKey)
}

func (c *runpodClient) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decoding response from %s %s: %w", method, path, err)
		}
	}
	return nil
}

type resourceAvailability struct {
	ID           string `json:"id"`
	Availability string `json:"availability"`
}

type dataCenter struct {
	ID                 string                 `json:"id"`
	NetworkVolumeTypes []string               `json:"networkVolumeTypes"`
	GPUAvailability    []resourceAvailability `json:"gpuAvailability"`
}

// pickDataCenter finds a data center that both supports network volumes and
// has stock for gpuTypeID, preferring higher availability tiers.
func (c *runpodClient) pickDataCenter(ctx context.Context, gpuTypeID string) (string, error) {
	var list struct {
		DataCenters []dataCenter `json:"dataCenters"`
	}
	path := "/v2/catalog/datacenters?include=GPU_AVAILABILITY&networkVolumeTypes=STANDARD"
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return "", err
	}

	rank := map[string]int{"HIGH": 3, "MEDIUM": 2, "LOW": 1, "NONE": 0}
	best := ""
	bestRank := -1
	for _, dc := range list.DataCenters {
		for _, g := range dc.GPUAvailability {
			if g.ID != gpuTypeID {
				continue
			}
			if r := rank[g.Availability]; r > bestRank {
				best, bestRank = dc.ID, r
			}
		}
	}
	if bestRank <= 0 {
		return "", fmt.Errorf("no network-volume-capable data center currently has stock for GPU type %q", gpuTypeID)
	}
	return best, nil
}

type networkVolume struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DataCenter string `json:"dataCenter"`
	Size       int    `json:"size"`
}

// ensureNetworkVolume returns an existing volume matching name, or creates a
// new one in a data center with stock for gpuTypeID.
func (c *runpodClient) ensureNetworkVolume(ctx context.Context, name string, sizeGB int, gpuTypeID string) (networkVolume, error) {
	var list struct {
		Volumes []networkVolume `json:"networkVolumes"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/network-volumes", nil, &list); err != nil {
		return networkVolume{}, err
	}
	for _, v := range list.Volumes {
		if v.Name == name {
			return v, nil
		}
	}

	dc, err := c.pickDataCenter(ctx, gpuTypeID)
	if err != nil {
		return networkVolume{}, err
	}

	var created networkVolume
	body := map[string]interface{}{
		"name":       name,
		"size":       sizeGB,
		"dataCenter": dc,
	}
	if err := c.do(ctx, http.MethodPost, "/v2/network-volumes", body, &created); err != nil {
		return networkVolume{}, err
	}
	return created, nil
}

type pod struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type createPodParams struct {
	Name         string
	Image        string
	Args         string
	GPUTypeID    string
	DataCenterID string
	VolumeID     string
	VolumePath   string
	Env          map[string]string
	Ports        []string
	Disk         int
}

func (c *runpodClient) createPod(ctx context.Context, p createPodParams) (pod, error) {
	body := map[string]interface{}{
		"name":  p.Name,
		"image": p.Image,
		"args":  p.Args,
		"cloud": "SECURE",
		"disk":  p.Disk,
		"env":   p.Env,
		"ports": p.Ports,
		"gpu": map[string]interface{}{
			"id":    p.GPUTypeID,
			"count": 1,
		},
		"dataCenterIds": []string{p.DataCenterID},
		"mounts": map[string]interface{}{
			"network": []map[string]string{
				{"volumeId": p.VolumeID, "path": p.VolumePath},
			},
		},
	}

	var created pod
	if err := c.do(ctx, http.MethodPost, "/v2/pods", body, &created); err != nil {
		return pod{}, err
	}
	return created, nil
}

func (c *runpodClient) deletePod(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v2/pods/"+id, nil, nil)
}

// patchPodArgs updates a pod's container args in place. Takes effect on the
// next restart, not live.
func (c *runpodClient) patchPodArgs(ctx context.Context, id, args string) error {
	return c.do(ctx, http.MethodPatch, "/v2/pods/"+id, map[string]string{"args": args}, nil)
}

func (c *runpodClient) restartPod(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v2/pods/"+id+"/action", map[string]string{"action": "restart"}, nil)
}

// listNetworkVolumeCapableGPUs returns, for every data center that supports
// network volumes, the GPUs it offers with current stock. Used to help pick
// a GPU_TYPE_ID that's actually deployable with a persistent cache volume.
func (c *runpodClient) listNetworkVolumeCapableGPUs(ctx context.Context) ([]dataCenter, error) {
	var list struct {
		DataCenters []dataCenter `json:"dataCenters"`
	}
	path := "/v2/catalog/datacenters?include=GPU_AVAILABILITY&networkVolumeTypes=STANDARD"
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list.DataCenters, nil
}

func podProxyURL(podID string, port int) string {
	return fmt.Sprintf("https://%s-%d.proxy.runpod.net", podID, port)
}
