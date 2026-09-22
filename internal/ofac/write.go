package ofac

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// manifest is the metadata sidecar. generated_at is the only non-deterministic
// field; it is supplied by the caller so tests can pin it.
type manifest struct {
	GeneratedAt        time.Time      `json:"generated_at"`
	SourceURL          string         `json:"source_url"`
	SourceLastModified string         `json:"source_last_modified"`
	SourceSHA256       string         `json:"source_sha256"`
	Total              int            `json:"total"`
	PerChainCounts     map[string]int `json:"per_chain_counts"`
}

// Write emits the consumer-compatible output set into outDir:
//
//   - sanctioned_addresses.json  — flat JSON array of entries, sorted (chain, address)
//   - sanctioned_addresses_{CHAIN}.txt — one address per line, sorted
//   - manifest.json — provenance + counts
//
// entries are assumed already sorted by Parse; Write re-sorts defensively so the
// output is deterministic regardless of input order.
func Write(entries []Entry, outDir string, meta Meta, now time.Time) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sortEntries(sorted)

	if err := writeJSONArray(filepath.Join(outDir, "sanctioned_addresses.json"), sorted); err != nil {
		return err
	}
	perChain, err := writePerChainFiles(outDir, sorted)
	if err != nil {
		return err
	}
	return writeManifest(filepath.Join(outDir, "manifest.json"), sorted, perChain, meta, now)
}

func writeJSONArray(path string, entries []Entry) error {
	// Ensure a JSON array (starting with '[') even when empty.
	if entries == nil {
		entries = []Entry{}
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func writePerChainFiles(outDir string, entries []Entry) (map[string]int, error) {
	byChain := map[string][]string{}
	for _, e := range entries {
		byChain[e.Chain] = append(byChain[e.Chain], e.Address)
	}
	counts := make(map[string]int, len(byChain))
	for chain, addrs := range byChain {
		sort.Strings(addrs)
		counts[chain] = len(addrs)
		var sb strings.Builder
		for _, a := range addrs {
			sb.WriteString(a)
			sb.WriteByte('\n')
		}
		name := fmt.Sprintf("sanctioned_addresses_%s.txt", chain)
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(sb.String()), 0o644); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func writeManifest(path string, entries []Entry, perChain map[string]int, meta Meta, now time.Time) error {
	m := manifest{
		GeneratedAt:        now.UTC(),
		SourceURL:          meta.SourceURL,
		SourceLastModified: meta.SourceLastModified,
		SourceSHA256:       meta.SourceSHA256,
		Total:              len(entries),
		PerChainCounts:     perChain,
	}
	if m.PerChainCounts == nil {
		m.PerChainCounts = map[string]int{}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
