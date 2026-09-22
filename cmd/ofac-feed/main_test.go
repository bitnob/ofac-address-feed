package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bitnob/ofac-address-feed/internal/crosscheck"
)

// TestCrosscheckOrSkipWithNoReferenceConfigured verifies that an empty
// -reference-url skips the cross-check gracefully (no error) rather than
// failing the run — the feed must still be able to publish without a
// reference feed configured.
func TestCrosscheckOrSkipWithNoReferenceConfigured(t *testing.T) {
	if err := crosscheckOrSkip(crosscheck.Config{Dir: t.TempDir(), ReferenceBaseURL: ""}); err != nil {
		t.Fatalf("expected skip (nil error) with no reference configured, got: %v", err)
	}
}

// TestCrosscheckOrSkipWithReferenceConfiguredFailsOnMiss verifies that when a
// reference feed base URL is supplied, the cross-check runs for real against
// it and fails when our output misses an address the reference feed has.
func TestCrosscheckOrSkipWithReferenceConfiguredFailsOnMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sanctioned_addresses_ETH.json":
			w.Write([]byte(`["0xaaa","0xbbb"]`))
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

	err := crosscheckOrSkip(crosscheck.Config{Dir: dir, ReferenceBaseURL: srv.URL})
	if err == nil {
		t.Fatal("expected error when the reference feed reports a miss")
	}
}
