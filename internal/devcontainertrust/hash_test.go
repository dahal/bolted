package devcontainertrust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
)

// scriptedBackend is a minimal backend.Backend that returns a canned
// ExecResult / error for HashConfig's single `cat` call. Only Exec is
// exercised; the other methods panic so a misuse is loud.
type scriptedBackend struct {
	result backend.ExecResult
	err    error

	gotCmd []string
}

func (s *scriptedBackend) EnsureVM(context.Context, backend.VMSpec) error { panic("not used") }
func (s *scriptedBackend) StartVM(context.Context) error                  { panic("not used") }
func (s *scriptedBackend) StopVM(context.Context) error                   { panic("not used") }
func (s *scriptedBackend) IsRunning(context.Context) (bool, error)        { panic("not used") }
func (s *scriptedBackend) Exec(_ context.Context, cmd []string, _ backend.ExecOpts) (backend.ExecResult, error) {
	s.gotCmd = append([]string(nil), cmd...)
	return s.result, s.err
}
func (s *scriptedBackend) ForwardPort(context.Context, int, int) error { panic("not used") }
func (s *scriptedBackend) UnforwardPort(context.Context, int) error    { panic("not used") }
func (s *scriptedBackend) DeleteVM(context.Context) error              { panic("not used") }

func TestHashConfig_HappyPath(t *testing.T) {
	body := []byte(`{"image":"alpine:3.20","postCreateCommand":"npm install"}`)
	b := &scriptedBackend{result: backend.ExecResult{ExitCode: 0, Stdout: body}}
	hashHex, summary, err := HashConfig(b, "/bolted/repos/api")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	if hashHex != want {
		t.Errorf("hash mismatch: got %s want %s", hashHex, want)
	}
	if !strings.Contains(summary, "alpine:3.20") {
		t.Errorf("expected image in summary, got %q", summary)
	}
	if !strings.Contains(summary, "postCreateCommand") {
		t.Errorf("expected postCreateCommand in summary, got %q", summary)
	}
	// cmd should be `cat /bolted/repos/api/.devcontainer/devcontainer.json`
	if len(b.gotCmd) != 2 || b.gotCmd[0] != "cat" || !strings.HasSuffix(b.gotCmd[1], "/.devcontainer/devcontainer.json") {
		t.Errorf("unexpected cmd: %v", b.gotCmd)
	}
}

func TestHashConfig_NoConfigReturnsSentinel(t *testing.T) {
	b := &scriptedBackend{result: backend.ExecResult{ExitCode: 1}}
	hashHex, summary, err := HashConfig(b, "/bolted/repos/api")
	if !errors.Is(err, ErrNoConfig) {
		t.Errorf("expected ErrNoConfig, got %v", err)
	}
	if hashHex != "" || summary != "" {
		t.Errorf("expected empty hash+summary on no-config, got %q %q", hashHex, summary)
	}
}

func TestHashConfig_ExecError(t *testing.T) {
	want := errors.New("boom")
	b := &scriptedBackend{err: want}
	if _, _, err := HashConfig(b, "/bolted/repos/api"); !errors.Is(err, want) {
		t.Errorf("expected wrapped exec error, got %v", err)
	}
}

func TestSummarize_InvalidJSONFallback(t *testing.T) {
	out := summarize([]byte("not json"))
	if !strings.Contains(out, "could not parse") {
		t.Errorf("expected fallback notice, got %q", out)
	}
	if !strings.Contains(out, "8 bytes") {
		t.Errorf("expected byte count in fallback, got %q", out)
	}
}

