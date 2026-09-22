package ofac

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
)

// featureTypeRe matches the reference-value-set label for a crypto address type,
// e.g. "Digital Currency Address - ETH". The code is captured generically so new
// chains are picked up automatically without a hardcoded allow-list.
var featureTypeRe = regexp.MustCompile(`^Digital Currency Address - (.+)$`)

// evmRe matches a well-formed 0x EVM address (20 bytes hex).
var evmRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// genericAddrRe is a deliberately permissive "looks like a crypto address" check:
// a single run of 20-120 alphanumerics with no spaces or punctuation. Real
// addresses across every supported chain fall inside this range (XRP ~25, BTC
// 26-35, Tron 34, ETH 42, Solana 32-44, Monero ~95). Prose words are shorter or
// contain spaces/punctuation and are excluded. We bias toward keeping so that a
// slightly-off address is retained (and logged) rather than dropped.
var genericAddrRe = regexp.MustCompile(`^[A-Za-z0-9]{20,120}$`)

// addrSplitRe splits a VersionDetail that packs multiple addresses into one field
// separated by commas, semicolons, or whitespace.
var addrSplitRe = regexp.MustCompile(`[,;\s]+`)

// --- XML binding structs (each is DecodeElement'd from a small bounded subtree;
// the whole 80MB document is never unmarshaled at once) ---

type featureType struct {
	ID   string `xml:"ID,attr"`
	Text string `xml:",chardata"`
}

type distinctParty struct {
	FixedRef string  `xml:"FixedRef,attr"`
	Profile  profile `xml:"Profile"`
}

type profile struct {
	ID         string     `xml:"ID,attr"`
	Identities []identity `xml:"Identity"`
	Features   []feature  `xml:"Feature"`
}

type identity struct {
	Primary string  `xml:"Primary,attr"`
	Aliases []alias `xml:"Alias"`
}

type alias struct {
	Primary        string           `xml:"Primary,attr"`
	DocumentedName []documentedName `xml:"DocumentedName"`
}

type documentedName struct {
	Parts []struct {
		Value string `xml:"NamePartValue"`
	} `xml:"DocumentedNamePart"`
}

type feature struct {
	FeatureTypeID string `xml:"FeatureTypeID,attr"`
	Versions      []struct {
		Detail string `xml:"VersionDetail"`
	} `xml:"FeatureVersion"`
}

type sanctionsEntry struct {
	ProfileID string `xml:"ProfileID,attr"`
	Measures  []struct {
		Comment string `xml:"Comment"`
	} `xml:"SanctionsMeasure"`
}

// rawFeature is an address-bearing feature captured during the streaming pass,
// before currency resolution and normalization.
type rawFeature struct {
	partyID       string
	name          string
	featureTypeID string
	detail        string
}

// Parse streams the OFAC advanced XML and returns the extracted, validated,
// de-duplicated set of sanctioned digital-currency addresses.
//
// It parses token-by-token, unmarshaling only the small ReferenceValueSets
// entries, one DistinctParty, or one SanctionsEntry at a time — never the whole
// document. Currency and program are resolved after the pass so the result does
// not depend on section ordering within the file.
func Parse(r io.Reader) ([]Entry, error) {
	br := skipBOM(bufio.NewReader(r))

	dec := xml.NewDecoder(br)
	// Explicitly reject anything that isn't UTF-8/ASCII rather than silently
	// mis-decoding. The advanced export is always utf-8.
	dec.CharsetReader = utf8CharsetReader

	featureTypes := map[string]string{}          // FeatureTypeID -> currency code
	programs := map[string]map[string]struct{}{} // ProfileID -> set of program names
	var raws []rawFeature
	sawDistinctPartyBeforeMap := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml token: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		switch se.Name.Local {
		case "FeatureType":
			var ft featureType
			if err := dec.DecodeElement(&ft, &se); err != nil {
				return nil, fmt.Errorf("decode FeatureType: %w", err)
			}
			if m := featureTypeRe.FindStringSubmatch(strings.TrimSpace(ft.Text)); m != nil && ft.ID != "" {
				featureTypes[ft.ID] = strings.TrimSpace(m[1])
			}

		case "DistinctParty":
			if len(featureTypes) == 0 {
				// The reference set precedes the parties in this format; an empty
				// map here means the schema/namespace regressed. Fail closed
				// rather than silently emit nothing.
				sawDistinctPartyBeforeMap = true
			}
			var dp distinctParty
			if err := dec.DecodeElement(&dp, &se); err != nil {
				return nil, fmt.Errorf("decode DistinctParty: %w", err)
			}
			partyID := dp.Profile.ID
			if partyID == "" {
				partyID = dp.FixedRef
			}
			name := primaryName(dp.Profile)
			for _, f := range dp.Profile.Features {
				if _, isCrypto := featureTypes[f.FeatureTypeID]; !isCrypto {
					continue
				}
				for _, v := range f.Versions {
					raws = append(raws, rawFeature{
						partyID:       partyID,
						name:          name,
						featureTypeID: f.FeatureTypeID,
						detail:        v.Detail,
					})
				}
			}

		case "SanctionsEntry":
			var se2 sanctionsEntry
			if err := dec.DecodeElement(&se2, &se); err != nil {
				return nil, fmt.Errorf("decode SanctionsEntry: %w", err)
			}
			if se2.ProfileID == "" {
				continue
			}
			for _, m := range se2.Measures {
				p := strings.TrimSpace(m.Comment)
				if p == "" {
					continue
				}
				if programs[se2.ProfileID] == nil {
					programs[se2.ProfileID] = map[string]struct{}{}
				}
				programs[se2.ProfileID][p] = struct{}{}
			}
		}
	}

	if len(featureTypes) == 0 {
		return nil, fmt.Errorf("no 'Digital Currency Address' feature types found in ReferenceValueSets: source schema/namespace may have changed")
	}
	if sawDistinctPartyBeforeMap {
		return nil, fmt.Errorf("DistinctParty encountered before feature-type map was built: unexpected document ordering")
	}

	return assemble(raws, featureTypes, programs), nil
}

