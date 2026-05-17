package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPasswordScore(t *testing.T) {
	cases := []struct {
		name string
		pw   string
		want int
	}{
		// Empty / trivial.
		{"empty", "", 0},
		{"single", "a", 0},
		{"all_same_short", "aaaa", 0},

		// Short, one class.
		{"short_lower", "abcde", 0},
		{"short_digit", "12345", 0},

		// 6-9 chars, one class.
		{"lower_only_6", "abcdef", 1},
		{"digit_only_6", "123456", 1},

		// 6-9 chars, two classes.
		{"two_class_short", "abc123", 2},

		// 10-13 chars.
		{"two_class_med", "abcdef1234", 3},

		// 14+ chars with many classes.
		{"three_class_long", "abcdef1234ABCD", 4},

		// 20+ chars, strong.
		{"long_diverse", "abcDEF123!xyzPQR789zz", 4},

		// Heavy repetition penalty (>50% same byte) caps at score 1
		// regardless of length / class diversity.
		{"repeat_penalty", "aaaaaaaaaa1", 1},
		// Extreme repetition with multiple classes — still capped.
		{"repeat_penalty_strong_ish", "aaaaaaaaaaaaaaaA1!", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := passwordScore([]byte(tc.pw))
			if got != tc.want {
				t.Errorf("passwordScore(%q) = %d, want %d", tc.pw, got, tc.want)
			}
			if got < 0 || got > 4 {
				t.Errorf("score %d out of [0,4]", got)
			}
		})
	}
}

func TestPasswordScore_SymbolClass(t *testing.T) {
	// A password containing all four classes should bump score versus
	// the same length with fewer classes.
	scoreFour := passwordScore([]byte("Abc1!Abc1!"))
	scoreTwo := passwordScore([]byte("abc1abc1ab"))
	if scoreFour <= scoreTwo {
		t.Errorf("four-class score (%d) should beat two-class score (%d)", scoreFour, scoreTwo)
	}
}

func TestPasswordScore_ClassCovers_Upper(t *testing.T) {
	// All-upper should still register as one class (no bonus).
	if got := passwordScore([]byte("ABCDEF")); got != 1 {
		t.Errorf("ABCDEF: got %d want 1", got)
	}
}

// ---- WarnIfWeak ------------------------------------------------------------

func TestWarnIfWeak_WarnsBelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	WarnIfWeak(&buf, []byte("abc"), false)
	if !strings.Contains(buf.String(), "warning") {
		t.Errorf("expected warning, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "/4") {
		t.Errorf("expected score in output, got %q", buf.String())
	}
}

func TestWarnIfWeak_QuietWhenStrong(t *testing.T) {
	var buf bytes.Buffer
	WarnIfWeak(&buf, []byte("StrongerLongerPassword!23"), false)
	if buf.Len() != 0 {
		t.Errorf("expected no warning for strong password, got %q", buf.String())
	}
}

func TestWarnIfWeak_InsecureSuppresses(t *testing.T) {
	var buf bytes.Buffer
	WarnIfWeak(&buf, []byte("abc"), true)
	if buf.Len() != 0 {
		t.Errorf("--insecure-password should suppress, got %q", buf.String())
	}
}

func TestWarnIfWeak_AtThresholdIsQuiet(t *testing.T) {
	// minStrongScore is 3 — find a pw that scores exactly 3.
	var buf bytes.Buffer
	WarnIfWeak(&buf, []byte("abcdef1234"), false) // scored 3 above
	if buf.Len() != 0 {
		t.Errorf("score-3 password should not warn, got %q", buf.String())
	}
}
