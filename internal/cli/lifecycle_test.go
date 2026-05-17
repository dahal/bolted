package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
	"github.com/dahal/bolted/internal/config"
	"github.com/dahal/bolted/internal/volume"
)

// ---- Fake prompter ---------------------------------------------------------

type fakePrompter struct {
	promptOnce   func(label string) ([]byte, error)
	promptTwice  func(label string) ([]byte, error)
	fromStdin    func() ([]byte, error)
}

func (f *fakePrompter) Prompt(label string) ([]byte, error) {
	if f.promptOnce == nil {
		return nil, errors.New("Prompt not configured")
	}
	return f.promptOnce(label)
}

func (f *fakePrompter) PromptTwiceConfirm(label string) ([]byte, error) {
	if f.promptTwice == nil {
		return nil, errors.New("PromptTwiceConfirm not configured")
	}
	return f.promptTwice(label)
}

func (f *fakePrompter) FromStdin() ([]byte, error) {
	if f.fromStdin == nil {
		return nil, errors.New("FromStdin not configured")
	}
	return f.fromStdin()
}

// ---- Fake volume -----------------------------------------------------------

type fakeVolume struct {
	createErr   error
	openErr     error
	openDevice  volume.Device
	mountErr    error
	unmountErr  error
	closeErr    error

	created  bool
	opened   bool
	mounted  bool
	unmount  bool
	closed   bool

	lastCreatePassword []byte
	lastOpenPassword   []byte
}

func (f *fakeVolume) Create(_ context.Context, _ string, _ int64, password []byte) error {
	f.created = true
	// Copy because the caller will zero the original.
	f.lastCreatePassword = append([]byte(nil), password...)
	return f.createErr
}

func (f *fakeVolume) Open(_ context.Context, _ string, password []byte) (volume.Device, error) {
	f.opened = true
	f.lastOpenPassword = append([]byte(nil), password...)
	if f.openErr != nil {
		return "", f.openErr
	}
	dev := f.openDevice
	if dev == "" {
		dev = volume.Device("bolted")
	}
	return dev, nil
}

func (f *fakeVolume) Mount(_ context.Context, _ volume.Device, _ string) error {
	f.mounted = true
	return f.mountErr
}

func (f *fakeVolume) Unmount(_ context.Context, _ string) error {
	f.unmount = true
	return f.unmountErr
}

func (f *fakeVolume) Close(_ context.Context, _ volume.Device) error {
	f.closed = true
	return f.closeErr
}

// ---- withLifecycleDeps -----------------------------------------------------

type lifecycleStubs struct {
	tempDir  string
	cfg      *config.Config
	cfgErr   error
	saveErr  error
	defaults config.VMConfig
	detectErr error
	backendErr error
	prompter *fakePrompter
	mockBE   *mock.Mock
	vol      *fakeVolume
	volMaker func(backend.Backend, volume.Options) volumeOps
}

// install wires the stubs into the package-level vars and returns a
// t.Cleanup function (added via the test's t.Cleanup mechanism).
func (s *lifecycleStubs) install(t *testing.T) {
	t.Helper()
	if s.tempDir == "" {
		s.tempDir = t.TempDir()
	}
	if s.defaults == (config.VMConfig{}) {
		s.defaults = config.VMConfig{Memory: "8GB", CPUs: 4, Disk: "50GB"}
	}
	if s.prompter == nil {
		s.prompter = &fakePrompter{
			promptOnce:  func(string) ([]byte, error) { return []byte("pw"), nil },
			promptTwice: func(string) ([]byte, error) { return []byte("pw"), nil },
			fromStdin:   func() ([]byte, error) { return []byte("pw"), nil },
		}
	}
	if s.mockBE == nil {
		s.mockBE = mock.New()
	}
	if s.vol == nil {
		s.vol = &fakeVolume{}
	}

	origNewBackend := newBackendFn
	origNewVolume := newVolumeFn
	origDetect := detectDefaultsFn
	origLoad := loadConfigFn
	origSave := saveConfigFn
	origWS := boltedDirFn
	origPrompter := newPrompterFn
	t.Cleanup(func() {
		newBackendFn = origNewBackend
		newVolumeFn = origNewVolume
		detectDefaultsFn = origDetect
		loadConfigFn = origLoad
		saveConfigFn = origSave
		boltedDirFn = origWS
		newPrompterFn = origPrompter
	})

	newBackendFn = func(_ backend.Config) (backend.Backend, error) {
		if s.backendErr != nil {
			return nil, s.backendErr
		}
		return s.mockBE, nil
	}
	if s.volMaker != nil {
		newVolumeFn = s.volMaker
	} else {
		newVolumeFn = func(_ backend.Backend, _ volume.Options) volumeOps {
			return s.vol
		}
	}
	detectDefaultsFn = func() (config.VMConfig, error) {
		if s.detectErr != nil {
			return config.VMConfig{}, s.detectErr
		}
		return s.defaults, nil
	}
	loadConfigFn = func(path string) (*config.Config, error) {
		if s.cfgErr != nil {
			return nil, s.cfgErr
		}
		if s.cfg != nil {
			return s.cfg, nil
		}
		c := config.NewDefault()
		c.VM = s.defaults
		return c, nil
	}
	saveConfigFn = func(_ string, _ *config.Config) error { return s.saveErr }
	boltedDirFn = func() string { return s.tempDir }
	newPrompterFn = func() passwordPrompter { return s.prompter }
}

