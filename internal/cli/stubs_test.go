package cli

import (
	"strings"
	"testing"
)

func TestStubCommand_ReturnsErrorReferencingSpec(t *testing.T) {
	cmd := newStubCmd("init")
	if cmd.Use != "init" {
		t.Errorf("expected Use=init, got %q", cmd.Use)
	}
	if !strings.Contains(cmd.Short, "spec 09") {
		t.Errorf("expected short to mention spec 09, got %q", cmd.Short)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error from stub")
	}
	if !strings.Contains(err.Error(), "spec 09") {
		t.Errorf("expected error to reference spec 09, got: %v", err)
	}
}

func TestStubCommand_UnknownSpecStillErrors(t *testing.T) {
	cmd := newStubCmd("frobnicate")
	if cmd.Short != "Not yet implemented" {
		t.Errorf("expected generic short for unknown spec, got %q", cmd.Short)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error from stub")
	}
}

func TestRegisterSubcommands_EveryReservedNamePresent(t *testing.T) {
	root := newRootCmd("bolt")
	registered := make(map[string]bool)
	for _, c := range root.Commands() {
		registered[c.Use] = true
	}
	for _, name := range reservedSubcommands {
		if commandsProvidedByCobra[name] {
			// Cobra registers "help" lazily; "completion" is added when it
			// first runs. Skip checking those here.
			continue
		}
		if !registered[name] {
			t.Errorf("reserved subcommand %q is not registered on root", name)
		}
	}
}

func TestRegisterSubcommands_VersionImplementedNotStubbed(t *testing.T) {
	root := newRootCmd("bolt")
	for _, c := range root.Commands() {
		if c.Use != "version" {
			continue
		}
		if strings.Contains(c.Short, "Not yet implemented") {
			t.Errorf("version should be implemented, but Short=%q", c.Short)
		}
		return
	}
	t.Fatal("version command not registered")
}

func TestStubSpec_AllMappingsReferenceExistingSpecs(t *testing.T) {
	// Defensive: every value should look like "spec NN" so users can find it.
	for name, ref := range stubSpec {
		if !strings.HasPrefix(ref, "spec ") {
			t.Errorf("stubSpec[%q] = %q, expected prefix 'spec '", name, ref)
		}
	}
}
