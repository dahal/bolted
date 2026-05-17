//go:build windows

package keychain

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// System returns the Windows-backed Store, which shells out to the
// `cmdkey` CLI (preinstalled on Windows). The runner indirection
// (winRunner) is the only thing tests need to swap.
func System() Store { return &winStore{run: defaultWinRunner} }

// winRunner is the injection point for `cmdkey` invocations. Mirrors
// the darwin runner signature so the two implementations stay close.
type winRunner func(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error)

// defaultWinRunner is the production runner; it execs the binary via
// os/exec and captures stdout / stderr.
func defaultWinRunner(ctx context.Context, name string, args []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// winStore is the Store backed by the `cmdkey` CLI. cmdkey is a
// limited tool — it cannot print stored secrets (Microsoft blocks
// readback for security reasons). So Get always returns ErrNotFound;
// the caller falls back to a password prompt. We still wire Set and
// Delete so future replacement with a true DPAPI binding is a drop-in.
type winStore struct {
	run winRunner
}

// Get is unsupported on Windows via cmdkey: the tool intentionally
// refuses to read back stored secrets. We return ErrNotFound so the
// CLI's "try keychain first, fall back to prompt" flow falls back.
func (s *winStore) Get(service, account string) ([]byte, error) {
	return nil, ErrNotFound
}

// Set stores the password under cmdkey's generic credential target.
// The credential target is "<service>:<account>" so multiple accounts
// per service stay separate.
func (s *winStore) Set(service, account string, secret []byte) error {
	target := service + ":" + account
	_, stderr, err := s.run(context.Background(), "cmdkey", []string{
		"/generic:" + target,
		"/user:" + account,
		"/pass:" + string(secret),
	})
	if err != nil {
		return fmt.Errorf("keychain: cmdkey add: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// Delete removes the credential. cmdkey returns success even for a
// missing entry, so no special-case logic is needed.
func (s *winStore) Delete(service, account string) error {
	target := service + ":" + account
	_, stderr, err := s.run(context.Background(), "cmdkey", []string{
		"/delete:" + target,
	})
	if err != nil {
		// cmdkey exits non-zero with "Element not found" if the entry
		// is already gone. Treat as success for idempotency.
		if strings.Contains(strings.ToLower(string(stderr)), "element not found") {
			return nil
		}
		return fmt.Errorf("keychain: cmdkey delete: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}