// assemble resolves currency + program for each raw feature, splits/validates
// addresses, normalizes them, and de-duplicates by (normalized address, chain).
func assemble(raws []rawFeature, featureTypes map[string]string, programs map[string]map[string]struct{}) []Entry {
	seen := map[string]struct{}{} // dedup key: chain\x00address
	var out []Entry

	for _, rf := range raws {
		chain := featureTypes[rf.featureTypeID]
		addrs, skipped := extractAddresses(rf.detail)
		for _, s := range skipped {
			log.Printf("ofac: skipping non-address value in party %s (%s): %q", rf.partyID, chain, s)
		}
		if len(addrs) == 0 && strings.TrimSpace(rf.detail) != "" {
			log.Printf("ofac: no address extracted from party %s (%s) detail: %q", rf.partyID, chain, rf.detail)
		}
		for _, a := range addrs {
			key := chain + "\x00" + a
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, Entry{
				Address: a,
				Name:    rf.name,
				Program: joinPrograms(programs[rf.partyID]),
				Chain:   chain,
				PartyID: rf.partyID,
			})
		}
	}

	sortEntries(out)
	return out
}

// extractAddresses splits a VersionDetail into candidate tokens, keeps those that
// look like addresses (normalizing EVM addresses), and returns the rest as skips.
func extractAddresses(detail string) (addrs, skipped []string) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return nil, nil
	}
	for _, tok := range addrSplitRe.Split(detail, -1) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		switch {
		case evmRe.MatchString(tok):
			addrs = append(addrs, normalizeAddress(tok))
		case genericAddrRe.MatchString(tok):
			// Address-shaped but not an exact EVM address: keep it (never drop a
			// possibly-real address) but normalize in case it is a mixed-case 0x.
			addrs = append(addrs, normalizeAddress(tok))
		default:
			skipped = append(skipped, tok)
		}
	}
	return addrs, skipped
}

// normalizeAddress matches the consuming KYT service's address normalization:
// 0x-prefixed (EVM) addresses are lowercased for case-insensitive matching;
// every other chain's address is preserved verbatim (case is significant for
// base58/bech32).
func normalizeAddress(a string) string {
	a = strings.TrimSpace(a)
	if strings.HasPrefix(strings.ToLower(a), "0x") {
		return strings.ToLower(a)
	}
	return a
}

// primaryName returns the best-effort primary display name for a profile:
// the parts of the first DocumentedName under the primary Alias of the primary
// Identity, joined with spaces. Best-effort — an empty result is acceptable.
func primaryName(p profile) string {
	id := pickPrimaryIdentity(p.Identities)
	if id == nil {
		return ""
	}
	al := pickPrimaryAlias(id.Aliases)
	if al == nil || len(al.DocumentedName) == 0 {
		return ""
	}
	var parts []string
	for _, part := range al.DocumentedName[0].Parts {
		if v := strings.TrimSpace(part.Value); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " ")
}

func pickPrimaryIdentity(ids []identity) *identity {
	for i := range ids {
		if ids[i].Primary == "true" {
			return &ids[i]
		}
	}
	if len(ids) > 0 {
		return &ids[0]
	}
	return nil
}

func pickPrimaryAlias(as []alias) *alias {
	for i := range as {
		if as[i].Primary == "true" {
			return &as[i]
		}
	}
	if len(as) > 0 {
		return &as[0]
	}
	return nil
}

func joinPrograms(set map[string]struct{}) string {
	if len(set) == 0 {
		return ""
	}
	progs := make([]string, 0, len(set))
	for p := range set {
		progs = append(progs, p)
	}
	sort.Strings(progs)
	return strings.Join(progs, "; ")
}

// sortEntries orders deterministically by (chain, address) so nightly diffs stay
// minimal.
func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Chain != entries[j].Chain {
			return entries[i].Chain < entries[j].Chain
		}
		return entries[i].Address < entries[j].Address
	})
}
