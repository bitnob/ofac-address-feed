// Package crosscheck compares our freshly-generated per-chain sanctioned
// address sets against an independent reference feed, supplied by the
// caller as a base URL.
//
// The two directions of divergence are treated asymmetrically:
//
//   - MISSES (the reference feed has an address we don't) are the critical,
//     near-zero-tolerance direction: a miss is a sanctioned address we would
//     fail to screen. Any miss fails the check.
//   - EXTRAS (we have an address the reference feed doesn't) are expected in
//     normal operation — we parse the SDN XML directly and may pick up
//     names, chains, or entries the reference extractor skips — so a small
//     tolerance is allowed.
package crosscheck

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultExtraTolerancePct is the default allowance for extras, expressed as a
// percentage of the reference chain's address count.
const DefaultExtraTolerancePct = 20.0

// DefaultUserAgent is sent on reference fetches (some hosts reject blank UAs).
const DefaultUserAgent = "ofac-address-feed-crosscheck/1.0"

// maxListed caps how many individual addresses are printed per chain/category
// in the human-readable report, to keep CI logs readable when divergence is
// large.
const maxListed = 25

// ChainResult is the outcome of comparing our address set for one chain
// against the reference feed's set for that same chain.
type ChainResult struct {
	Chain       string
	OurCount    int
	RefCount    int
	Misses      []string // in reference, not in ours — critical
	Extras      []string // in ours, not in reference — tolerated
	RefNotFound bool     // reference feed returned 404 for this chain
}

// normalizeAddress lowercases 0x/EVM-style addresses for case-insensitive
// comparison; every other address format (base58, bech32, base58check, ...) is
// compared byte-for-byte since case is significant there.
func normalizeAddress(a string) string {
	a = strings.TrimSpace(a)
	if len(a) >= 2 && a[0] == '0' && (a[1] == 'x' || a[1] == 'X') {
		return strings.ToLower(a)
	}
	return a
}

// Compare is a pure function: given our addresses and the independent
// reference feed's addresses for a single chain, it returns the set
// differences. Both slices may contain duplicates or mixed case; Compare
// normalizes and dedups.
func Compare(chain string, ours, ref []string) ChainResult {
	ourSet := map[string]string{}
	for _, a := range ours {
		ourSet[normalizeAddress(a)] = a
	}
	refSet := map[string]string{}
	for _, a := range ref {
		refSet[normalizeAddress(a)] = a
	}

	var misses, extras []string
	for k, orig := range refSet {
		if _, ok := ourSet[k]; !ok {
			misses = append(misses, orig)
		}
	}
	for k, orig := range ourSet {
		if _, ok := refSet[k]; !ok {
			extras = append(extras, orig)
		}
	}
	sort.Strings(misses)
	sort.Strings(extras)

	return ChainResult{
		Chain:    chain,
		OurCount: len(ourSet),
		RefCount: len(refSet),
		Misses:   misses,
		Extras:   extras,
	}
}

// Summary is the overall verdict across every chain checked.
type Summary struct {
	Chains  []ChainResult
	Passed  bool
	Reasons []string
}

// Evaluate applies the pass/fail policy:
//
//   - Any miss, on any chain, fails the run. There is no tolerance for misses.
//   - Extras only fail once they exceed extraTolerancePct of the reference
//     chain's own count. A chain the reference doesn't publish at all
//     (RefCount == 0, e.g. a 404) never fails on extras alone — there is no
//     baseline to have drifted from, and covering a chain the reference feed
//     doesn't is expected/fine.
func Evaluate(results []ChainResult, extraTolerancePct float64) Summary {
	s := Summary{Chains: results, Passed: true}
	for _, r := range results {
		if len(r.Misses) > 0 {
			s.Passed = false
			s.Reasons = append(s.Reasons, fmt.Sprintf(
				"%s: %d miss(es) — addresses the reference feed has that we don't", r.Chain, len(r.Misses)))
		}
		if r.RefCount > 0 {
			allowed := extraTolerancePct / 100 * float64(r.RefCount)
			if float64(len(r.Extras)) > allowed {
				s.Passed = false
				s.Reasons = append(s.Reasons, fmt.Sprintf(
					"%s: %d extra(s) exceed tolerance (%.1f%% of ref count %d = %.1f allowed)",
					r.Chain, len(r.Extras), extraTolerancePct, r.RefCount, allowed))
			}
		}
	}
	return s
}

