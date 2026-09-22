package crosscheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCompareNoDivergence(t *testing.T) {
	r := Compare("XBT", []string{"addr1", "addr2"}, []string{"addr2", "addr1"})
	if len(r.Misses) != 0 || len(r.Extras) != 0 {
		t.Fatalf("expected no divergence, got misses=%v extras=%v", r.Misses, r.Extras)
	}
	if r.OurCount != 2 || r.RefCount != 2 {
		t.Fatalf("unexpected counts: our=%d ref=%d", r.OurCount, r.RefCount)
	}
}

func TestCompareMisses(t *testing.T) {
	// Reference has an address we don't — the critical direction.
	r := Compare("XBT", []string{"addr1"}, []string{"addr1", "addr2", "addr3"})
	want := []string{"addr2", "addr3"}
	if !reflect.DeepEqual(r.Misses, want) {
		t.Errorf("Misses: got %v want %v", r.Misses, want)
	}
	if len(r.Extras) != 0 {
		t.Errorf("unexpected extras: %v", r.Extras)
	}
}

func TestCompareExtras(t *testing.T) {
	// We have addresses the reference doesn't — expected/tolerated direction.
	r := Compare("ETH", []string{"0xaaa", "0xbbb", "0xccc"}, []string{"0xaaa"})
	want := []string{"0xbbb", "0xccc"}
	if !reflect.DeepEqual(r.Extras, want) {
		t.Errorf("Extras: got %v want %v", r.Extras, want)
	}
	if len(r.Misses) != 0 {
		t.Errorf("unexpected misses: %v", r.Misses)
	}
}

func TestCompareEVMCaseInsensitive(t *testing.T) {
	ours := []string{"0xABCDEF0000000000000000000000000000000A"}
	ref := []string{"0xabcdef0000000000000000000000000000000a"}
	r := Compare("ETH", ours, ref)
	if len(r.Misses) != 0 {
		t.Errorf("expected case-insensitive EVM match, got misses=%v", r.Misses)
	}
	if len(r.Extras) != 0 {
		t.Errorf("expected case-insensitive EVM match, got extras=%v", r.Extras)
	}
}

func TestCompareNonEVMCaseSensitive(t *testing.T) {
	// Base58/bech32 case is significant; differing case must NOT be treated as
	// a match — it should show up as both a miss and an extra.
	ours := []string{"TAbbVaBKgH4VBLXgWqACuwoKF4cH1HinQh"}
	ref := []string{"tabbvabkgh4vblxgwqacuwokf4ch1hinqh"}
	r := Compare("TRX", ours, ref)
	if len(r.Misses) != 1 {
		t.Errorf("expected 1 miss for case-differing non-EVM address, got %v", r.Misses)
	}
	if len(r.Extras) != 1 {
		t.Errorf("expected 1 extra for case-differing non-EVM address, got %v", r.Extras)
	}
}

func TestCompareDedupsWithinSet(t *testing.T) {
	r := Compare("ETH", []string{"0xaaa", "0xAAA"}, []string{"0xaaa"})
	if r.OurCount != 1 {
		t.Errorf("expected dedup to collapse case-variant duplicates: got OurCount=%d", r.OurCount)
	}
	if len(r.Misses) != 0 || len(r.Extras) != 0 {
		t.Errorf("expected no divergence after dedup: misses=%v extras=%v", r.Misses, r.Extras)
	}
}

func TestEvaluatePassesOnCleanMatch(t *testing.T) {
	results := []ChainResult{
		{Chain: "XBT", OurCount: 2, RefCount: 2},
	}
	s := Evaluate(results, DefaultExtraTolerancePct)
	if !s.Passed {
		t.Fatalf("expected pass, got reasons: %v", s.Reasons)
	}
}

func TestEvaluateFailsOnAnyMiss(t *testing.T) {
	results := []ChainResult{
		{Chain: "XBT", OurCount: 1, RefCount: 2, Misses: []string{"addr2"}},
	}
	s := Evaluate(results, 100) // even a huge tolerance must not save a miss
	if s.Passed {
		t.Fatal("expected fail on miss, got pass")
	}
	if len(s.Reasons) != 1 {
		t.Fatalf("expected 1 reason, got %v", s.Reasons)
	}
}

