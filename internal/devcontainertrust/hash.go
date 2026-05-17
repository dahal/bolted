package devcontainertrust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/dahal/bolted/internal/backend"
)

// hashCtxFn is the indirection point that lets tests skip context plumbing.
// Production code calls HashConfig with a real context.
var hashCtxFn = context.Background

// HashConfig reads <repoPath>/.devcontainer/devcontainer.json inside the VM
// via b.Exec, computes its sha256 (hex), and renders a human-friendly
// summary of the security-relevant fields (image, features, lifecycle
// commands, mounts, env).
//
// If the file does not exist HashConfig returns ("", "", ErrNoConfig) — the
// caller can treat a repo with no devcontainer config as nothing to gate.
// Any other exec or parse error is wrapped and returned.
//
// The hash is computed over the raw file bytes (not the parsed-and-
// re-marshaled tree), so an attacker cannot bypass the gate by adding
// semantically-meaningless whitespace.
func HashConfig(b backend.Backend, repoPath string) (string, string, error) {
	cfgPath := path.Join(repoPath, ".devcontainer", "devcontainer.json")
	res, err := b.Exec(hashCtxFn(), []string{"cat", cfgPath}, backend.ExecOpts{})
	if err != nil {
		return "", "", fmt.Errorf("devcontainertrust: read %s: %w", cfgPath, err)
	}
	if res.ExitCode != 0 {
		// cat returns non-zero when the file doesn't exist (most common
		// case) or on permission errors. We surface the empty hash +
		// ErrNoConfig so the caller can distinguish "no gate needed"
		// from a hard error. The stderr content is dropped on purpose
		// to keep the no-config path quiet.
		return "", "", ErrNoConfig
	}
	data := res.Stdout
	sum := sha256.Sum256(data)
	hex := hex.EncodeToString(sum[:])
	return hex, summarize(data), nil
}

// summarize renders the security-relevant fields of devcontainer.json as a
// stable multi-line block. The format is deliberately human-readable, not
// machine-parseable — it exists only to give the user enough context to
// decide whether to approve.
//
// Devcontainer.json is officially JSON-with-comments, but most files in the
// wild are plain JSON. If parsing fails we fall back to a "could not
// parse" notice plus the byte size; the hash gate still works because it
// hashes the raw bytes.
func summarize(data []byte) string {
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Sprintf("devcontainer.json (%d bytes) — could not parse for summary: %v", len(data), err)
	}
	var b strings.Builder
	b.WriteString("devcontainer.json summary:\n")
	writeStringField(&b, "  image", parsed, "image")
	writeStringField(&b, "  dockerFile", parsed, "dockerFile")
	if image, ok := parsed["image"]; !ok || image == "" {
		// Some files use `build.dockerfile` instead of top-level `dockerFile`.
		if build, ok := parsed["build"].(map[string]any); ok {
			writeStringField(&b, "  build.dockerfile", build, "dockerfile")
			writeStringField(&b, "  build.context", build, "context")
		}
	}
	writeFeatures(&b, parsed)
	writeStringField(&b, "  postCreateCommand", parsed, "postCreateCommand")
	writeStringField(&b, "  postStartCommand", parsed, "postStartCommand")
	writeStringField(&b, "  postAttachCommand", parsed, "postAttachCommand")
	writeStringField(&b, "  onCreateCommand", parsed, "onCreateCommand")
	writeStringField(&b, "  updateContentCommand", parsed, "updateContentCommand")
	writeStringField(&b, "  initializeCommand", parsed, "initializeCommand")
	writeList(&b, "  mounts", parsed, "mounts")
	writeEnv(&b, "  remoteEnv", parsed, "remoteEnv")
	writeEnv(&b, "  containerEnv", parsed, "containerEnv")
	return b.String()
}

// writeStringField renders parsed[key] as a single line if present and
// non-empty. Values that aren't strings are JSON-encoded so the user still
// sees them.
func writeStringField(b *strings.Builder, label string, parsed map[string]any, key string) {
	v, ok := parsed[key]
	if !ok {
		return
	}
	switch s := v.(type) {
	case string:
		if s == "" {
			return
		}
		fmt.Fprintf(b, "%s: %s\n", label, s)
	default:
		raw, _ := json.Marshal(v)
		fmt.Fprintf(b, "%s: %s\n", label, string(raw))
	}
}

// writeFeatures renders the `features` block. Each feature is `<id>: <version>`
// on its own line; sub-options under a feature are emitted as JSON.
func writeFeatures(b *strings.Builder, parsed map[string]any) {
	feats, ok := parsed["features"].(map[string]any)
	if !ok || len(feats) == 0 {
		return
	}
	b.WriteString("  features:\n")
	keys := make([]string, 0, len(feats))
	for k := range feats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := feats[k]
		switch tv := v.(type) {
		case string:
			fmt.Fprintf(b, "    - %s: %q\n", k, tv)
		case map[string]any:
			if version, ok := tv["version"]; ok {
				fmt.Fprintf(b, "    - %s: version=%v\n", k, version)
			} else {
				raw, _ := json.Marshal(tv)
				fmt.Fprintf(b, "    - %s: %s\n", k, string(raw))
			}
		default:
			raw, _ := json.Marshal(v)
			fmt.Fprintf(b, "    - %s: %s\n", k, string(raw))
		}
	}
}

// writeList renders a JSON array field as a bulleted list. Non-array values
// are rendered as JSON on a single line so the user still sees them.
func writeList(b *strings.Builder, label string, parsed map[string]any, key string) {
	v, ok := parsed[key]
	if !ok {
		return
	}
	arr, ok := v.([]any)
	if !ok {
		raw, _ := json.Marshal(v)
		fmt.Fprintf(b, "%s: %s\n", label, string(raw))
		return
	}
	if len(arr) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", label)
	for _, item := range arr {
		switch tv := item.(type) {
		case string:
			fmt.Fprintf(b, "    - %s\n", tv)
		default:
			raw, _ := json.Marshal(item)
			fmt.Fprintf(b, "    - %s\n", string(raw))
		}
	}
}

// writeEnv renders an env-style object (KEY → VALUE) sorted by key.
func writeEnv(b *strings.Builder, label string, parsed map[string]any, key string) {
	v, ok := parsed[key]
	if !ok {
		return
	}
	env, ok := v.(map[string]any)
	if !ok {
		raw, _ := json.Marshal(v)
		fmt.Fprintf(b, "%s: %s\n", label, string(raw))
		return
	}
	if len(env) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", label)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "    - %s=%v\n", k, env[k])
	}
}
