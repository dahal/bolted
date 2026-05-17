package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// passwordPrompter abstracts the password-reading surface so init/unlock
// can be unit-tested without a real TTY. Implementations must zero the
// returned buffer when callers indicate they're done (callers call Zero on
// the buffer after passing it to Volume operations).
type passwordPrompter interface {
	// Prompt reads a single password from the user with a labelled
	// prompt. No echo.
	Prompt(label string) ([]byte, error)
	// PromptTwiceConfirm asks twice with confirmation; returns
	// errPasswordMismatch on mismatch. Empty passwords are rejected.
	PromptTwiceConfirm(label string) ([]byte, error)
	// FromStdin reads up to one line from stdin (for the
	// --password-stdin flow). No prompt is rendered.
	FromStdin() ([]byte, error)
}

// errPasswordMismatch is returned by PromptTwiceConfirm when the two
// inputs differ.
var errPasswordMismatch = errors.New("passwords do not match")

// errEmptyPassword is returned for an empty input.
var errEmptyPassword = errors.New("password cannot be empty")

// errNotATerminal is returned when an interactive prompt is requested but
// stdin isn't a TTY (and the caller didn't pass --password-stdin).
var errNotATerminal = errors.New("stdin is not a terminal; use --password-stdin to supply the password")

// ttyPrompter is the production prompter. It writes the label to stderr
// and reads from /dev/tty (or stdin's fd on Windows) without echo.
type ttyPrompter struct {
	stdin  *os.File
	stderr io.Writer
}

// newTTYPrompter wires the prompter against os.Stdin / os.Stderr.
func newTTYPrompter() *ttyPrompter {
	return &ttyPrompter{stdin: os.Stdin, stderr: os.Stderr}
}

// isTerminalFn is the indirection point for stdin TTY detection. Tests
// swap it.
var isTerminalFn = term.IsTerminal

// readPasswordFn is the indirection point for the no-echo read. Tests swap
// it to return canned bytes.
var readPasswordFn = term.ReadPassword

func (p *ttyPrompter) Prompt(label string) ([]byte, error) {
	if !isTerminalFn(int(p.stdin.Fd())) {
		return nil, errNotATerminal
	}
	if _, err := fmt.Fprint(p.stderr, label); err != nil {
		return nil, err
	}
	pw, err := readPasswordFn(int(p.stdin.Fd()))
	// readPassword does not print a newline; emit one for visual balance.
	fmt.Fprintln(p.stderr)
	if err != nil {
		return nil, err
	}
	return pw, nil
}

func (p *ttyPrompter) PromptTwiceConfirm(label string) ([]byte, error) {
	first, err := p.Prompt(label)
	if err != nil {
		return nil, err
	}
	if len(first) == 0 {
		zero(first)
		return nil, errEmptyPassword
	}
	second, err := p.Prompt("Confirm: ")
	if err != nil {
		zero(first)
		return nil, err
	}
	if !bytes.Equal(first, second) {
		zero(first)
		zero(second)
		return nil, errPasswordMismatch
	}
	zero(second)
	return first, nil
}

func (p *ttyPrompter) FromStdin() ([]byte, error) {
	return readPasswordFromReader(p.stdin)
}

// readPasswordFromReader reads up to and including the first newline,
// returns the trimmed bytes. Used by --password-stdin.
func readPasswordFromReader(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	line = bytes.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return nil, errEmptyPassword
	}
	return line, nil
}

// zero overwrites every byte of b with 0. Callers should defer this after
// passing the password to its consumer (e.g. Volume.Create / Volume.Open).
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
