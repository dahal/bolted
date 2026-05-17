package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/keychain"
)

// installKeychainStub swaps newKeychainStoreFn for a fake. Mirrors
// the lifecycleStubs pattern.
func installKeychainStub(t *testing.T, store *fakeKeychainStore) {
	t.Helper()
	orig := newKeychainStoreFn
	t.Cleanup(func() { newKeychainStoreFn = orig })
	newKeychainStoreFn = func() keychain.Store { return store }
}

// ---- runKeychainForget -----------------------------------------------------

func TestRunKeychainForget_OK(t *testing.T) {
	store := &fakeKeychainStore{}
	installKeychainStub(t, store)

	var stderr bytes.Buffer
	if err := runKeychainForget(io.Discard, &stderr); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if store.deleteCalls != 1 {
		t.Errorf("expected 1 Delete call, got %d", store.deleteCalls)
	}
	if store.deleteSvc != keychainServiceName {
		t.Errorf("Delete svc = %q, want %q", store.deleteSvc, keychainServiceName)
	}
	if store.deleteAcct != keychainAccountName {
		t.Errorf("Delete acct = %q, want %q", store.deleteAcct, keychainAccountName)
	}
	if !strings.Contains(stderr.String(), "removed") {
		t.Errorf("expected confirmation on stderr, got %q", stderr.String())
	}
}

func TestRunKeychainForget_DeleteError(t *testing.T) {
	want := errors.New("kc fail")
	store := &fakeKeychainStore{deleteErr: want}
	installKeychainStub(t, store)

	err := runKeychainForget(io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped delete error, got %v", err)
	}
}

// ---- Cobra constructor -----------------------------------------------------

func TestNewKeychainCmd_HasForgetSubcommand(t *testing.T) {
	cmd := newKeychainCmd()
	if cmd.Use != "keychain" {
		t.Errorf("Use = %q, want keychain", cmd.Use)
	}
	var saw bool
	for _, sub := range cmd.Commands() {
		if sub.Use == "forget" {
			saw = true
		}
	}
	if !saw {
		t.Error("expected forget subcommand")
	}
}

func TestKeychainForgetCmd_RunE_HappyPath(t *testing.T) {
	store := &fakeKeychainStore{}
	installKeychainStub(t, store)

	cmd := newKeychainCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"forget"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if store.deleteCalls != 1 {
		t.Errorf("expected 1 Delete via cobra, got %d", store.deleteCalls)
	}
}