// ---- Helpers ---------------------------------------------------------------

func discardWriters() (io.Writer, io.Writer) { return io.Discard, io.Discard }

// ---- vmSpecFromConfig ------------------------------------------------------

func TestVMSpecFromConfig_OK(t *testing.T) {
	vm := config.VMConfig{Memory: "8GB", CPUs: 4, Disk: "50GB"}
	spec, err := vmSpecFromConfig(vm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.CPUs != 4 || spec.MemoryMB != 8*1024 || spec.DiskGB != 50 {
		t.Errorf("got %+v, want CPUs=4 MemoryMB=8192 DiskGB=50", spec)
	}
}

func TestVMSpecFromConfig_BadMemory(t *testing.T) {
	_, err := vmSpecFromConfig(config.VMConfig{Memory: "garbage", CPUs: 4, Disk: "50GB"})
	if err == nil || !strings.Contains(err.Error(), "memory") {
		t.Errorf("expected memory error, got %v", err)
	}
}

func TestVMSpecFromConfig_BadDisk(t *testing.T) {
	_, err := vmSpecFromConfig(config.VMConfig{Memory: "8GB", CPUs: 4, Disk: "huh"})
	if err == nil || !strings.Contains(err.Error(), "disk") {
		t.Errorf("expected disk error, got %v", err)
	}
}

func TestMemoryMBFromString_BadInput(t *testing.T) {
	if _, err := memoryMBFromString("nope"); err == nil {
		t.Error("expected error")
	}
}

func TestDiskGBFromString_BadInput(t *testing.T) {
	if _, err := diskGBFromString("nope"); err == nil {
		t.Error("expected error")
	}
}

func TestConfigPath(t *testing.T) {
	orig := boltedDirFn
	t.Cleanup(func() { boltedDirFn = orig })
	boltedDirFn = func() string { return "/tmp/bolt" }
	want := filepath.Join("/tmp/bolt", "config.yaml")
	if got := configPath(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---- runInit ---------------------------------------------------------------

func TestRunInit_HappyPath(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !s.vol.created {
		t.Error("Volume.Create was not called")
	}
	// EnsureVM + StartVM should have been called on the mock.
	calls := s.mockBE.Methods()
	if len(calls) < 2 || calls[0] != "EnsureVM" || calls[1] != "StartVM" {
		t.Errorf("unexpected backend call sequence: %v", calls)
	}
}

func TestRunInit_PasswordIsZeroedAfterUse(t *testing.T) {
	// Use a sentinel slice; runInit defers zero(password).
	pw := []byte("secret-pw")
	s := &lifecycleStubs{
		prompter: &fakePrompter{
			promptTwice: func(string) ([]byte, error) { return pw, nil },
		},
	}
	s.install(t)
	if err := runInit(context.Background(), io.Discard, io.Discard, initOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for i, b := range pw {
		if b != 0 {
			t.Errorf("byte %d = %d; expected zeroed buffer", i, b)
		}
	}
}

func TestRunInit_DetectFails(t *testing.T) {
	want := errors.New("sysctl fail")
	s := &lifecycleStubs{detectErr: want}
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped detect error, got %v", err)
	}
}

func TestRunInit_PromptFails(t *testing.T) {
	want := errors.New("prompt fail")
	s := &lifecycleStubs{
		prompter: &fakePrompter{
			promptTwice: func(string) ([]byte, error) { return nil, want },
		},
	}
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped prompt error, got %v", err)
	}
}

func TestRunInit_FromNoticePrinted(t *testing.T) {
	// --from is still a deferred notice until spec 15 wires it in.
	var stderr bytes.Buffer
	s := &lifecycleStubs{}
	s.install(t)
	if err := runInit(context.Background(), io.Discard, &stderr, initOptions{fromURL: "x"}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stderr.String(), "--from") {
		t.Errorf("expected notice about --from, got: %q", stderr.String())
	}
}

func TestRunInit_ProfileWritesYAML(t *testing.T) {
	// --profile end-to-end: fetch bytes via profilesGetFn, persist to
	// BoltedDir/bolted.yaml with 0600, print a nudge to stderr.
	var stderr bytes.Buffer
	s := &lifecycleStubs{}
	s.install(t)

	origGet := profilesGetFn
	origWrite := profileWriteFileFn
	t.Cleanup(func() { profilesGetFn = origGet; profileWriteFileFn = origWrite })

	want := []byte("features:\n  - ghcr.io/devcontainers/features/github-cli:1\n")
	var calledGet string
	var wrotePath string
	var wroteBytes []byte
	var wrotePerm os.FileMode
	profilesGetFn = func(name string) ([]byte, error) {
		calledGet = name
		return want, nil
	}
	profileWriteFileFn = func(path string, data []byte, perm os.FileMode) error {
		wrotePath = path
		wroteBytes = data
		wrotePerm = perm
		return nil
	}

	if err := runInit(context.Background(), io.Discard, &stderr, initOptions{profile: "fullstack"}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if calledGet != "fullstack" {
		t.Errorf("profilesGetFn called with %q, want fullstack", calledGet)
	}
	if !strings.HasSuffix(wrotePath, filepath.Join("bolted.yaml")) {
		t.Errorf("unexpected write path %q", wrotePath)
	}
	if string(wroteBytes) != string(want) {
		t.Errorf("wrote %q, want %q", wroteBytes, want)
	}
	if wrotePerm != 0o600 {
		t.Errorf("perm = %v, want 0o600", wrotePerm)
	}
	if !strings.Contains(stderr.String(), "fullstack") {
		t.Errorf("expected stderr to mention profile name, got %q", stderr.String())
	}
}

func TestRunInit_ProfileUnknownErrors(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)

	origGet := profilesGetFn
	origNames := profilesNamesFn
	t.Cleanup(func() { profilesGetFn = origGet; profilesNamesFn = origNames })

	profilesGetFn = func(string) ([]byte, error) { return nil, errors.New("boom") }
	profilesNamesFn = func() []string { return []string{"minimal", "fullstack"} }

	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{profile: "bogus"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bogus") {
		t.Errorf("expected error to name the bad profile, got %v", err)
	}
	if !strings.Contains(msg, "minimal") || !strings.Contains(msg, "fullstack") {
		t.Errorf("expected error to list available profiles, got %v", err)
	}
}

func TestRunInit_ProfileWriteFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)

	origGet := profilesGetFn
	origWrite := profileWriteFileFn
	t.Cleanup(func() { profilesGetFn = origGet; profileWriteFileFn = origWrite })

	profilesGetFn = func(string) ([]byte, error) { return []byte("x"), nil }
	want := errors.New("disk-full")
	profileWriteFileFn = func(string, []byte, os.FileMode) error { return want }

	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{profile: "fullstack"})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped write err, got %v", err)
	}
}

