package cli

import (
	"fmt"
	"io"
	"unicode"
)

// passwordScore returns a coarse 0..4 strength score for the supplied
// password using only the standard library. Intentionally NOT a real
// zxcvbn — that ships a multi-megabyte dictionary. The intent is to
// flag obvious junk during `bolt init` / `bolt passwd`, not to defend
// against motivated attackers (Argon2id + a slow KDF does that part).
//
// Scoring inputs:
//
//   - length: short passwords cap the score hard regardless of variety.
//   - character-class diversity: lower, upper, digit, symbol. Each
//     extra class bumps the score by one (capped at 4).
//   - repeat penalty: if more than half the runes are the same byte,
//     reduce the score by one — guards against "aaaaaaaaaa".
//
// The score ranges:
//
//	0 — trivially weak (very short, or extreme repetition).
//	1 — weak (short or single character class).
//	2 — fair (length OK, two classes).
//	3 — good (length OK, three+ classes).
//	4 — strong (long, three+ classes, no heavy repetition).
//
// The function does not allocate beyond a small fixed set.
func passwordScore(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	// Length tier.
	var lengthScore int
	switch {
	case len(b) < 6:
		lengthScore = 0
	case len(b) < 10:
		lengthScore = 1
	case len(b) < 14:
		lengthScore = 2
	case len(b) < 20:
		lengthScore = 3
	default:
		lengthScore = 4
	}

	// Character-class diversity.
	var hasLower, hasUpper, hasDigit, hasSymbol bool
	repeats := make(map[byte]int, len(b))
	mostCommon := 0
	for _, r := range b {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case unicode.IsPrint(rune(r)):
			hasSymbol = true
		}
		repeats[r]++
		if repeats[r] > mostCommon {
			mostCommon = repeats[r]
		}
	}
	var classes int
	for _, b := range []bool{hasLower, hasUpper, hasDigit, hasSymbol} {
		if b {
			classes++
		}
	}

	score := lengthScore
	// Diversity bonus: +1 for each extra class beyond the first, capped.
	if classes >= 2 {
		score++
	}
	if classes >= 3 {
		score++
	}
	if classes >= 4 {
		score++
	}
	// Heavy-repetition penalty (>50% same byte): such a password is
	// effectively a one-byte secret regardless of how long it is, so
	// we cap the score at 1 — never above "weak".
	if mostCommon*2 > len(b) {
		score--
		if score > 1 {
			score = 1
		}
	}
	// Clamp to [0, 4].
	if score < 0 {
		score = 0
	}
	if score > 4 {
		score = 4
	}
	return score
}

// minStrongScore is the threshold below which WarnIfWeak emits a
// warning. Lifted to a package var so tests (and future config) can
// tweak it without rewriting WarnIfWeak.
var minStrongScore = 3

// WarnIfWeak prints a single-line warning to w when the password's
// score is below minStrongScore. Returns without writing anything if
// `insecure` is true (callers wire this to the --insecure-password
// flag) or the score is already strong.
//
// Exposed for `bolt init` to call after the password is read but before
// the volume is created. Lifecycle code wires it in separately so
// spec 17 and spec 03 stay independently mergeable.
func WarnIfWeak(w io.Writer, b []byte, insecure bool) {
	if insecure {
		return
	}
	score := passwordScore(b)
	if score >= minStrongScore {
		return
	}
	fmt.Fprintf(w, "warning: password strength score %d/4 — consider a longer, more varied passphrase (use --insecure-password to suppress)\n", score)
}
