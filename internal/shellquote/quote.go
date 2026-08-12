// Package shellquote provides POSIX shell quoting helpers shared by the CLI
// and the command executor.
package shellquote

import "strings"

// safeChars can appear unquoted in a POSIX shell command line.
const safeChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

// Quote returns s safe for embedding in a POSIX shell command line.
// Strings made only of shell-safe characters pass through unchanged;
// everything else is wrapped in single quotes with embedded single quotes
// escaped. Unlike Go's %q verb this never corrupts newlines or other
// control characters.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool { return !strings.ContainsRune(safeChars, r) }) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
