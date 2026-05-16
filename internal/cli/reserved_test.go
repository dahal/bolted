package cli

import (
	"sort"
	"testing"
)

func TestReservedSubcommands_AlphabetisedAndUnique(t *testing.T) {
	list := ReservedSubcommands()
	seen := make(map[string]bool, len(list))
	for _, n := range list {
		if seen[n] {
			t.Errorf("duplicate reserved subcommand: %q", n)
		}
		seen[n] = true
	}
	sorted := make([]string, len(list))
	copy(sorted, list)
	sort.Strings(sorted)
	for i := range list {
		if list[i] != sorted[i] {
			t.Errorf("reserved list not sorted: at index %d got %q, want %q", i, list[i], sorted[i])
		}
	}
}

func TestIsReserved(t *testing.T) {
	if !isReserved("init") {
		t.Error("expected init to be reserved")
	}
	if !isReserved("version") {
		t.Error("expected version to be reserved")
	}
	if isReserved("git") {
		t.Error("expected git not to be reserved")
	}
	if isReserved("") {
		t.Error("expected empty string not to be reserved")
	}
	if isReserved("INIT") {
		t.Error("reserved lookup should be case-sensitive")
	}
}

func TestReservedSubcommands_ReturnsCopy(t *testing.T) {
	a := ReservedSubcommands()
	if len(a) == 0 {
		t.Fatal("expected non-empty list")
	}
	a[0] = "MUTATED"
	b := ReservedSubcommands()
	if b[0] == "MUTATED" {
		t.Error("ReservedSubcommands should return a copy, not the underlying slice")
	}
}
