package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPassthroughStub_PrintsMessageAndExitsNonZero(t *testing.T) {
	var buf bytes.Buffer
	old := stderr
	stderr = &buf
	defer func() { stderr = old }()

	code := passthroughStub([]string{"git", "clone", "https://example.com"})
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	got := buf.String()
	if !strings.Contains(got, "spec 11") {
		t.Errorf("expected message to mention spec 11, got: %q", got)
	}
	if !strings.Contains(got, `"git"`) {
		t.Errorf("expected message to mention the command name, got: %q", got)
	}
}

func TestPassthroughStub_NoArgs(t *testing.T) {
	var buf bytes.Buffer
	old := stderr
	stderr = &buf
	defer func() { stderr = old }()

	code := passthroughStub(nil)
	if code == 0 {
		t.Fatal("expected non-zero exit on no args")
	}
	if !strings.Contains(buf.String(), "no command given") {
		t.Errorf("expected friendly message, got: %q", buf.String())
	}
}
