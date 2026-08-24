package main

import (
	"os"
	"testing"
)

func TestLoadState_MissingFileReturnsZeroValue(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := loadState()
	if err != nil {
		t.Fatalf("loadState() error = %v, want nil when file is absent", err)
	}
	if s != (podState{}) {
		t.Errorf("loadState() = %+v, want zero value", s)
	}
}

func TestSaveStateThenLoadStateRoundTrips(t *testing.T) {
	t.Chdir(t.TempDir())
	want := podState{PodID: "pod-1", BaseURL: "https://x", DataCenterID: "EU-NL-1", VolumeID: "vol-1"}
	if err := saveState(want); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}
	got, err := loadState()
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if got != want {
		t.Errorf("loadState() = %+v, want %+v", got, want)
	}
}

func TestSaveStateOverwritesPreviousState(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := saveState(podState{PodID: "old"}); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}
	if err := saveState(podState{PodID: "new"}); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}
	got, err := loadState()
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if got.PodID != "new" {
		t.Errorf("loadState().PodID = %q, want %q", got.PodID, "new")
	}
}

func TestLoadState_MalformedJSONReturnsError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(stateFile, []byte("not json"), 0o600); err != nil {
		t.Fatalf("writing malformed state file: %v", err)
	}
	if _, err := loadState(); err == nil {
		t.Fatal("loadState() error = nil, want a JSON decode error")
	}
}

func TestLoadState_UnreadableFileReturnsError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores file permissions, so the unreadable case can't be exercised here")
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile(stateFile, []byte("{}"), 0o000); err != nil {
		t.Fatalf("writing unreadable state file: %v", err)
	}
	if _, err := loadState(); err == nil {
		t.Fatal("loadState() error = nil, want a permission error")
	}
}
