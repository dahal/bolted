package backend

import (
	"reflect"
	"testing"
)

// TestVMSpec_ZeroValue documents that the zero value of VMSpec is what the
// factory hands a backend when no sizing has been computed yet. Backends
// must reject zero values themselves — the type doesn't enforce it.
func TestVMSpec_ZeroValue(t *testing.T) {
	var s VMSpec
	if s.CPUs != 0 || s.MemoryMB != 0 || s.DiskGB != 0 {
		t.Errorf("expected zero VMSpec, got %+v", s)
	}
}

// TestExecOpts_ZeroValue documents that the zero value of ExecOpts is
// usable: no cwd, no env, no stdin, no TTY.
func TestExecOpts_ZeroValue(t *testing.T) {
	var o ExecOpts
	if o.Cwd != "" || len(o.Env) != 0 || o.Stdin != nil || o.TTY {
		t.Errorf("expected zero ExecOpts, got %+v", o)
	}
}

// TestExecResult_ZeroValue documents the zero ExecResult.
func TestExecResult_ZeroValue(t *testing.T) {
	var r ExecResult
	if len(r.Stdout) != 0 || len(r.Stderr) != 0 || r.ExitCode != 0 {
		t.Errorf("expected zero ExecResult, got %+v", r)
	}
}

// TestBackendInterface_Shape pins the Backend interface's method set so a
// refactor that accidentally drops or renames a method fails loudly.
func TestBackendInterface_Shape(t *testing.T) {
	want := []string{
		"DeleteVM",
		"EnsureVM",
		"Exec",
		"ForwardPort",
		"IsRunning",
		"StartVM",
		"StopVM",
		"UnforwardPort",
	}
	typ := reflect.TypeOf((*Backend)(nil)).Elem()
	got := make([]string, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		got[i] = typ.Method(i).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Backend method set drifted:\n got:  %v\n want: %v", got, want)
	}
}
