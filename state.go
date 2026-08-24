package main

import (
	"encoding/json"
	"os"
)

const stateFile = ".runpod-state.json"

type podState struct {
	PodID        string `json:"podId"`
	BaseURL      string `json:"baseURL"`
	DataCenterID string `json:"dataCenterId"`
	VolumeID     string `json:"volumeId"`
}

func loadState() (podState, error) {
	var s podState
	b, err := os.ReadFile(stateFile)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func saveState(s podState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, b, 0o600)
}
