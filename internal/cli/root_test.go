package cli

import (
	"log/slog"
	"strings"
	"testing"
)

func TestParseLogLevel_Valid(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for in, want := range cases {
		got, err := parseLogLevel(in)
		if err != nil {
			t.Errorf("parseLogLevel(%q) returned error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseLogLevel_Invalid(t *testing.T) {
	_, err := parseLogLevel("trace")
	if err == nil {
		t.Fatal("expected error for unknown level")
	}
	if !strings.Contains(err.Error(), "invalid log level") {
		t.Errorf("error should mention 'invalid log level', got: %v", err)
	}
}

func TestConfigureLogger_Valid(t *testing.T) {
	if err := configureLogger("info"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigureLogger_Invalid(t *testing.T) {
	if err := configureLogger("nope"); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestLongDescription_EmbedsInvokedName(t *testing.T) {
	for _, name := range []string{"bolt"} {
		got := longDescription(name)
		if !strings.Contains(got, `"`+name+`"`) {
			t.Errorf("expected long description to embed %q in quotes, got: %s", name, got)
		}
	}
}

func TestNewRootCmd_HasLogLevelFlag(t *testing.T) {
	cmd := newRootCmd("bolt")
	flag := cmd.PersistentFlags().Lookup("log-level")
	if flag == nil {
		t.Fatal("expected --log-level persistent flag to be registered")
	}
	if flag.DefValue != "warn" {
		t.Errorf("expected --log-level default 'warn', got %q", flag.DefValue)
	}
}

func TestNewRootCmd_UseMatchesInvokedName(t *testing.T) {
	for _, name := range []string{"bolt"} {
		cmd := newRootCmd(name)
		if cmd.Use != name {
			t.Errorf("expected Use=%q, got %q", name, cmd.Use)
		}
	}
}
