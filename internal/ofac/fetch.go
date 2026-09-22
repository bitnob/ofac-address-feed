package ofac

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
)

// DefaultSourceURL is OFAC's authoritative SDN advanced-XML export.
const DefaultSourceURL = "https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/sdn_advanced.xml"

// Fetch downloads the source document to a temp file, streaming it through a
// sha256 hasher so the full body never has to live in memory. It returns the
// temp file path (the caller is responsible for removing it) and provenance
// metadata captured from the response.
//
// A non-empty User-Agent is required: OFAC's WAF blocks blank UAs. Redirects
// (the treas.gov endpoint 302s to a signed S3 URL) are followed by the default
// client, which re-sends the User-Agent header.
func Fetch(ctx context.Context, sourceURL, userAgent string) (path string, meta Meta, err error) {
	if userAgent == "" {
		return "", Meta{}, fmt.Errorf("user-agent must not be empty (OFAC WAF blocks blank UAs)")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", Meta{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/xml, text/xml, */*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", Meta{}, fmt.Errorf("fetch %s: %w", sourceURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", Meta{}, fmt.Errorf("fetch %s: unexpected status %s", sourceURL, resp.Status)
	}

	tmp, err := os.CreateTemp("", "ofac-sdn-*.xml")
	if err != nil {
		return "", Meta{}, err
	}
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()

	h := sha256.New()
	if _, err = io.Copy(tmp, io.TeeReader(resp.Body, h)); err != nil {
		return "", Meta{}, fmt.Errorf("download body: %w", err)
	}

	meta = Meta{
		SourceURL:          sourceURL,
		SourceLastModified: resp.Header.Get("Last-Modified"),
		SourceSHA256:       hex.EncodeToString(h.Sum(nil)),
	}
	return tmp.Name(), meta, nil
}

// HashFile computes the sha256 of a local file, for the --input path where there
// is no HTTP response to hash.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
