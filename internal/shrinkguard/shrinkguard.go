// Package shrinkguard protects against publishing a sanctions list that has
// collapsed — to zero, or by an implausibly large amount — versus the last
// published list. A silent shrink (source outage, parser regression, schema
// change) would otherwise ship a list that fails to screen addresses it used
// to catch, with nothing visibly wrong.
package shrinkguard

import (
	"encoding/json"
	"fmt"
	"os"
)

// DefaultThresholdPct is the maximum tolerated percentage drop in the total
// address count between successive publishes before the guard fails the run.
const DefaultThresholdPct = 15.0

// Manifest is the subset of manifest.json fields the shrink-guard needs.
type Manifest struct {
	Total          int            `json:"total"`
	PerChainCounts map[string]int `json:"per_chain_counts"`
}

// LoadManifest reads and decodes a manifest.json file.
func LoadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return m, nil
}

// Result is the shrink-guard verdict for one comparison.
type Result struct {
	PreviousTotal int
	NewTotal      int
	DropPct       float64 // percentage drop from previous to new; 0 if flat or grew
	ThresholdPct  float64
	Passed        bool
	Reason        string // human-readable failure reason, empty when Passed
}

// Check applies the shrink-guard policy:
//
//   - newTotal == 0 always fails — an empty sanctions list is never valid to
//     publish, regardless of history.
//   - previousTotal <= 0 (no prior manifest, e.g. first-ever publish, or one
//     that failed to load) has no baseline to compare against, so it passes
//     as long as newTotal is non-zero.
//   - Otherwise, a drop of more than thresholdPct percent fails.
func Check(previousTotal, newTotal int, thresholdPct float64) Result {
	r := Result{PreviousTotal: previousTotal, NewTotal: newTotal, ThresholdPct: thresholdPct, Passed: true}

	if newTotal == 0 {
		r.Passed = false
		r.Reason = "new total is 0 — refusing to publish an empty sanctions list"
		return r
	}
	if previousTotal <= 0 {
		// No last-good baseline to protect (first publish, or the prior
		// manifest couldn't be read) — nothing to compare against.
		return r
	}

	drop := previousTotal - newTotal
	if drop > 0 {
		r.DropPct = float64(drop) / float64(previousTotal) * 100
		if r.DropPct > thresholdPct {
			r.Passed = false
			r.Reason = fmt.Sprintf(
				"total dropped %.1f%% (from %d to %d), exceeds %.1f%% threshold — keeping last-good, not publishing",
				r.DropPct, previousTotal, newTotal, thresholdPct)
		}
	}
	return r
}
