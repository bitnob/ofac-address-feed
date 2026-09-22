// Command ofac-feed fetches OFAC's SDN advanced XML export, extracts sanctioned
// digital-currency addresses, and writes a consumer-compatible output set.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/bitnob/ofac-address-feed/internal/crosscheck"
	"github.com/bitnob/ofac-address-feed/internal/ofac"
	"github.com/bitnob/ofac-address-feed/internal/shrinkguard"
)

func main() {
	// Subcommand dispatch. The default (no subcommand, or the first argument
	// is a flag) is the existing generate-and-write flow so `ofac-feed
	// -out ...` keeps working unchanged.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "crosscheck":
			if err := runCrosscheck(os.Args[2:]); err != nil {
				log.Fatalf("ofac-feed crosscheck: %v", err)
			}
			return
		case "shrinkguard":
			if err := runShrinkGuard(os.Args[2:]); err != nil {
				log.Fatalf("ofac-feed shrinkguard: %v", err)
			}
			return
		}
	}

	var (
		outDir    = flag.String("out", "out", "output directory")
		sourceURL = flag.String("source-url", ofac.DefaultSourceURL, "OFAC SDN advanced-XML source URL")
		input     = flag.String("input", "", "parse a local XML file instead of fetching (for tests/CI fixtures)")
		userAgent = flag.String("user-agent", "ofac-address-feed/1.0", "HTTP User-Agent (must be non-empty; OFAC WAF blocks blank UAs)")
		timeout   = flag.Duration("timeout", 5*time.Minute, "fetch timeout")
	)
	flag.Parse()

	if err := run(*outDir, *sourceURL, *input, *userAgent, *timeout); err != nil {
		log.Fatalf("ofac-feed: %v", err)
	}
}

func run(outDir, sourceURL, input, userAgent string, timeout time.Duration) error {
	var (
		xmlPath string
		meta    ofac.Meta
		cleanup func()
	)

	if input != "" {
		sum, err := ofac.HashFile(input)
		if err != nil {
			return fmt.Errorf("hash input: %w", err)
		}
		lastMod := ""
		if fi, err := os.Stat(input); err == nil {
			lastMod = fi.ModTime().UTC().Format(time.RFC1123)
		}
		xmlPath = input
		meta = ofac.Meta{SourceURL: "file://" + input, SourceLastModified: lastMod, SourceSHA256: sum}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		path, m, err := ofac.Fetch(ctx, sourceURL, userAgent)
		if err != nil {
			return err
		}
		xmlPath, meta = path, m
		cleanup = func() { os.Remove(path) }
	}
	if cleanup != nil {
		defer cleanup()
	}

	f, err := os.Open(xmlPath)
	if err != nil {
		return err
	}
	defer f.Close()

	entries, err := ofac.Parse(f)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	if err := ofac.Write(entries, outDir, meta, time.Now()); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	log.Printf("ofac-feed: extracted %d addresses -> %s (source last-modified %q, sha256 %s)",
		len(entries), outDir, meta.SourceLastModified, meta.SourceSHA256)
	return nil
}

// runCrosscheck implements `ofac-feed crosscheck`: it compares our
// freshly-generated per-chain address sets in -dir against an independently-
// maintained reference feed and fails loudly on any miss (see
// internal/crosscheck for policy). If no reference feed is configured
// (-reference-url empty), the check is skipped gracefully rather than
// failing — the shrink-guard remains the always-on guard, and the feed must
// still be able to publish without a reference feed configured.
func runCrosscheck(args []string) error {
	fs := flag.NewFlagSet("crosscheck", flag.ExitOnError)
	dir := fs.String("dir", "out", "directory containing our sanctioned_addresses_{CHAIN}.txt files and manifest.json")
	referenceURL := fs.String("reference-url", "", "base URL of an independent reference feed to cross-check against (e.g. https://example.com/lists); per-chain files are expected at {reference-url}/sanctioned_addresses_{CHAIN}.json. If empty, the cross-check is skipped.")
	chainsFlag := fs.String("chains", "", "comma-separated chain codes to check (default: our manifest's chains)")
	userAgent := fs.String("user-agent", crosscheck.DefaultUserAgent, "HTTP User-Agent for reference fetches")
	timeout := fs.Duration("timeout", 30*time.Second, "per-chain reference fetch timeout")
	tolerance := fs.Float64("extra-tolerance-pct", crosscheck.DefaultExtraTolerancePct, "allowed extras as a percentage of the reference chain's own count before failing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var chains []string
	for _, c := range strings.Split(*chainsFlag, ",") {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
			chains = append(chains, c)
		}
	}

	return crosscheckOrSkip(crosscheck.Config{
		Dir:               *dir,
		ReferenceBaseURL:  *referenceURL,
		Chains:            chains,
		UserAgent:         *userAgent,
		Timeout:           *timeout,
		ExtraTolerancePct: *tolerance,
	})
}

// crosscheckOrSkip runs the cross-check against cfg.ReferenceBaseURL, or —
// when it's empty, meaning no reference feed is configured — logs and
// returns nil so the caller treats it as a successful (skipped) step.
func crosscheckOrSkip(cfg crosscheck.Config) error {
	if cfg.ReferenceBaseURL == "" {
		log.Print("crosscheck: no reference feed configured; skipping")
		return nil
	}

	summary, err := crosscheck.Run(context.Background(), cfg)
	if err != nil {
		return err
	}

	fmt.Print(crosscheck.FormatSummary(summary))
	if !summary.Passed {
		return fmt.Errorf("cross-check against reference feed failed (see report above)")
	}
	return nil
}

// runShrinkGuard implements `ofac-feed shrinkguard`: it fails the run rather
// than let a collapsed list (0 addresses, or an implausible drop) get
// published, so a source outage or parser regression can't ship a stale-and-
// silently-wrong sanctions list.
func runShrinkGuard(args []string) error {
	fs := flag.NewFlagSet("shrinkguard", flag.ExitOnError)
	current := fs.String("current", "out/manifest.json", "path to the freshly generated manifest.json")
	previous := fs.String("previous", "", "path to the previously-published manifest.json (empty or missing = no baseline, e.g. first publish)")
	threshold := fs.Float64("threshold-pct", shrinkguard.DefaultThresholdPct, "maximum allowed percentage drop in total before failing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	curManifest, err := shrinkguard.LoadManifest(*current)
	if err != nil {
		return fmt.Errorf("load current manifest %s: %w", *current, err)
	}

	prevTotal := 0
	if *previous != "" {
		prevManifest, err := shrinkguard.LoadManifest(*previous)
		switch {
		case err == nil:
			prevTotal = prevManifest.Total
		case os.IsNotExist(err):
			log.Printf("ofac-feed shrinkguard: no previous manifest at %s (first publish?) — skipping drop comparison", *previous)
		default:
			return fmt.Errorf("load previous manifest %s: %w", *previous, err)
		}
	}

	result := shrinkguard.Check(prevTotal, curManifest.Total, *threshold)
	log.Printf("ofac-feed shrinkguard: previous=%d new=%d drop=%.1f%% threshold=%.1f%%",
		result.PreviousTotal, result.NewTotal, result.DropPct, result.ThresholdPct)
	if !result.Passed {
		return fmt.Errorf("%s", result.Reason)
	}
	log.Printf("ofac-feed shrinkguard: OK")
	return nil
}
