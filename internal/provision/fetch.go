package provision

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient is the var indirection point that lets tests stub the
// network without spinning up a real test server. The default has a
// 30-second timeout — `bolted.yaml` is small (kilobytes), so any
// fetch that takes longer is almost certainly a hung connection.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// FetchYAML retrieves a bolted.yaml from either an https URL or a
// local filesystem path. The decision is made by prefix: anything
// starting with "https://" is treated as a URL; anything starting with
// "http://" is rejected (an insecure transport for trust-anchor data is
// a footgun); everything else is treated as a path.
//
// On success returns the file bytes verbatim — callers run Load on the
// result if they want a parsed *BoltedProfile, or pass the bytes to
// Save to persist them as `~/.bolted/bolted.yaml`.
func FetchYAML(urlOrPath string) ([]byte, error) {
	if urlOrPath == "" {
		return nil, fmt.Errorf("provision: FetchYAML: empty path")
	}
	if strings.HasPrefix(urlOrPath, "http://") {
		return nil, fmt.Errorf("provision: FetchYAML: refusing insecure http:// — use https:// or a local path")
	}
	if strings.HasPrefix(urlOrPath, "https://") {
		return fetchHTTPS(urlOrPath)
	}
	data, err := readFileFn(urlOrPath)
	if err != nil {
		return nil, fmt.Errorf("provision: FetchYAML: read %s: %w", urlOrPath, err)
	}
	return data, nil
}

// fetchHTTPS does a single GET and returns the body. Non-2xx is an
// error; we surface the status code so the caller can show a useful
// diagnostic.
func fetchHTTPS(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("provision: FetchYAML: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provision: FetchYAML: GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("provision: FetchYAML: read body %s: %w", url, err)
	}
	return body, nil
}
