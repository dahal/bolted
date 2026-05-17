package devcontainertrust

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// isTerminalFn is the indirection point for TTY detection. Tests override
// it to simulate interactive / non-interactive stdin.
//
// Production implementation: only a real *os.File whose Stat reports the
// ModeCharDevice bit counts as a TTY. A *bytes.Buffer, pipe, or regular
// file all return false — matching the "non-TTY without --trust must
// abort" rule.
var isTerminalFn = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// Confirm prints summary to out, asks the user "Allow this devcontainer to
// run? [y/N]", and reads one line from in. The default (empty line or
// anything not starting with y/Y) is "no". Returns (true, nil) only on an
// explicit yes.
//
// If in is not an interactive terminal, Confirm refuses outright and
// returns (false, ErrNoTTY) without reading anything. Callers should
// suggest the `--trust` flag to approve non-interactively (CI use).
func Confirm(in io.Reader, out io.Writer, summary string) (bool, error) {
	if !isTerminalFn(in) {
		return false, ErrNoTTY
	}
	if summary != "" {
		if _, err := fmt.Fprintln(out, summary); err != nil {
			return false, err
		}
	}
	if _, err := fmt.Fprint(out, "Allow this devcontainer to run? [y/N] "); err != nil {
		return false, err
	}
	br := bufio.NewReader(in)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	line = strings.TrimSpace(line)
	if line == "y" || line == "Y" || line == "yes" || line == "YES" || line == "Yes" {
		return true, nil
	}
	return false, nil
}