func TestEvaluateTreatsExtrasAsymmetrically(t *testing.T) {
	// 1 extra out of 100 reference addresses is well within a 20% tolerance.
	within := []ChainResult{
		{Chain: "ETH", OurCount: 101, RefCount: 100, Extras: make([]string, 1)},
	}
	if s := Evaluate(within, 20); !s.Passed {
		t.Errorf("expected small extras within tolerance to pass, got reasons: %v", s.Reasons)
	}

	// 30 extras out of 100 reference addresses exceeds a 20% tolerance.
	beyond := []ChainResult{
		{Chain: "ETH", OurCount: 130, RefCount: 100, Extras: make([]string, 30)},
	}
	if s := Evaluate(beyond, 20); s.Passed {
		t.Errorf("expected extras beyond tolerance to fail")
	}
}

func TestEvaluateNoBaselineNeverFailsOnExtras(t *testing.T) {
	// RefCount == 0 (e.g. reference 404s for a chain we cover) must never fail
	// on extras alone — there's no baseline to have drifted from.
	results := []ChainResult{
		{Chain: "USDT", OurCount: 50, RefCount: 0, Extras: make([]string, 50), RefNotFound: true},
	}
	s := Evaluate(results, 20)
	if !s.Passed {
		t.Errorf("expected pass when reference has no baseline for the chain, got reasons: %v", s.Reasons)
	}
}

func TestFetchReferenceOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("expected non-empty User-Agent")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`["addr1","addr2"]`))
	}))
	defer srv.Close()

	addrs, notFound, err := FetchReference(context.Background(), srv.URL, "ETH", "test-agent/1.0", 5*time.Second)
	if err != nil {
		t.Fatalf("FetchReference: %v", err)
	}
	if notFound {
		t.Fatal("unexpected notFound=true")
	}
	want := []string{"addr1", "addr2"}
	if !reflect.DeepEqual(addrs, want) {
		t.Errorf("addrs: got %v want %v", addrs, want)
	}
}

func TestFetchReference404IsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	addrs, notFound, err := FetchReference(context.Background(), srv.URL, "ZZZ", "test-agent/1.0", 5*time.Second)
	if err != nil {
		t.Fatalf("expected 404 to be handled without error, got: %v", err)
	}
	if !notFound {
		t.Fatal("expected notFound=true for a 404 response")
	}
	if addrs != nil {
		t.Errorf("expected nil addrs on 404, got %v", addrs)
	}
}

func TestFetchReferenceServerErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, err := FetchReference(context.Background(), srv.URL, "ETH", "test-agent/1.0", 5*time.Second)
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestFetchReferenceBuildsExpectedURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if _, _, err := FetchReference(context.Background(), srv.URL, "TRX", "ua", 5*time.Second); err != nil {
		t.Fatalf("FetchReference: %v", err)
	}
	if gotPath != "/sanctioned_addresses_TRX.json" {
		t.Errorf("unexpected reference URL path: got %q", gotPath)
	}
}

func TestFetchReferenceBaseURLTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if _, _, err := FetchReference(context.Background(), srv.URL+"/", "TRX", "ua", 5*time.Second); err != nil {
		t.Fatalf("FetchReference: %v", err)
	}
	if gotPath != "/sanctioned_addresses_TRX.json" {
		t.Errorf("unexpected reference URL path with trailing-slash base: got %q", gotPath)
	}
}

// TestRunWithConfiguredReferenceFailsOnMiss exercises the full Run path
// against a mock reference server (standing in for the independent reference
// feed): the chain universe comes from our own manifest.json, and a chain the
// reference has an address for that we don't must fail the run.
func TestRunWithConfiguredReferenceFailsOnMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sanctioned_addresses_ETH.json":
			w.Write([]byte(`["0xaaa","0xbbb"]`))
		case "/sanctioned_addresses_XBT.json":
			w.Write([]byte(`["addr1"]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"per_chain_counts":{"ETH":1,"XBT":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// We only have one of ETH's two reference addresses -> a miss.
	if err := os.WriteFile(filepath.Join(dir, "sanctioned_addresses_ETH.txt"),
		[]byte("0xaaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sanctioned_addresses_XBT.txt"),
		[]byte("addr1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := Run(context.Background(), Config{
		Dir:              dir,
		ReferenceBaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Passed {
		t.Fatalf("expected Run to fail on the ETH miss, got pass: %+v", summary)
	}
}

// TestRunWithConfiguredReferencePassesOnCleanMatch is the mirror-image case:
// our sets exactly match the mock reference feed, so the run must pass.
func TestRunWithConfiguredReferencePassesOnCleanMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sanctioned_addresses_ETH.json":
			w.Write([]byte(`["0xaaa"]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"per_chain_counts":{"ETH":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sanctioned_addresses_ETH.txt"),
		[]byte("0xaaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, err := Run(context.Background(), Config{
		Dir:              dir,
		ReferenceBaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !summary.Passed {
		t.Fatalf("expected Run to pass on clean match, got: %+v", summary)
	}
}