func TestSummarize_AllFields(t *testing.T) {
	body := []byte(`{
		"image": "ubuntu:24.04",
		"features": {
			"ghcr.io/devcontainers/features/node:1": {"version": "20"},
			"ghcr.io/devcontainers/features/go:1": "latest",
			"weird-array-feature": [1,2,3],
			"obj-no-version": {"foo": "bar"}
		},
		"postCreateCommand": "make install",
		"postStartCommand": "echo start",
		"postAttachCommand": "echo attach",
		"onCreateCommand": "echo create",
		"updateContentCommand": "echo update",
		"initializeCommand": "echo init",
		"mounts": ["src=local,dst=/data", {"type":"bind","source":"x","target":"/y"}],
		"remoteEnv": {"FOO": "bar", "BAZ": 1},
		"containerEnv": {"HELLO": "world"}
	}`)
	s := summarize(body)
	wantSubstrings := []string{
		"image: ubuntu:24.04",
		"features:",
		"node:1: version=20",
		"go:1: \"latest\"",
		"weird-array-feature: [1,2,3]",
		"obj-no-version:",
		"postCreateCommand: make install",
		"postStartCommand: echo start",
		"postAttachCommand: echo attach",
		"onCreateCommand: echo create",
		"updateContentCommand: echo update",
		"initializeCommand: echo init",
		"mounts:",
		"src=local,dst=/data",
		"remoteEnv:",
		"FOO=bar",
		"BAZ=1",
		"containerEnv:",
		"HELLO=world",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(s, sub) {
			t.Errorf("summary missing %q\nGOT:\n%s", sub, s)
		}
	}
}

func TestSummarize_DockerFileTopLevel(t *testing.T) {
	body := []byte(`{"dockerFile": "Dockerfile.dev"}`)
	s := summarize(body)
	if !strings.Contains(s, "dockerFile: Dockerfile.dev") {
		t.Errorf("expected dockerFile, got %q", s)
	}
}

func TestSummarize_BuildSubObjectWhenNoImage(t *testing.T) {
	body := []byte(`{"build": {"dockerfile": "Dockerfile", "context": "."}}`)
	s := summarize(body)
	if !strings.Contains(s, "build.dockerfile: Dockerfile") || !strings.Contains(s, "build.context: .") {
		t.Errorf("expected build sub-fields, got %q", s)
	}
}

func TestSummarize_BuildIgnoredWhenImagePresent(t *testing.T) {
	body := []byte(`{"image":"alpine", "build": {"dockerfile":"X"}}`)
	s := summarize(body)
	if strings.Contains(s, "build.dockerfile") {
		t.Errorf("expected build.* to be skipped when image is set, got %q", s)
	}
}

func TestSummarize_NonStringStringField(t *testing.T) {
	// `postCreateCommand` is sometimes an array; writeStringField should
	// JSON-encode it rather than dropping it.
	body := []byte(`{"postCreateCommand": ["npm","install"]}`)
	s := summarize(body)
	if !strings.Contains(s, `postCreateCommand: ["npm","install"]`) {
		t.Errorf("expected JSON-encoded value, got %q", s)
	}
}

func TestSummarize_EmptyStringFieldDropped(t *testing.T) {
	body := []byte(`{"image": ""}`)
	s := summarize(body)
	if strings.Contains(s, "image:") {
		t.Errorf("empty string fields should not be rendered, got %q", s)
	}
}

func TestSummarize_EmptyFeaturesAndArraysDropped(t *testing.T) {
	body := []byte(`{"features": {}, "mounts": [], "remoteEnv": {}}`)
	s := summarize(body)
	if strings.Contains(s, "features:") {
		t.Errorf("empty features should not be rendered, got %q", s)
	}
	if strings.Contains(s, "mounts:") {
		t.Errorf("empty mounts should not be rendered, got %q", s)
	}
	if strings.Contains(s, "remoteEnv:") {
		t.Errorf("empty remoteEnv should not be rendered, got %q", s)
	}
}

func TestSummarize_NonArrayMountsFallback(t *testing.T) {
	body := []byte(`{"mounts": "src=local,dst=/data"}`)
	s := summarize(body)
	if !strings.Contains(s, "mounts: ") {
		t.Errorf("expected fallback rendering, got %q", s)
	}
}

func TestSummarize_NonObjectEnvFallback(t *testing.T) {
	body := []byte(`{"remoteEnv": "FOO=bar"}`)
	s := summarize(body)
	if !strings.Contains(s, "remoteEnv: ") {
		t.Errorf("expected fallback rendering, got %q", s)
	}
}

func TestSummarize_ArrayWithObjectItems(t *testing.T) {
	body := []byte(`{"mounts": [{"type":"bind"}]}`)
	s := summarize(body)
	if !strings.Contains(s, `{"type":"bind"}`) {
		t.Errorf("expected JSON-encoded array item, got %q", s)
	}
}

func TestHashCtxFn_DefaultIsBackground(t *testing.T) {
	if hashCtxFn() == nil {
		t.Fatal("hashCtxFn returned nil context")
	}
}
