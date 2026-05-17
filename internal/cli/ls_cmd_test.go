package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dahal/bolted/internal/backend"
	"github.com/dahal/bolted/internal/backend/mock"
)

// ---- listRepoNames --------------------------------------------------------

func TestListRepoNames_HappyPath(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0, Stdout: []byte("beta\nalpha\nalpha\n  \n")}},
	}
	names, err := listRepoNames(context.Background(), scripted)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if strings.Join(names, ",") != "alpha,beta" {
		t.Errorf("got %v, want [alpha beta]", names)
	}
}

func TestListRepoNames_ExecError(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("boom")},
	}
	if _, err := listRepoNames(context.Background(), scripted); err == nil {
		t.Fatal("expected error")
	}
}

func TestListRepoNames_NonZeroExit(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 2}},
	}
	if _, err := listRepoNames(context.Background(), scripted); err == nil {
		t.Fatal("expected error")
	}
}

// ---- repoSize -------------------------------------------------------------

func TestRepoSize_HappyPath(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0, Stdout: []byte("420M\t/bolted/repos/api\n")}},
	}
	size, err := repoSize(context.Background(), scripted, "api")
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if size != "420M" {
		t.Errorf("got %q, want 420M", size)
	}
}

func TestRepoSize_NonZeroExit(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 1}},
	}
	size, err := repoSize(context.Background(), scripted, "api")
	if err == nil {
		t.Error("expected error")
	}
	if size != "—" {
		t.Errorf("expected dash fallback, got %q", size)
	}
}

func TestRepoSize_ExecError(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:     mock.New(),
		execErrs: []error{errors.New("boom")},
	}
	size, _ := repoSize(context.Background(), scripted, "api")
	if size != "—" {
		t.Errorf("expected dash on exec err, got %q", size)
	}
}

func TestRepoSize_EmptyOutput(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0, Stdout: []byte("")}},
	}
	size, _ := repoSize(context.Background(), scripted, "api")
	if size != "—" {
		t.Errorf("expected dash on empty output, got %q", size)
	}
}

func TestRepoSize_SpaceSeparated(t *testing.T) {
	scripted := &scriptedBackend{
		Mock:       mock.New(),
		execScript: []backend.ExecResult{{ExitCode: 0, Stdout: []byte("1.2G /bolted/repos/api\n")}},
	}
	size, _ := repoSize(context.Background(), scripted, "api")
	if size != "1.2G" {
		t.Errorf("got %q", size)
	}
}

// ---- runLs ----------------------------------------------------------------

func TestRunLs_RequireUnlockedFails(t *testing.T) {
	s := &lifecycleStubs{}
	s.install(t)
	withStatStub(t, statMissing)
	err := runLs(context.Background(), io.Discard, io.Discard, lsOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if code := exitCodeFromError(err); code != exitLocked {
		t.Errorf("expected exit %d, got %d", exitLocked, code)
	}
}

func TestRunLs_ListReposError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0}, // unlocked
		{ExitCode: 2}, // ls /bolted/repos
	}, nil)
	err := runLs(context.Background(), io.Discard, io.Discard, lsOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunLs_ContainersReadError(t *testing.T) {
	ds := installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},                                    // unlocked
		{ExitCode: 0, Stdout: []byte("api\n")},           // ls
	}, nil)
	if err := writeJunk(ds.stateDir); err != nil {
		t.Fatal(err)
	}
	err := runLs(context.Background(), io.Discard, io.Discard, lsOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunLs_RunningContainerIDsError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},                          // unlocked
		{ExitCode: 0, Stdout: []byte("api\n")}, // ls
	}, []error{nil, nil, errors.New("podman ps boom")})
	err := runLs(context.Background(), io.Discard, io.Discard, lsOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunLs_HappyPathTable(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},                                          // unlocked probe
		{ExitCode: 0, Stdout: []byte("api\nweb\n")},            // ls /bolted/repos
		{ExitCode: 0, Stdout: []byte("id-api\n")},              // podman ps (only api running)
		{ExitCode: 0, Stdout: []byte("420M\t/bolted/repos/api\n")}, // du api
		{ExitCode: 0, Stdout: []byte("180M\t/bolted/repos/web\n")}, // du web
	}, nil)
	if err := recordContainer("api", "id-api"); err != nil {
		t.Fatal(err)
	}
	if err := recordContainer("web", "id-web"); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := runLs(context.Background(), &stdout, io.Discard, lsOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "STATUS") || !strings.Contains(out, "SIZE") {
		t.Errorf("expected header, got %q", out)
	}
	if !strings.Contains(out, "api") || !strings.Contains(out, "running") {
		t.Errorf("expected api running, got %q", out)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "stopped") {
		t.Errorf("expected web stopped, got %q", out)
	}
	if !strings.Contains(out, "420M") || !strings.Contains(out, "180M") {
		t.Errorf("expected sizes, got %q", out)
	}
}

