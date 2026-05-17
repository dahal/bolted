//go:build darwin

package keychain

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// System returns the macOS-backed Store, which shells out to the
// `security` CLI (preinstalled on every Mac). The runner indirection
// (cmdRunner) is the only thing tests need to swap.
func System() Store { return &macStore{run: defaultRunner} }

// runner is the injection point for `security` invocations. It returns
// stdout, stderr, and the underlying *exec.ExitError if the command
// exited non-zero. A non-nil err on success is treated as a hard
// failure (e.g. command not found).
type runner func(ctx context.Context, name string, args []string, stdin []byte) (stdout, stderr []byte, err error)

// defaultRunner is the production runner; it execs the binary via
// os/exec. We capture stdout and stderr separately so we can pattern-
// match `security`'s "not found" error without losing useful output.
func defaultRunner(ctx context.Context, name string, args []string, stdin []byte) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// macStore is the Store backed by the `security` CLI.
type macStore struct {
	run runner
}

// Get reads the generic password for (service, account). The `-w` flag
// prints just the password to stdout; we strip the trailing newline.
// A missing entry surfaces as exit code 44 with "could not be found"
// on stderr — we translate that to ErrNotFound.
func (s *macStore) Get(service, account string) ([]byte, error) {
	stdout, stderr, err := s.run(context.Background(), "security", []string{
		"find-generic-password",
		"-s", service,
		"-a", account,
		"-w",
	}, nil)
	if err != nil {
		if isMissing(stderr, err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("keychain: find-generic-password: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	// `security -w` appends a trailing newline.
	return bytes.TrimRight(stdout, "\r\n"), nil
}

// Set creates or overwrites the entry. `-U` upserts; without it
// `security` errors when the entry already exists. We pass the
// password via `-w <value>` because `security` does not have a robust
// stdin mode for add-generic-password.
func (s *macStore) Set(service, account string, secret []byte) error {
	_, stderr, err := s.run(context.Background(), "security", []string{
		"add-generic-password",
		"-U",
		"-s", service,
		"-a", account,
		"-w", string(secret),
	}, nil)
	if err != nil {
		return fmt.Errorf("keychain: add-generic-password: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// Delete removes the entry. A missing entry is treated as success so
// the call is idempotent (matches the documented contract in
// keychain.go).
func (s *macStore) Delete(service, account string) error {
	_, stderr, err := s.run(context.Background(), "security", []string{
		"delete-generic-password",
		"-s", service,
		"-a", account,
	}, nil)
	if err != nil {
		if isMissing(stderr, err) {
			return nil
		}
		return fmt.Errorf("keychain: delete-generic-password: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// isMissing detects `security`'s "entry not found" response. `security`
// prints "The specified item could not be found in the keychain." on
// stderr and exits with code 44; we match on the substring (locale-
// independent enough for the supported macOS releases) rather than the
// exit code so we don't drift if Apple renumbers it.
func isMissing(stderr []byte, _ error) bool {
	return strings.Contains(strings.ToLower(string(stderr)), "could not be found")
}
