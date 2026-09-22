package shrinkguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckPassesOnGrowth(t *testing.T) {
	r := Check(1000, 1200, DefaultThresholdPct)
	if !r.Passed {
		t.Fatalf("expected pass on growth, got reason: %q", r.Reason)
	}
	if r.DropPct != 0 {
		t.Errorf("expected DropPct 0 on growth, got %v", r.DropPct)
	}
}

func TestCheckPassesOnFlat(t *testing.T) {
	r := Check(1000, 1000, DefaultThresholdPct)
	if !r.Passed {
		t.Fatalf("expected pass on flat total, got reason: %q", r.Reason)
	}
}

func TestCheckFailsOnZeroTotal(t *testing.T) {
	r := Check(1000, 0, DefaultThresholdPct)
	if r.Passed {
		t.Fatal("expected fail on new total of 0")
	}
	if r.Reason == "" {
		t.Error("expected a reason for the failure")
	}
}

func TestCheckFailsOnZeroTotalEvenWithNoBaseline(t *testing.T) {
	// An empty list is never acceptable, even on a first-ever publish.
	r := Check(0, 0, DefaultThresholdPct)
	if r.Passed {
		t.Fatal("expected fail on new total of 0 regardless of baseline")
	}
}

func TestCheckPassesWithNoBaseline(t *testing.T) {
	// First-ever publish (or an unreadable prior manifest): nothing to compare
	// against, so a non-zero new total passes.
	r := Check(0, 500, DefaultThresholdPct)
	if !r.Passed {
		t.Fatalf("expected pass with no baseline, got reason: %q", r.Reason)
	}
	if r.DropPct != 0 {
		t.Errorf("expected DropPct 0 with no baseline, got %v", r.DropPct)
	}
}

func TestCheckPassesAtExactThreshold(t *testing.T) {
	// Exactly a 15% drop (1000 -> 850) must pass; only *exceeding* the
	// threshold fails.
	r := Check(1000, 850, 15.0)
	if !r.Passed {
		t.Fatalf("expected pass at exactly the threshold, got reason: %q", r.Reason)
	}
}

func TestCheckFailsJustBeyondThreshold(t *testing.T) {
	// 1000 -> 849 is a 15.1% drop, just over a 15% threshold.
	r := Check(1000, 849, 15.0)
	if r.Passed {
		t.Fatal("expected fail just beyond the threshold")
	}
	if r.DropPct <= 15.0 {
		t.Errorf("expected DropPct > 15.0, got %v", r.DropPct)
	}
}

func TestCheckFailsOnLargeDrop(t *testing.T) {
	r := Check(1000, 100, DefaultThresholdPct)
	if r.Passed {
		t.Fatal("expected fail on a 90% drop")
	}
}

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	m := Manifest{Total: 42, PerChainCounts: map[string]int{"ETH": 20, "XBT": 22}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got.Total != 42 {
		t.Errorf("Total: got %d want 42", got.Total)
	}
	if got.PerChainCounts["ETH"] != 20 {
		t.Errorf("PerChainCounts[ETH]: got %d want 20", got.PerChainCounts["ETH"])
	}
}

func TestLoadManifestMissingFile(t *testing.T) {
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Fatal("expected error for missing manifest file")
	}
}
