# ofac-address-feed

OFAC sanctioned digital-currency address feed.

Address-screening workflows need a list of OFAC-sanctioned crypto addresses.
It parses the U.S. Treasury OFAC SDN "advanced" XML export — the authoritative
source — directly, and publishes a normalized, version-controlled address list.
Sourcing straight from OFAC keeps the extraction logic, update cadence, and
freshness guarantees explicit and auditable.

## Source

OFAC's authoritative SDN advanced-XML export:

```
https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/sdn_advanced.xml
```

The tool streams and parses this document (never loading the whole ~80MB file
into memory at once), extracting every `Digital Currency Address - <CODE>`
feature from every `DistinctParty`, along with the party's primary display
name and the sanctions program(s) it's listed under.

## Running locally

```console
$ go build -o ofac-feed ./cmd/ofac-feed
$ ./ofac-feed -out out
```

Flags:

| Flag           | Default                          | Purpose                                                                |
|----------------|-----------------------------------|-------------------------------------------------------------------------|
| `-out`         | `out`                              | Output directory                                                        |
| `-source-url`  | OFAC's SDN advanced-XML export URL | Override the source (e.g. for testing against a mirror)                 |
| `-input`       | *(empty)*                          | Parse a local XML file instead of fetching (useful for tests/CI fixtures)|
| `-user-agent`  | `ofac-address-feed/1.0`            | HTTP User-Agent — OFAC's WAF blocks blank UAs                           |
| `-timeout`     | `5m`                               | Fetch timeout                                                           |

## Output format

Running the tool writes three files into the output directory:

- **`sanctioned_addresses.json`** — a flat JSON array of entries, sorted by
  `(chain, address)`:

  ```json
  [
    { "address": "0x1111...", "name": "EVIL CORP", "program": "CYBER2", "chain": "ETH" }
  ]
  ```

- **`sanctioned_addresses_{CHAIN}.txt`** — one address per line, per chain
  (chain codes are OFAC-raw, e.g. `XBT`, `ETH`, `TRX`, `USDT`), sorted.

- **`manifest.json`** — provenance and counts:

  ```json
  {
    "generated_at": "2026-09-22T00:00:00Z",
    "source_url": "https://sanctionslistservice.ofac.treas.gov/...",
    "source_last_modified": "Mon, 21 Sep 2026 14:01:59 GMT",
    "source_sha256": "…",
    "total": 1234,
    "per_chain_counts": { "ETH": 600, "XBT": 500, "TRX": 100, "USDT": 34 }
  }
  ```

EVM (`0x…`) addresses are normalized to lowercase for case-insensitive
matching; every other chain's address is preserved verbatim, since case is
significant for base58/bech32/base58check formats.

The `main` branch is code-only; the `lists` branch is a dedicated data
branch, force-updated in place each night by CI, containing only the
generated `out/` artifacts.

## Consuming the feed

The generated list is published to the `lists` branch and can be fetched
directly:

```
https://raw.githubusercontent.com/bitnob/ofac-address-feed/lists/sanctioned_addresses.json
```

The per-chain files (`sanctioned_addresses_{CHAIN}.txt`) and `manifest.json`
are published alongside it on the same branch.

## Guarantees: shrink-guard and cross-check

A silently-failing nightly job that keeps serving a stale (or empty) sanctions
list is the exact failure mode this feed is built to avoid. Two guards run
before every publish, and either one failing blocks the publish and pages the
team — see `.github/workflows/update-list.yml`.

### Shrink-guard (`ofac-feed shrinkguard`)

Compares the freshly generated `manifest.json` total against the
currently-published one. Fails (and does **not** publish — last-good stays
live) when:

- the new total is **0**, or
- the new total drops by more than a configurable percentage (default **15%**)
  versus the previous total.

A first-ever publish (no prior manifest to compare against) always passes,
provided the new total is non-zero.

```console
$ ofac-feed shrinkguard -current out/manifest.json -previous /path/to/prior/manifest.json [-threshold-pct 15]
```

### Cross-check (`ofac-feed crosscheck`)

Cross-checks the generated per-chain address sets against an **independent
reference feed** (configured via `-reference-url`; skipped when unset), per
chain, in both directions — but treats them asymmetrically:

- **Misses** — addresses the reference feed has that we don't — are the
  critical, near-zero-tolerance direction (a miss is a sanctioned address
  we'd fail to screen). **Any miss fails the check.**
- **Extras** — addresses we have that the reference feed doesn't — are
  expected in normal operation (we parse the SDN XML directly and may pick up
  names, chains, or entries the reference extractor skips), so a small
  tolerance is allowed, expressed as a percentage of the reference chain's
  own count (default **20%**).

Comparison is case-insensitive for `0x`/EVM addresses (both sides normalized)
and exact for every other chain. A `404` from the reference feed for a given
chain is treated as a signal to report (the reference feed simply doesn't
publish that chain), not a crash.

The reference feed is expected to publish the same flat-JSON-array file shape
we do, at `{reference-url}/sanctioned_addresses_{CHAIN}.json`. If
`-reference-url` is left empty, the cross-check is skipped (logged, exit 0) —
the shrink-guard above remains the always-on guard, and the feed can still
publish without a reference feed configured.

```console
$ ofac-feed crosscheck -dir out -reference-url https://example.com/lists [-extra-tolerance-pct 20] [-chains XBT,ETH,TRX]
```

## Nightly publishing

`.github/workflows/update-list.yml` runs nightly (and on manual dispatch):
build → generate → shrink-guard → cross-check → publish `out/` to the `lists`
branch (only committing when content actually changed). Any failure anywhere
in that chain fails the job loudly and opens (or updates) a tracking issue, so
a stale or empty list is never served silently.

## Development

Standard-library-only Go (no external dependencies). To build and test:

```console
$ go build ./...
$ go vet ./...
$ go test ./...
```

Parser behaviour is pinned by golden fixtures under `internal/ofac/testdata/`
(a trimmed `sdn_advanced.xml` sample and the expected `sanctioned_addresses.json`
output). Regenerate the golden output after an intentional format change with
`go test ./internal/ofac -run TestWrite -update`.

## Disclaimer

This tool republishes public data from the U.S. Treasury's OFAC SDN list. It is
provided as-is, with no warranty (see `LICENSE`), and is **not** legal advice or
an authoritative sanctions determination. OFAC's own published list is the
sole authoritative source; consumers should treat this feed as a convenience
mirror and retain their own independent controls (for example, a local floor
list plus an ingest-side shrink-guard).

## License

[MIT](LICENSE) © Bitnob.