func TestRunLs_JSONHappyPath(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: []byte("api\n")},
		{ExitCode: 0, Stdout: []byte("id-api\n")},
		{ExitCode: 0, Stdout: []byte("420M\t/bolted/repos/api\n")},
	}, nil)
	_ = recordContainer("api", "id-api")
	var stdout bytes.Buffer
	if err := runLs(context.Background(), &stdout, io.Discard, lsOptions{jsonOut: true}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var entries []repoEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout.String())
	}
	if len(entries) != 1 || entries[0].Name != "api" || entries[0].Status != "running" || entries[0].Size != "420M" {
		t.Errorf("unexpected entries: %+v", entries)
	}
}

// jsonLsErrWriter forces encode to fail.
type jsonLsErrWriter struct{}

func (jsonLsErrWriter) Write([]byte) (int, error) { return 0, errors.New("write boom") }

func TestRunLs_JSONEncodeError(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: []byte("api\n")},
		{ExitCode: 0, Stdout: []byte("")},
		{ExitCode: 0, Stdout: []byte("420M\t/p\n")},
	}, nil)
	err := runLs(context.Background(), jsonLsErrWriter{}, io.Discard, lsOptions{jsonOut: true})
	if err == nil || !strings.Contains(err.Error(), "encode JSON") {
		t.Errorf("expected encode error, got %v", err)
	}
}

func TestRunLs_StoppedWhenIDNotRunning(t *testing.T) {
	// containers.json has api → id-api but podman ps reports id-other.
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: []byte("api\n")},
		{ExitCode: 0, Stdout: []byte("id-other\n")},
		{ExitCode: 0, Stdout: []byte("420M\t/p\n")},
	}, nil)
	_ = recordContainer("api", "id-api")
	var stdout bytes.Buffer
	if err := runLs(context.Background(), &stdout, io.Discard, lsOptions{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(stdout.String(), "stopped") {
		t.Errorf("expected stopped, got %q", stdout.String())
	}
}

// ---- renderLsTable --------------------------------------------------------

func TestRenderLsTable(t *testing.T) {
	var buf bytes.Buffer
	renderLsTable(&buf, []repoEntry{
		{Name: "api", Status: "running", Size: "420M"},
		{Name: "web", Status: "stopped", Size: "180M"},
	})
	out := buf.String()
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "api") || !strings.Contains(out, "web") {
		t.Errorf("unexpected output: %q", out)
	}
}

// ---- Cobra plumbing -------------------------------------------------------

func TestNewLsCmd_FlagsRegistered(t *testing.T) {
	cmd := newLsCmd()
	if cmd.Flags().Lookup("json") == nil {
		t.Error("expected --json flag")
	}
	if cmd.Use != "ls" {
		t.Errorf("expected Use=ls, got %q", cmd.Use)
	}
}

func TestLsCmd_RunE_Dispatch(t *testing.T) {
	installDevSetup(t, []backend.ExecResult{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: []byte("")},
		{ExitCode: 0, Stdout: []byte("")},
	}, nil)
	cmd := newLsCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
