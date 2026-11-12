// Package cmdline writes FFmpeg commands as text that shells accept, and
// reads such text back into arguments.
package cmdline

import (
	"errors"
	"runtime"
	"strings"
	"unicode"
)

// Windows is set when commands are written for Windows shells.
var Windows = runtime.GOOS == "windows"

// Join writes program and args as one command line. Arguments a shell would
// change are quoted: with single quotes for POSIX shells, and with double
// quotes for cmd.exe and PowerShell when windows is set. Split reads the
// result back into the same arguments.
func Join(program string, args []string, windows bool) string {
	parts := []string{Quote(program, windows)}
	for _, a := range args {
		parts = append(parts, Quote(a, windows))
	}
	return strings.Join(parts, " ")
}

// Quote quotes a for a shell if it needs it.
func Quote(a string, windows bool) string {
	if a != "" && !strings.ContainsFunc(a, func(r rune) bool { return !safe(r, windows) }) {
		return a
	}
	if windows && !strings.Contains(a, `"`) {
		return `"` + a + `"`
	}
	// A single quote ends the quoted part, adds a double-quoted ', and
	// starts a new quoted part.
	return "'" + strings.ReplaceAll(a, "'", `'"'"'`) + "'"
}

// safe reports runes that no supported shell treats specially. Windows
// paths need backslashes and colons; PowerShell splits on commas.
func safe(r rune, windows bool) bool {
	switch {
	case r > unicode.MaxASCII:
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return true
	case windows:
		return strings.ContainsRune(`_-+=:./\@`, r)
	}
	return strings.ContainsRune("_-+=:,./@%^", r)
}

// Split reads a command line into arguments. Spaces separate arguments.
// Text in single or double quotes is kept as it is, spaces included, and
// quoted parts next to each other or to other text join into one argument.
// Backslashes have no special meaning, so Windows paths need no doubling.
func Split(s string) ([]string, error) {
	var (
		args  []string
		cur   strings.Builder
		inArg bool
		quote rune
	)
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case unicode.IsSpace(r):
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("a quote is not closed: " + string(quote))
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
