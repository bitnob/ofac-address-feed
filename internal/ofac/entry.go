// Package ofac parses OFAC's SDN "advanced" XML export and extracts sanctioned
// digital-currency addresses.
//
// Correctness is the overriding goal: dropping a genuine sanctioned address is a
// sanctions miss (the worst possible failure), so the extractor is deliberately
// biased toward keeping anything address-shaped and logging the uncertain cases
// rather than silently discarding them.
package ofac

// Entry is a single sanctioned digital-currency address.
//
// The JSON shape is drop-in compatible with the consuming KYT screening
// service's address-entry format (json tags "address", "name", "program"); the
// consumer ignores the extra "chain" field. PartyID is internal bookkeeping and
// is never emitted.
type Entry struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
	Program string `json:"program,omitempty"`
	Chain   string `json:"chain"`
	PartyID string `json:"-"`
}

// Meta describes the provenance of a parsed source document. It feeds manifest.json.
type Meta struct {
	SourceURL          string
	SourceLastModified string
	SourceSHA256       string
}