// FormatSummary renders a human-readable report suitable for CI logs.
func FormatSummary(s Summary) string {
	var sb strings.Builder
	sb.WriteString("OFAC cross-check vs independent reference feed\n")
	for _, r := range s.Chains {
		note := ""
		if r.RefNotFound {
			note = "  (reference publishes no list for this chain)"
		}
		fmt.Fprintf(&sb, "  %-6s ours=%-6d ref=%-6d misses=%-4d extras=%-4d%s\n",
			r.Chain, r.OurCount, r.RefCount, len(r.Misses), len(r.Extras), note)
		for i, m := range r.Misses {
			if i >= maxListed {
				fmt.Fprintf(&sb, "    ... and %d more miss(es)\n", len(r.Misses)-maxListed)
				break
			}
			fmt.Fprintf(&sb, "    MISS  %s\n", m)
		}
	}
	if s.Passed {
		sb.WriteString("PASS\n")
	} else {
		sb.WriteString("FAIL:\n")
		for _, reason := range s.Reasons {
			fmt.Fprintf(&sb, "  - %s\n", reason)
		}
	}
	return sb.String()
}

// LoadOurAddresses reads a per-chain address list previously written by
// ofac.Write (one address per line). A missing file means we produced zero
// addresses for that chain, not an error.
func LoadOurAddresses(path string) ([]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// referenceURL builds the per-chain reference feed URL from a caller-supplied
// base URL, assuming the same flat-JSON-array file naming we use ourselves:
// "{baseURL}/sanctioned_addresses_{CHAIN}.json".
func referenceURL(baseURL, chain string) string {
	return strings.TrimRight(baseURL, "/") + "/sanctioned_addresses_" + chain + ".json"
}

// FetchReference downloads the independent reference feed's published
// address list for a chain, given the feed's base URL. A 404 means the
// reference feed simply doesn't publish that chain — notFound=true, err=nil —
// which is a signal to report, not a fetch failure. Any other non-200 status
// or transport error is returned as err.
func FetchReference(ctx context.Context, baseURL, chain, userAgent string, timeout time.Duration) (addrs []string, notFound bool, err error) {
	url := referenceURL(baseURL, chain)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("fetch %s: unexpected status %s", url, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", url, err)
	}
	var list []string
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, false, fmt.Errorf("decode %s: %w", url, err)
	}
	return list, false, nil
}

// Config controls a full cross-check Run.
type Config struct {
	Dir               string        // directory holding our sanctioned_addresses_{CHAIN}.txt + manifest.json
	ReferenceBaseURL  string        // base URL of the independent reference feed; required (no default)
	Chains            []string      // defaults to our manifest's chains if empty
	UserAgent         string        // defaults to DefaultUserAgent if empty
	Timeout           time.Duration // per-chain fetch timeout, defaults to 30s if zero
	ExtraTolerancePct float64       // defaults to DefaultExtraTolerancePct if zero
}

// chainsFromManifest reads the chain codes present in dir/manifest.json's
// per_chain_counts. A missing or unparsable manifest yields no chains rather
// than an error.
func chainsFromManifest(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil
	}
	var m struct {
		PerChainCounts map[string]int `json:"per_chain_counts"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	out := make([]string, 0, len(m.PerChainCounts))
	for c := range m.PerChainCounts {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Run executes a full cross-check: for each chain in cfg.Chains (or the
// chains we ourselves generated, per manifest.json, if empty), it loads our
// locally-generated addresses, fetches the independent reference feed's list,
// compares, and evaluates the aggregate verdict.
func Run(ctx context.Context, cfg Config) (Summary, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tolerance := cfg.ExtraTolerancePct
	if tolerance <= 0 {
		tolerance = DefaultExtraTolerancePct
	}
	chains := cfg.Chains
	if len(chains) == 0 {
		chains = chainsFromManifest(cfg.Dir)
	}

	results := make([]ChainResult, 0, len(chains))
	for _, chain := range chains {
		ourPath := filepath.Join(cfg.Dir, fmt.Sprintf("sanctioned_addresses_%s.txt", chain))
		ours, err := LoadOurAddresses(ourPath)
		if err != nil {
			return Summary{}, fmt.Errorf("load our %s addresses: %w", chain, err)
		}
		ref, notFound, err := FetchReference(ctx, cfg.ReferenceBaseURL, chain, cfg.UserAgent, timeout)
		if err != nil {
			return Summary{}, fmt.Errorf("fetch reference %s: %w", chain, err)
		}
		r := Compare(chain, ours, ref)
		r.RefNotFound = notFound
		results = append(results, r)
	}

	return Evaluate(results, tolerance), nil
}
