package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/config"
	"github.com/dahal/bolted/internal/keychain"
	"github.com/dahal/bolted/internal/volume"
)

// ---- Fake passwd-volume ----------------------------------------------------

// fakePasswdVolume implements passwdVolume for tests. Each method
// records that it was called and can be configured to fail. Open
// returns sequential results from openResults if non-nil, otherwise
// it returns openDevice / openErr unconditionally.
type fakePasswdVolume struct {
	openCalls   int
	openResults []openResult
	openDevice  volume.Device
	openErr     error

	closeCalls int
	closeErr   error
	// closeErrs, if non-nil, returns one error per Close call (indexed
	// from 0). Used when a test needs only the Nth Close to fail.
	closeErrs []error

	addCalls    int
	addErr      error
	addExisting []byte
	addNew      []byte

	removeCalls    int
	removeErr      error
	removePassword []byte
}

// openResult is one canned (dev, err) pair for an Open invocation.
// Used when the test needs the first Open to succeed and the second
// to fail (or vice-versa).
type openResult struct {
	dev volume.Device
	err error
}

func (f *fakePasswdVolume) Open(_ context.Context, _ string, _ []byte) (volume.Device, error) {
	f.openCalls++
	if len(f.openResults) > 0 {
		idx := f.openCalls - 1
		if idx >= len(f.openResults) {
			idx = len(f.openResults) - 1
		}
		r := f.openResults[idx]
		return r.dev, r.err
	}
	if f.openErr != nil {
		return "", f.openErr
	}
	if f.openDevice == "" {
		return volume.Device("bolted"), nil
	}
	return f.openDevice, nil
}

func (f *fakePasswdVolume) Close(_ context.Context, _ volume.Device) error {
	f.closeCalls++
	if len(f.closeErrs) > 0 {
		idx := f.closeCalls - 1
		if idx >= len(f.closeErrs) {
			idx = len(f.closeErrs) - 1
		}
		return f.closeErrs[idx]
	}
	return f.closeErr
}

func (f *fakePasswdVolume) AddKeyslot(_ context.Context, _ string, existing, new []byte) error {
	f.addCalls++
	f.addExisting = append([]byte(nil), existing...)
	f.addNew = append([]byte(nil), new...)
	return f.addErr
}

func (f *fakePasswdVolume) RemoveKeyslot(_ context.Context, _ string, password []byte) error {
	f.removeCalls++
	f.removePassword = append([]byte(nil), password...)
	return f.removeErr
}

// ---- Fake keychain store ---------------------------------------------------

type fakeKeychainStore struct {
	getResult []byte
	getErr    error

	setErr    error
	setCalls  int
	setSvc    string
	setAcct   string
	setSecret []byte

	deleteErr     error
	deleteCalls   int
	deleteSvc     string
	deleteAcct    string
}

func (f *fakeKeychainStore) Get(service, account string) ([]byte, error) {
	return f.getResult, f.getErr
}

func (f *fakeKeychainStore) Set(service, account string, secret []byte) error {
	f.setCalls++
	f.setSvc = service
	f.setAcct = account
	f.setSecret = append([]byte(nil), secret...)
	return f.setErr
}

func (f *fakeKeychainStore) Delete(service, account string) error {
	f.deleteCalls++
	f.deleteSvc = service
	f.deleteAcct = account
	return f.deleteErr
}

// ---- passwdStubs -----------------------------------------------------------

// passwdStubs is the install helper for the passwd command tests. It
// mirrors lifecycleStubs but configures the wider passwdVolume and
// keychain hooks. Reuses lifecycleStubs for the parts that do overlap
// (loadConfig, backend, prompter).
type passwdStubs struct {
	tempDir   string
	cfg       *config.Config
	cfgErr    error
	backendErr error
	prompter  *fakePrompter
	vol       *fakePasswdVolume
	store     *fakeKeychainStore

	// Set true to make the config's Keychain flag true.
	keychainEnabled bool
}

func (s *passwdStubs) install(t *testing.T) {
	t.Helper()
	if s.tempDir == "" {
		s.tempDir = t.TempDir()
	}
	if s.prompter == nil {
		s.prompter = &fakePrompter{
			promptOnce:  func(string) ([]byte, error) { return []byte("old"), nil },
			promptTwice: func(string) ([]byte, error) { return []byte("new"), nil },
		}
	}
	if s.vol == nil {
		s.vol = &fakePasswdVolume{}
	}
	if s.store == nil {
		s.store = &fakeKeychainStore{}
	}

	origBackend := newBackendFn
	origLoad := loadConfigFn
	origWS := boltedDirFn
	origPrompter := newPrompterFn
	origVol := newPasswdVolumeFn
	origStore := newKeychainStoreFn
	t.Cleanup(func() {
		newBackendFn = origBackend
		loadConfigFn = origLoad
		boltedDirFn = origWS
		newPrompterFn = origPrompter
		newPasswdVolumeFn = origVol
		newKeychainStoreFn = origStore
	})

	newBackendFn = func(_ backend.Config) (backend.Backend, error) {
		if s.backendErr != nil {
			return nil, s.backendErr
		}
		return nil, nil
	}
	loadConfigFn = func(_ string) (*config.Config, error) {
		if s.cfgErr != nil {
			return nil, s.cfgErr
		}
		if s.cfg != nil {
			return s.cfg, nil
		}
		c := config.NewDefault()
		c.Keychain = s.keychainEnabled
		return c, nil
	}
	boltedDirFn = func() string { return s.tempDir }
	newPrompterFn = func() passwordPrompter { return s.prompter }
	newPasswdVolumeFn = func(_ backend.Backend, _ volume.Options) passwdVolume { return s.vol }
	newKeychainStoreFn = func() keychain.Store { return s.store }
}

