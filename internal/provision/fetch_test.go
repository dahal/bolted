package provision

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetchYAML_LocalPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bolted.yaml")
	want := []byte("features: []\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := FetchYAML(path)
	if err != nil {
		t.Fatalf("FetchYAML: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFetchYAML_LocalMissing(t *testing.T) {
	_, err := FetchYAML(filepath.Join(t.TempDir(), "nope"))
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Errorf("expected read err, got %v", err)
	}
}

func TestFetchYAML_Empty(t *testing.T) {
	_, err := FetchYAML("")
	if err == nil || !strings.Contains(err.Error(), "empty path") {
		t.Errorf("expected empty path err, got %v", err)
	}
}

func TestFetchYAML_RejectsHTTP(t *testing.T) {
	_, err := FetchYAML("http://example.com/x.yaml")
	if err == nil || !strings.Contains(err.Error(), "insecure http") {
		t.Errorf("expected insecure-http err, got %v", err)
	}
}

func TestFetchYAML_HTTPS_OK(t *testing.T) {
	body := []byte("features: []\n")
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)

	orig := httpClient
	t.Cleanup(func() { httpClient = orig })
	httpClient = ts.Client()
	httpClient.Timeout = 5 * time.Second

	// Convert https://... since FetchYAML routes by prefix.
	got, err := FetchYAML(ts.URL + "/file.yaml")
	if err != nil {
		t.Fatalf("FetchYAML: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body mismatch: %q", got)
	}
}

func TestFetchYAML_HTTPS_Non200(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	orig := httpClient
	t.Cleanup(func() { httpClient = orig })
	httpClient = ts.Client()

	_, err := FetchYAML(ts.URL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("expected HTTP 404 err, got %v", err)
	}
}

func TestFetchYAML_HTTPS_GetError(t *testing.T) {
	// Point at a port nothing is listening on. We swap to a client
	// with a tight timeout so the test stays fast.
	orig := httpClient
	t.Cleanup(func() { httpClient = orig })
	httpClient = &http.Client{Timeout: 100 * time.Millisecond, Transport: &errTransport{}}

	_, err := FetchYAML("https://example.invalid/")
	if err == nil || !strings.Contains(err.Error(), "GET") {
		t.Errorf("expected GET err, got %v", err)
	}
}

func TestFetchYAML_HTTPS_BodyReadError(t *testing.T) {
	// A server that sends a Content-Length but truncates the body
	// triggers io.ReadAll to surface an unexpected EOF.
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
		// hijack and close prematurely
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(ts.Close)

	orig := httpClient
	t.Cleanup(func() { httpClient = orig })
	httpClient = ts.Client()

	_, err := FetchYAML(ts.URL)
	if err == nil {
		t.Error("expected body-read error")
	}
}

// errTransport always errors. Used to force the http.Client.Get failure
// path without depending on real DNS/network behavior.
type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("forced transport failure")
}
