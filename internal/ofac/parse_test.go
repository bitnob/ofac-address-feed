package ofac

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

const fixture = "testdata/sdn_advanced_sample.xml"

func parseFixture(t *testing.T) []Entry {
	t.Helper()
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	entries, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return entries
}

func TestParseFixtureExactEntries(t *testing.T) {
	got := parseFixture(t)

	// Sorted by (chain, address). Covers: ETH mixed-case normalization, a
	// two-address VersionDetail (26101), Bitcoin (XBT) base58, Tron, dedup of the
	// same ETH address across two parties (keeps party 1001), and a prose
	// VersionDetail (26202) that is skipped.
	want := []Entry{
		{Address: "0x1111111111111111111111111111111111111111", Name: "EVIL CORP", Program: "CYBER2", Chain: "ETH", PartyID: "1001"},
		{Address: "0x2222222222222222222222222222222222222222", Name: "EVIL CORP", Program: "CYBER2", Chain: "ETH", PartyID: "1001"},
		{Address: "0x8589427373d6d84e98730d7795d8f6f8731fda16", Name: "EVIL CORP", Program: "CYBER2", Chain: "ETH", PartyID: "1001"},
		{Address: "TAbbVaBKgH4VBLXgWqACuwoKF4cH1HinQh", Name: "BAD ACTOR", Program: "DPRK3", Chain: "TRX", PartyID: "1002"},
		{Address: "12aNKp2iDKuhEde2YfPdd4DFGenRUTKupL", Name: "EVIL CORP", Program: "CYBER2", Chain: "XBT", PartyID: "1001"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestParseNormalizesEVMToLowercase(t *testing.T) {
	got := parseFixture(t)
	for _, e := range got {
		if strings.HasPrefix(e.Address, "0x") && e.Address != strings.ToLower(e.Address) {
			t.Errorf("EVM address not lowercased: %q", e.Address)
		}
	}
}

func TestParseDedupKeepsSingleAcrossParties(t *testing.T) {
	got := parseFixture(t)
	n := 0
	for _, e := range got {
		if e.Address == "0x8589427373d6d84e98730d7795d8f6f8731fda16" {
			n++
			if e.PartyID != "1001" {
				t.Errorf("dedup kept wrong party: got %q want 1001", e.PartyID)
			}
		}
	}
	if n != 1 {
		t.Errorf("dedup failed: found %d copies of shared ETH address, want 1", n)
	}
}

func TestExtractAddressesMultiValue(t *testing.T) {
	addrs, skipped := extractAddresses("  0x1111111111111111111111111111111111111111 , 0x2222222222222222222222222222222222222222 ")
	want := []string{
		"0x1111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222",
	}
	if !reflect.DeepEqual(addrs, want) {
		t.Errorf("multi-value split: got %v want %v", addrs, want)
	}
	if len(skipped) != 0 {
		t.Errorf("unexpected skips: %v", skipped)
	}
}

func TestExtractAddressesProseSkipped(t *testing.T) {
	addrs, skipped := extractAddresses("See OFAC website for updated address")
	if len(addrs) != 0 {
		t.Errorf("prose emitted as address: %v", addrs)
	}
	if len(skipped) == 0 {
		t.Errorf("prose not logged as skipped")
	}
}

func TestExtractAddressesTrimsWhitespace(t *testing.T) {
	addrs, _ := extractAddresses("\t 12aNKp2iDKuhEde2YfPdd4DFGenRUTKupL \n")
	if len(addrs) != 1 || addrs[0] != "12aNKp2iDKuhEde2YfPdd4DFGenRUTKupL" {
		t.Errorf("whitespace trim failed: %v", addrs)
	}
}

func TestNormalizeAddressPreservesNonEVM(t *testing.T) {
	// Base58/bech32 case is significant; must not be altered.
	in := "TAbbVaBKgH4VBLXgWqACuwoKF4cH1HinQh"
	if got := normalizeAddress(in); got != in {
		t.Errorf("non-EVM altered: got %q want %q", got, in)
	}
}

func TestParseFailsClosedOnMissingFeatureTypes(t *testing.T) {
	// No FeatureType reference set -> must error, never return an empty list.
	doc := `<?xml version="1.0" encoding="utf-8"?>
<Sanctions xmlns="https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/ADVANCED_XML">
  <DistinctParties/>
</Sanctions>`
	if _, err := Parse(strings.NewReader(doc)); err == nil {
		t.Fatal("expected error when no crypto feature types present, got nil")
	}
}

func TestParseToleratesBOM(t *testing.T) {
	f, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, f...)
	entries, err := Parse(strings.NewReader(string(withBOM)))
	if err != nil {
		t.Fatalf("Parse with BOM: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("BOM parse: got %d entries want 5", len(entries))
	}
}
