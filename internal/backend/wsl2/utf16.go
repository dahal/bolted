package wsl2

import (
	"encoding/binary"
	"unicode/utf16"
)

// decodeWSLOutput converts the raw bytes returned by `wsl.exe` into a UTF-8
// string. WSL emits UTF-16LE on most builds — with a BOM on older
// versions, without on newer — and historically has been inconsistent
// about trailing NUL bytes between lines. The decoder accepts either
// encoding so callers don't have to care which build of WSL they're
// talking to.
//
// Heuristic:
//
//   - If the buffer starts with the UTF-16LE BOM (0xFF 0xFE), strip it
//     and decode as UTF-16LE.
//   - Else if more than 30% of the bytes are NUL, treat it as
//     BOM-less UTF-16LE.
//   - Otherwise return the buffer verbatim as UTF-8.
//
// NUL bytes in the decoded result are stripped — they show up when WSL
// emits a trailing UTF-16LE word for line padding.
func decodeWSLOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}

	// Strip a leading UTF-16LE BOM if present.
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		return decodeUTF16LE(b[2:])
	}

	// Heuristic: if a meaningful fraction of bytes are NUL, the buffer
	// is almost certainly BOM-less UTF-16LE (ASCII chars expand to
	// `<byte> 0x00`). The threshold of ~30% is conservative enough to
	// avoid misclassifying genuine UTF-8 with the odd embedded NUL.
	nulCount := 0
	for _, c := range b {
		if c == 0 {
			nulCount++
		}
	}
	if nulCount*10 > len(b)*3 {
		return decodeUTF16LE(b)
	}

	return stripNULs(string(b))
}

// decodeUTF16LE decodes a UTF-16LE byte sequence to a UTF-8 string. An
// odd-length input is rounded down (the trailing half-word is silently
// dropped — it's malformed and there's nothing useful we can do with it).
func decodeUTF16LE(b []byte) string {
	// Round down to an even byte count.
	n := len(b) &^ 1
	if n == 0 {
		return ""
	}
	u16 := make([]uint16, n/2)
	for i := 0; i < n; i += 2 {
		u16[i/2] = binary.LittleEndian.Uint16(b[i : i+2])
	}
	runes := utf16.Decode(u16)
	return stripNULs(string(runes))
}

// stripNULs removes ASCII NUL characters from s. They appear in wsl.exe
// output as line-padding artefacts and break callers that try to split on
// "\n".
func stripNULs(s string) string {
	if !hasNUL(s) {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != 0 {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// hasNUL reports whether s contains an ASCII NUL byte.
func hasNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}
