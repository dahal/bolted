package cli

import (
	"strings"
	"testing"
)

func TestStubCommand_ReturnsErrorReferencingSpec(t *testing.T) {
	// "config" is the last reserved name without an implementation —
	// the per-key get/set CLI isn't built yet (spec 03 ships the loader,
	// not the CLI). Update once a stand-alone `bolt config` lands.
	cmd := newStubCmd("config")
	if cmd.Use != "config" {
		t.Errorf("expected Use=config, got %q", cmd.Use)
	}
	if !strings.Contains(cmd.Short, "spec 03") {
		t.Errorf("expected short to mention spec 03, got %q", cmd.Short)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error from stub")
	}
	if !strings.Contains(err.Error(), "spec 03") {
		t.Errorf("expected error to reference spec 03, got: %v", err)
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
		// cmd.Use can include argument annotations (e.g. "dev <repo>").
		// cmd.Name() returns just the leading word.
		registered[c.Name()] = true
		// Aliases also count as "registered" for routing purposes —
		// e.g. `passwd` is an alias of `password`.
		for _, a := range c.Aliases {
			registered[a] = true
		}
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
