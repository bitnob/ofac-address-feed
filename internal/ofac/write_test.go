package ofac

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "update golden files")

var pinnedClock = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

var testMeta = Meta{
	SourceURL:          "https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/sdn_advanced.xml",
	SourceLastModified: "Fri, 18 Sep 2026 14:01:59 GMT",
	SourceSHA256:       "0000000000000000000000000000000000000000000000000000000000000000",
}

func TestWriteGoldenJSON(t *testing.T) {
	entries := parseFixture(t)
	dir := t.TempDir()
	if err := Write(entries, dir, testMeta, pinnedClock); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "sanctioned_addresses.json"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	if len(got) == 0 || got[0] != '[' {
		t.Fatalf("sanctioned_addresses.json must start with '['; got prefix %q", string(got[:min(8, len(got))]))
	}

	golden := "testdata/golden_sanctioned_addresses.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden mismatch:\n got=%s\nwant=%s", got, want)
	}
}

func TestWriteManifestDeterministic(t *testing.T) {
	entries := parseFixture(t)
	dir := t.TempDir()
	if err := Write(entries, dir, testMeta, pinnedClock); err != nil {
		t.Fatalf("Write: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if !m.GeneratedAt.Equal(pinnedClock) {
		t.Errorf("generated_at not pinned: got %v want %v", m.GeneratedAt, pinnedClock)
	}
	if m.Total != 5 {
		t.Errorf("total: got %d want 5", m.Total)
	}
	want := map[string]int{"ETH": 3, "TRX": 1, "XBT": 1}
	for k, v := range want {
		if m.PerChainCounts[k] != v {
			t.Errorf("per_chain_counts[%s]: got %d want %d", k, m.PerChainCounts[k], v)
		}
	}
	if m.SourceSHA256 != testMeta.SourceSHA256 {
		t.Errorf("source_sha256 not carried through")
	}
}

func TestWritePerChainFiles(t *testing.T) {
	entries := parseFixture(t)
	dir := t.TempDir()
	if err := Write(entries, dir, testMeta, pinnedClock); err != nil {
		t.Fatalf("Write: %v", err)
	}
	eth, err := os.ReadFile(filepath.Join(dir, "sanctioned_addresses_ETH.txt"))
	if err != nil {
		t.Fatalf("read ETH file: %v", err)
	}
	want := "0x1111111111111111111111111111111111111111\n" +
		"0x2222222222222222222222222222222222222222\n" +
		"0x8589427373d6d84e98730d7795d8f6f8731fda16\n"
	if string(eth) != want {
		t.Errorf("ETH txt mismatch:\n got=%q\nwant=%q", eth, want)
	}
}
