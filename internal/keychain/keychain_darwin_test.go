//go:build darwin

package keychain

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeRunner records the most recent call and returns canned outputs.
type fakeRunner struct {
	calls []recordedCall

	stdout []byte
	stderr []byte
	err    error
}

type recordedCall struct {
	name  string
	args  []string
	stdin []byte
}

func (f *fakeRunner) run(_ context.Context, name string, args []string, stdin []byte) ([]byte, []byte, error) {
	f.calls = append(f.calls, recordedCall{name: name, args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...)})
	return f.stdout, f.stderr, f.err
}

// ----- System --------------------------------------------------------------

func TestSystem_ReturnsMacStore(t *testing.T) {
	s := System()
	if _, ok := s.(*macStore); !ok {
		t.Fatalf("System() = %T, want *macStore", s)
	}
}

// ----- Get -----------------------------------------------------------------

func TestMacStore_Get_OK(t *testing.T) {
	r := &fakeRunner{stdout: []byte("hunter2\n")}
	s := &macStore{run: r.run}
	got, err := s.Get("svc", "acct")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hunter2" {
		t.Errorf("Get = %q, want hunter2", got)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	call := r.calls[0]
	if call.name != "security" {
		t.Errorf("cmd = %q, want security", call.name)
	}
	want := []string{"find-generic-password", "-s", "svc", "-a", "acct", "-w"}
	if strings.Join(call.args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", call.args, want)
	}
}

func TestMacStore_Get_NotFound(t *testing.T) {
	r := &fakeRunner{
		stderr: []byte("security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.\n"),
		err:    &exec.ExitError{},
	}
	s := &macStore{run: r.run}
	_, err := s.Get("svc", "acct")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
}

func TestMacStore_Get_OtherError(t *testing.T) {
	r := &fakeRunner{
		stderr: []byte("something exploded"),
		err:    errors.New("exec failed"),
	}
	s := &macStore{run: r.run}
	_, err := s.Get("svc", "acct")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("expected non-notfound error, got %v", err)
	}
	if !strings.Contains(err.Error(), "something exploded") {
		t.Errorf("expected wrapped stderr, got %v", err)
	}
}

// ----- Set -----------------------------------------------------------------

func TestMacStore_Set_OK(t *testing.T) {
	r := &fakeRunner{}
	s := &macStore{run: r.run}
	if err := s.Set("svc", "acct", []byte("secret")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	call := r.calls[0]
	want := []string{"add-generic-password", "-U", "-s", "svc", "-a", "acct", "-w", "secret"}
	if strings.Join(call.args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", call.args, want)
	}
}

func TestMacStore_Set_Error(t *testing.T) {
	r := &fakeRunner{
		stderr: []byte("permission denied"),
		err:    errors.New("exec failed"),
	}
	s := &macStore{run: r.run}
	err := s.Set("svc", "acct", []byte("x"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected wrapped stderr, got %v", err)
	}
}

// ----- Delete --------------------------------------------------------------

func TestMacStore_Delete_OK(t *testing.T) {
	r := &fakeRunner{}
	s := &macStore{run: r.run}
	if err := s.Delete("svc", "acct"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	call := r.calls[0]
	want := []string{"delete-generic-password", "-s", "svc", "-a", "acct"}
	if strings.Join(call.args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", call.args, want)
	}
}

func TestMacStore_Delete_MissingIsSuccess(t *testing.T) {
	r := &fakeRunner{
		stderr: []byte("security: could not be found"),
		err:    errors.New("exit 44"),
	}
	s := &macStore{run: r.run}
	if err := s.Delete("svc", "acct"); err != nil {
		t.Errorf("Delete should be idempotent for missing, got %v", err)
	}
}

func TestMacStore_Delete_OtherError(t *testing.T) {
	r := &fakeRunner{
		stderr: []byte("disk failure"),
		err:    errors.New("exec failed"),
	}
	s := &macStore{run: r.run}
	err := s.Delete("svc", "acct")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "disk failure") {
		t.Errorf("expected wrapped stderr, got %v", err)
	}
}

// ----- isMissing -----------------------------------------------------------

func TestIsMissing(t *testing.T) {
	cases := map[string]struct {
		stderr []byte
		want   bool
	}{
		"english":          {[]byte("The specified item could not be found in the keychain."), true},
		"lowercase":        {[]byte("could not be found"), true},
		"unrelated":        {[]byte("permission denied"), false},
		"empty":            {[]byte(""), false},
		"contains-could":   {[]byte("could not parse"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := isMissing(tc.stderr, errors.New("x"))
			if got != tc.want {
				t.Errorf("isMissing(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

// ----- defaultRunner -------------------------------------------------------

// TestDefaultRunner_RealExec exercises the production runner against a
// benign command (`true`) so the function's happy path is covered without
// reaching out to `security` for real. We pipe stdin through to make
// sure the stdin branch is also covered.
func TestDefaultRunner_RealExec(t *testing.T) {
	stdout, stderr, err := defaultRunner(context.Background(), "true", nil, []byte("ignored"))
	if err != nil {
		t.Fatalf("defaultRunner(true): %v", err)
	}
	if !bytes.Equal(stdout, []byte{}) && len(stdout) > 0 {
		t.Errorf("unexpected stdout: %q", stdout)
	}
	if len(stderr) > 0 {
		t.Errorf("unexpected stderr: %q", stderr)
	}
}

// TestDefaultRunner_BinaryNotFound covers the error branch.
func TestDefaultRunner_BinaryNotFound(t *testing.T) {
	_, _, err := defaultRunner(context.Background(), "/nonexistent/binary/__keychain_test", nil, nil)
	if err == nil {
		t.Fatal("expected error invoking missing binary")
	}
}

// TestDefaultRunner_NoStdin covers the branch where stdin is nil.
func TestDefaultRunner_NoStdin(t *testing.T) {
	_, _, err := defaultRunner(context.Background(), "true", nil, nil)
	if err != nil {
		t.Fatalf("defaultRunner(true) no stdin: %v", err)
	}
}
