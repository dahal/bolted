package devcontainertrust

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStore_ApprovedMissingFileReturnsFalse(t *testing.T) {
	s := NewStore(t.TempDir())
	if s.Approved("api", "abc") {
		t.Errorf("expected Approved=false for missing file")
	}
}

func TestStore_ApproveAndApproved(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Approve("api", "deadbeef"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !s.Approved("api", "deadbeef") {
		t.Errorf("expected Approved=true after Approve")
	}
	if s.Approved("api", "different") {
		t.Errorf("different hash should not be approved (re-prompt path)")
	}
	if s.Approved("other", "deadbeef") {
		t.Errorf("different repo should not be approved")
	}
}

func TestStore_ApproveEmptyRepoErrors(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Approve("", "hash"); err == nil {
		t.Fatal("expected error on empty repo")
	}
}

func TestStore_ApproveOverwrites(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Approve("api", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve("api", "v2"); err != nil {
		t.Fatal(err)
	}
	if !s.Approved("api", "v2") {
		t.Errorf("expected v2 to be approved after overwrite")
	}
	if s.Approved("api", "v1") {
		t.Errorf("v1 should no longer be approved after overwrite")
	}
}

func TestStore_Revoke(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Approve("api", "h"); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke("api"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if s.Approved("api", "h") {
		t.Errorf("expected api to be revoked")
	}
}

func TestStore_RevokeUnknownIsNoOp(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Revoke("never-existed"); err != nil {
		t.Errorf("revoking unknown should be no-op, got %v", err)
	}
}

func TestStore_RevokeMissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	// File does not exist at all; Revoke should not create it nor error.
	if err := s.Revoke("api"); err != nil {
		t.Errorf("revoking on missing file should be no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "devcontainer-trust.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("revoke should not create the file when no-op; stat err=%v", err)
	}
}

func TestStore_ApprovedSkipsNonStringEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer-trust.json")
	// hand-written mixed-shape file
	if err := os.WriteFile(path, []byte(`{"api":"abc","weird":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if !s.Approved("api", "abc") {
		t.Errorf("string entry should round-trip")
	}
	if s.Approved("weird", "42") {
		t.Errorf("non-string entry should be skipped")
	}
}

func TestStore_ApprovedReturnsFalseOnParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer-trust.json")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	// load() returns an error; Approved swallows it and reports false.
	if s.Approved("api", "x") {
		t.Errorf("expected Approved=false when load fails")
	}
}

func TestStore_ApproveReturnsLoadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer-trust.json")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if err := s.Approve("api", "x"); err == nil {
		t.Fatal("expected Approve to propagate load error")
	}
}

func TestStore_RevokeReturnsLoadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer-trust.json")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if err := s.Revoke("api"); err == nil {
		t.Fatal("expected Revoke to propagate load error")
	}
}

func TestStore_PathLayout(t *testing.T) {
	s := NewStore("/tmp/x")
	want := filepath.Join("/tmp/x", "devcontainer-trust.json")
	if got := s.path(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStore_RevokeWritesFileWhenEntryExists(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Approve("api", "h"); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke("api"); err != nil {
		t.Fatal(err)
	}
	// File should still exist (empty map) since we wrote through.
	if _, err := os.Stat(filepath.Join(dir, "devcontainer-trust.json")); err != nil {
		t.Errorf("expected file to exist after revoke, got stat err=%v", err)
	}
}
