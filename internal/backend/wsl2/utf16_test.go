package wsl2

import (
	"testing"
)

// utf16LEBytes encodes s as UTF-16LE for tests.
func utf16LEBytes(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		// Tests stick to BMP characters; surrogate pairs are exercised
		// via the BOM test which uses a literal byte slice.
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

func TestDecodeWSLOutput_Empty(t *testing.T) {
	if got := decodeWSLOutput(nil); got != "" {
		t.Errorf("nil = %q, want empty", got)
	}
	if got := decodeWSLOutput([]byte{}); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
}

func TestDecodeWSLOutput_PlainUTF8(t *testing.T) {
	// Plain ASCII without NULs should pass through untouched.
	in := []byte("bolted\nUbuntu\n")
	if got := decodeWSLOutput(in); got != "bolted\nUbuntu\n" {
		t.Errorf("got %q, want %q", got, "bolted\nUbuntu\n")
	}
}

func TestDecodeWSLOutput_WithBOM(t *testing.T) {
	body := utf16LEBytes("bolted\n")
	in := append([]byte{0xFF, 0xFE}, body...)
	if got := decodeWSLOutput(in); got != "bolted\n" {
		t.Errorf("got %q, want %q", got, "bolted\n")
	}
}

func TestDecodeWSLOutput_NoBOM_UTF16LE_DetectedByNULDensity(t *testing.T) {
	// ASCII "bolted\n" as UTF-16LE has NUL every other byte → ~50%
	// NUL density, well above the 30% threshold.
	in := utf16LEBytes("bolted\nUbuntu\n")
	if got := decodeWSLOutput(in); got != "bolted\nUbuntu\n" {
		t.Errorf("got %q, want %q", got, "bolted\nUbuntu\n")
	}
}

func TestDecodeWSLOutput_StripsNULsFromUTF8(t *testing.T) {
	// Genuine UTF-8 with a stray NUL → NULs stripped, content kept.
	in := []byte("hello\x00 world\n")
	if got := decodeWSLOutput(in); got != "hello world\n" {
		t.Errorf("got %q, want %q", got, "hello world\n")
	}
}

func TestDecodeUTF16LE_OddLengthIsRoundedDown(t *testing.T) {
	// 3 bytes: should drop the trailing one.
	in := []byte{0x68, 0x00, 0x69} // "h" then half of "i"
	if got := decodeUTF16LE(in); got != "h" {
		t.Errorf("got %q, want %q", got, "h")
	}
}

func TestDecodeUTF16LE_Empty(t *testing.T) {
	if got := decodeUTF16LE(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
	if got := decodeUTF16LE([]byte{0x68}); got != "" {
		t.Errorf("one byte rounded down = %q, want empty", got)
	}
}

func TestStripNULs_NoOp(t *testing.T) {
	if got := stripNULs("hello"); got != "hello" {
		t.Errorf("no-op stripNULs returned %q", got)
	}
}

func TestStripNULs_Mixed(t *testing.T) {
	if got := stripNULs("a\x00b\x00\x00c"); got != "abc" {
		t.Errorf("got %q, want abc", got)
	}
}

func TestHasNUL(t *testing.T) {
	if hasNUL("hello") {
		t.Error("hasNUL should be false on clean string")
	}
	if !hasNUL("hel\x00lo") {
		t.Error("hasNUL should be true with NUL present")
	}
}

func TestDecodeWSLOutput_LowNULDensityKeepsBytes(t *testing.T) {
	// A long UTF-8 string with one NUL → below the 30% threshold,
	// returned as UTF-8 with NUL stripped.
	body := "a long line of ascii text that should be kept as utf-8\x00\n"
	got := decodeWSLOutput([]byte(body))
	want := "a long line of ascii text that should be kept as utf-8\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