func TestRunInit_SaveConfigFails(t *testing.T) {
	want := errors.New("disk full")
	s := &lifecycleStubs{saveErr: want}
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped save error, got %v", err)
	}
}

func TestRunInit_BadVMSpec(t *testing.T) {
	s := &lifecycleStubs{
		defaults: config.VMConfig{Memory: "garbage", CPUs: 4, Disk: "50GB"},
	}
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if err == nil || !strings.Contains(err.Error(), "memory") {
		t.Errorf("expected memory parse error, got %v", err)
	}
}

func TestRunInit_BackendInitFails(t *testing.T) {
	want := errors.New("no backend")
	s := &lifecycleStubs{backendErr: want}
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestRunInit_EnsureVMFails(t *testing.T) {
	want := errors.New("ensure fail")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.ErrEnsureVM = want
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped ensure error, got %v", err)
	}
}

func TestRunInit_StartVMFails(t *testing.T) {
	want := errors.New("start fail")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.ErrStartVM = want
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped start error, got %v", err)
	}
}

func TestRunInit_VolumeCreateFails(t *testing.T) {
	want := errors.New("luks fail")
	s := &lifecycleStubs{vol: &fakeVolume{createErr: want}}
	s.install(t)
	err := runInit(context.Background(), io.Discard, io.Discard, initOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped create error, got %v", err)
	}
}