// ---- runPasswd: happy path -------------------------------------------------

func TestRunPasswd_HappyPath_NoKeychain(t *testing.T) {
	s := &passwdStubs{}
	s.install(t)
	if err := runPasswd(context.Background(), io.Discard, io.Discard); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if s.vol.openCalls != 2 {
		t.Errorf("expected 2 Open calls (verify old, verify new); got %d", s.vol.openCalls)
	}
	if s.vol.closeCalls != 2 {
		t.Errorf("expected 2 Close calls; got %d", s.vol.closeCalls)
	}
	if s.vol.addCalls != 1 {
		t.Errorf("expected 1 AddKeyslot call; got %d", s.vol.addCalls)
	}
	if s.vol.removeCalls != 1 {
		t.Errorf("expected 1 RemoveKeyslot call; got %d", s.vol.removeCalls)
	}
	if s.store.setCalls != 0 {
		t.Errorf("expected no keychain Set when keychain disabled; got %d", s.store.setCalls)
	}
}

func TestRunPasswd_HappyPath_WithKeychain(t *testing.T) {
	s := &passwdStubs{keychainEnabled: true}
	s.install(t)
	if err := runPasswd(context.Background(), io.Discard, io.Discard); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if s.store.setCalls != 1 {
		t.Errorf("expected keychain Set when keychain enabled; got %d", s.store.setCalls)
	}
	if s.store.setSvc != keychainServiceName {
		t.Errorf("set svc = %q, want %q", s.store.setSvc, keychainServiceName)
	}
	if s.store.setAcct != keychainAccountName {
		t.Errorf("set acct = %q, want %q", s.store.setAcct, keychainAccountName)
	}
}

func TestRunPasswd_KeychainSetFailureIsNonFatal(t *testing.T) {
	s := &passwdStubs{
		keychainEnabled: true,
		store:           &fakeKeychainStore{setErr: errors.New("kc broken")},
	}
	s.install(t)
	var stderr bytes.Buffer
	if err := runPasswd(context.Background(), io.Discard, &stderr); err != nil {
		t.Fatalf("rotation should still succeed when keychain fails, got %v", err)
	}
	if !strings.Contains(stderr.String(), "keychain update failed") {
		t.Errorf("expected warning on stderr, got %q", stderr.String())
	}
}

// ---- runPasswd: argv / sequence assertions ---------------------------------

func TestRunPasswd_AddKeyslotReceivesBothPasswords(t *testing.T) {
	pwOld := []byte("old-secret")
	pwNew := []byte("new-secret")
	s := &passwdStubs{
		prompter: &fakePrompter{
			promptOnce:  func(string) ([]byte, error) { return append([]byte(nil), pwOld...), nil },
			promptTwice: func(string) ([]byte, error) { return append([]byte(nil), pwNew...), nil },
		},
	}
	s.install(t)
	if err := runPasswd(context.Background(), io.Discard, io.Discard); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if string(s.vol.addExisting) != "old-secret" {
		t.Errorf("AddKeyslot existing = %q, want old-secret", s.vol.addExisting)
	}
	if string(s.vol.addNew) != "new-secret" {
		t.Errorf("AddKeyslot new = %q, want new-secret", s.vol.addNew)
	}
	if string(s.vol.removePassword) != "old-secret" {
		t.Errorf("RemoveKeyslot pw = %q, want old-secret (the old slot)", s.vol.removePassword)
	}
}

// ---- runPasswd: error paths ------------------------------------------------

func TestRunPasswd_LoadConfigFails(t *testing.T) {
	want := errors.New("no file")
	s := &passwdStubs{cfgErr: want}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped load error, got %v", err)
	}
}

func TestRunPasswd_BackendInitFails(t *testing.T) {
	want := errors.New("be fail")
	s := &passwdStubs{backendErr: want}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestRunPasswd_PromptCurrentFails(t *testing.T) {
	want := errors.New("read fail")
	s := &passwdStubs{
		prompter: &fakePrompter{
			promptOnce: func(string) ([]byte, error) { return nil, want },
		},
	}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped prompt error, got %v", err)
	}
}