func TestRunInit_PasswordStdin(t *testing.T) {
	pw := []byte("piped")
	s := &lifecycleStubs{
		prompter: &fakePrompter{
			fromStdin: func() ([]byte, error) { return pw, nil },
		},
	}
	s.install(t)
	if err := runInit(context.Background(), io.Discard, io.Discard, initOptions{passwordStdin: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRunInit_InsecurePasswordFlagAccepted(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	if err := runInit(context.Background(), io.Discard, io.Discard, initOptions{insecurePassword: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

// ---- runUnlock -------------------------------------------------------------

func TestRunUnlock_HappyPath(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	if err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !s.vol.opened || !s.vol.mounted {
		t.Errorf("expected Open + Mount; got opened=%v mounted=%v", s.vol.opened, s.vol.mounted)
	}
}

func TestRunUnlock_VMNotRunningStartsIt(t *testing.T) {
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.install(t)
	if err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	methods := s.mockBE.Methods()
	var sawStart bool
	for _, m := range methods {
		if m == "StartVM" {
			sawStart = true
		}
	}
	if !sawStart {
		t.Errorf("expected StartVM call, methods=%v", methods)
	}
}

func TestRunUnlock_VMRunningSkipsStart(t *testing.T) {
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = true
	s.install(t)
	if err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for _, m := range s.mockBE.Methods() {
		if m == "StartVM" {
			t.Errorf("did not expect StartVM when already running, methods=%v", s.mockBE.Methods())
		}
	}
}

func TestRunUnlock_LoadConfigFails(t *testing.T) {
	want := errors.New("no file")
	s := &lifecycleStubs{cfgErr: want}
	s.install(t)
	err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped load error, got %v", err)
	}
}

func TestRunUnlock_PromptFails(t *testing.T) {
	want := errors.New("prompt fail")
	s := &lifecycleStubs{
		prompter: &fakePrompter{promptOnce: func(string) ([]byte, error) { return nil, want }},
	}
	s.install(t)
	err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped prompt error, got %v", err)
	}
}

func TestRunUnlock_BackendInitFails(t *testing.T) {
	want := errors.New("be fail")
	s := &lifecycleStubs{backendErr: want}
	s.install(t)
	err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestRunUnlock_IsRunningFails(t *testing.T) {
	want := errors.New("is running fail")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.ErrIsRunning = want
	s.install(t)
	err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped IsRunning error, got %v", err)
	}
}

func TestRunUnlock_StartVMFails(t *testing.T) {
	want := errors.New("start fail")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.IsRunningResult = false
	s.mockBE.ErrStartVM = want
	s.install(t)
	err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped start error, got %v", err)
	}
}

func TestRunUnlock_BadPasswordExitsCode3(t *testing.T) {
	s := &lifecycleStubs{
		vol: &fakeVolume{openErr: volume.ErrBadPassword},
	}
	s.install(t)
	var stderr bytes.Buffer
	err := runUnlock(context.Background(), io.Discard, &stderr, unlockOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitBadPassword {
		t.Errorf("expected exit code %d, got %d (err=%v)", exitBadPassword, code, err)
	}
	if !strings.Contains(stderr.String(), "wrong password") {
		t.Errorf("expected friendly message, got: %q", stderr.String())
	}
}

func TestRunUnlock_OtherOpenError(t *testing.T) {
	want := errors.New("io error")
	s := &lifecycleStubs{vol: &fakeVolume{openErr: want}}
	s.install(t)
	err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped open error, got %v", err)
	}
}

func TestRunUnlock_MountFailsTriggersClose(t *testing.T) {
	want := errors.New("mount fail")
	v := &fakeVolume{mountErr: want}
	s := &lifecycleStubs{vol: v}
	s.install(t)
	err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped mount error, got %v", err)
	}
	if !v.closed {
		t.Error("expected best-effort Close after mount failure")
	}
}

func TestRunUnlock_PasswordStdin(t *testing.T) {
	pw := []byte("piped")
	s := &lifecycleStubs{
		prompter: &fakePrompter{fromStdin: func() ([]byte, error) { return pw, nil }},
	}
	s.install(t)
	if err := runUnlock(context.Background(), io.Discard, io.Discard, unlockOptions{passwordStdin: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

// ---- runLock ---------------------------------------------------------------

func TestRunLock_HappyPath(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	if err := runLock(context.Background(), io.Discard, io.Discard, lockOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !s.vol.unmount || !s.vol.closed {
		t.Errorf("expected Unmount + Close; got unmount=%v closed=%v", s.vol.unmount, s.vol.closed)
	}
}

func TestRunLock_LoadConfigFails(t *testing.T) {
	want := errors.New("no file")
	s := &lifecycleStubs{cfgErr: want}
	s.install(t)
	err := runLock(context.Background(), io.Discard, io.Discard, lockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped load error, got %v", err)
	}
}

func TestRunLock_BackendInitFails(t *testing.T) {
	want := errors.New("be fail")
	s := &lifecycleStubs{backendErr: want}
	s.install(t)
	err := runLock(context.Background(), io.Discard, io.Discard, lockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestRunLock_UnmountFails(t *testing.T) {
	want := errors.New("umount fail")
	s := &lifecycleStubs{vol: &fakeVolume{unmountErr: want}}
	s.install(t)
	err := runLock(context.Background(), io.Discard, io.Discard, lockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped unmount error, got %v", err)
	}
}

func TestRunLock_CloseFails(t *testing.T) {
	want := errors.New("close fail")
	s := &lifecycleStubs{vol: &fakeVolume{closeErr: want}}
	s.install(t)
	err := runLock(context.Background(), io.Discard, io.Discard, lockOptions{})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped close error, got %v", err)
	}
}

func TestRunLock_StopVMFlag(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	if err := runLock(context.Background(), io.Discard, io.Discard, lockOptions{stopVM: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var sawStop bool
	for _, m := range s.mockBE.Methods() {
		if m == "StopVM" {
			sawStop = true
		}
	}
	if !sawStop {
		t.Errorf("expected StopVM call, methods=%v", s.mockBE.Methods())
	}
}

func TestRunLock_StopVMFails(t *testing.T) {
	want := errors.New("stop fail")
	s := &lifecycleStubs{mockBE: mock.New()}
	s.mockBE.ErrStopVM = want
	s.install(t)
	err := runLock(context.Background(), io.Discard, io.Discard, lockOptions{stopVM: true})
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped stop error, got %v", err)
	}
}

// ---- exitError / exitCodeFromError ----------------------------------------

func TestExitError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("inner")
	ee := &exitError{code: 7, err: inner}
	if ee.Error() != "inner" {
		t.Errorf("Error() = %q, want inner", ee.Error())
	}
	if errors.Unwrap(ee) != inner {
		t.Error("Unwrap should return inner")
	}
	if got := (*exitError)(nil).Error(); got != "" {
		t.Errorf("nil Error() = %q, want empty", got)
	}
	zero := &exitError{}
	if zero.Error() != "" {
		t.Errorf("zero Error() = %q, want empty", zero.Error())
	}
}

func TestExitCodeFromError(t *testing.T) {
	if got := exitCodeFromError(nil); got != exitOK {
		t.Errorf("nil err code = %d, want %d", got, exitOK)
	}
	if got := exitCodeFromError(errors.New("plain")); got != exitGeneric {
		t.Errorf("plain err code = %d, want %d", got, exitGeneric)
	}
	wrapped := &exitError{code: exitBadPassword, err: errors.New("bad")}
	if got := exitCodeFromError(wrapped); got != exitBadPassword {
		t.Errorf("wrapped exit err code = %d, want %d", got, exitBadPassword)
	}
}

// ---- Cobra constructors ----------------------------------------------------

func TestNewInitCmd_FlagsRegistered(t *testing.T) {
	cmd := newInitCmd()
	for _, name := range []string{"profile", "from", "password-stdin", "insecure-password"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected --%s flag", name)
		}
	}
}

func TestNewUnlockCmd_FlagsRegistered(t *testing.T) {
	cmd := newUnlockCmd()
	if cmd.Flags().Lookup("password-stdin") == nil {
		t.Error("expected --password-stdin flag")
	}
}

func TestNewLockCmd_FlagsRegistered(t *testing.T) {
	cmd := newLockCmd()
	if cmd.Flags().Lookup("stop-vm") == nil {
		t.Error("expected --stop-vm flag")
	}
}

// ---- Cobra integration (RunE via cmd.Execute) -----------------------------

func TestInitCmd_RunE_HappyPath(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	cmd := newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestUnlockCmd_RunE_HappyPath(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	cmd := newUnlockCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestLockCmd_RunE_HappyPath(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	cmd := newLockCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