func TestRunPasswd_VerifyCurrentBadPassword(t *testing.T) {
	s := &passwdStubs{
		vol: &fakePasswdVolume{openErr: volume.ErrBadPassword},
	}
	s.install(t)
	var stderr bytes.Buffer
	err := runPasswd(context.Background(), io.Discard, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitBadPassword {
		t.Errorf("expected exit %d, got %d", exitBadPassword, code)
	}
	if !strings.Contains(stderr.String(), "wrong password") {
		t.Errorf("expected friendly message, got %q", stderr.String())
	}
	if s.vol.addCalls != 0 {
		t.Errorf("expected no AddKeyslot when verify fails; got %d", s.vol.addCalls)
	}
	if s.vol.removeCalls != 0 {
		t.Errorf("expected no RemoveKeyslot when verify fails; got %d", s.vol.removeCalls)
	}
}

func TestRunPasswd_VerifyCurrentOtherError(t *testing.T) {
	want := errors.New("io fail")
	s := &passwdStubs{vol: &fakePasswdVolume{openErr: want}}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped open error, got %v", err)
	}
}

func TestRunPasswd_CloseAfterVerifyFails(t *testing.T) {
	want := errors.New("close fail")
	s := &passwdStubs{vol: &fakePasswdVolume{closeErr: want}}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped close error, got %v", err)
	}
}

func TestRunPasswd_PromptNewFails(t *testing.T) {
	want := errors.New("read fail")
	s := &passwdStubs{
		prompter: &fakePrompter{
			promptOnce:  func(string) ([]byte, error) { return []byte("old"), nil },
			promptTwice: func(string) ([]byte, error) { return nil, want },
		},
	}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped prompt error, got %v", err)
	}
}

func TestRunPasswd_AddKeyslotFails(t *testing.T) {
	want := errors.New("add fail")
	s := &passwdStubs{vol: &fakePasswdVolume{addErr: want}}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped add error, got %v", err)
	}
	if s.vol.removeCalls != 0 {
		t.Errorf("must NOT remove old slot after add failure; got %d", s.vol.removeCalls)
	}
}

func TestRunPasswd_VerifyNewFails_OldSlotStays(t *testing.T) {
	want := errors.New("new key broken")
	// First Open (verify old) succeeds; second Open (verify new) fails.
	s := &passwdStubs{
		vol: &fakePasswdVolume{
			openResults: []openResult{
				{dev: volume.Device("bolted"), err: nil},
				{err: want},
			},
		},
	}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped verify-new error, got %v", err)
	}
	if s.vol.removeCalls != 0 {
		t.Errorf("must NOT remove old slot when new-key verify fails; got %d", s.vol.removeCalls)
	}
}

func TestRunPasswd_CloseAfterNewVerifyFails(t *testing.T) {
	want := errors.New("close fail")
	// Close errors on the SECOND close (after verify-new). The first
	// close (after verify-old) must succeed so we get past that gate.
	s := &passwdStubs{vol: &fakePasswdVolume{closeErrs: []error{nil, want}}}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped close error, got %v", err)
	}
}

func TestRunPasswd_RemoveKeyslotFails(t *testing.T) {
	want := errors.New("remove fail")
	s := &passwdStubs{vol: &fakePasswdVolume{removeErr: want}}
	s.install(t)
	err := runPasswd(context.Background(), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped remove error, got %v", err)
	}
}

// ---- Cobra constructor -----------------------------------------------------

func TestNewPasswdCmd_Use(t *testing.T) {
	cmd := newPasswdCmd()
	if cmd.Use != "passwd" {
		t.Errorf("Use = %q, want passwd", cmd.Use)
	}
	if cmd.Short == "" {
		t.Error("Short description should not be empty")
	}
}

func TestPasswdCmd_RunE_HappyPath(t *testing.T) {
	s := &passwdStubs{}
	s.install(t)
	cmd := newPasswdCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

// ---- Defaults -------------------------------------------------------------

// TestNewPasswdVolumeFn_DefaultReturnsRealVolume sanity-checks the
// default factory wires through to *volume.Volume. We can't construct
// a real backend here, but volume.New accepts a nil backend and
// returns a non-nil *Volume.
func TestNewPasswdVolumeFn_DefaultReturnsRealVolume(t *testing.T) {
	v := newPasswdVolumeFn(nil, volume.Options{Name: "test"})
	if v == nil {
		t.Fatal("expected non-nil passwdVolume")
	}
}

// TestNewKeychainStoreFn_DefaultIsKeychainSystem checks the default
// indirection lines up with keychain.System.
func TestNewKeychainStoreFn_DefaultIsKeychainSystem(t *testing.T) {
	// Save+restore in case other tests modified it.
	orig := newKeychainStoreFn
	t.Cleanup(func() { newKeychainStoreFn = orig })
	store := newKeychainStoreFn()
	if store == nil {
		t.Fatal("expected non-nil store from default factory")
	}
}
