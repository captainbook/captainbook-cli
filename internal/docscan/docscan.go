// Package docscan holds the shell-parsing helpers the documentation-drift tests
// share.
//
// It exists because they were duplicated, and the copies had already diverged in
// correctness: the inventory scanner's cutter is quote-aware, having been hardened
// after naive truncation at a `)` inside quotes silently dropped flags and let an
// incomplete command validate as a passing prefix. A second scanner written later
// for the non-inventory tree reintroduced exactly that bug. One implementation
// cannot drift from itself.
//
// Non-test package because two different test packages (cmd and cmd/inventory)
// need it, and Go gives test files no other way to share code. Internal, so it is
// not public API.
package docscan

import "strings"

// ShellOperators end an invocation when they appear outside quotes.
//
// `)` is included because prose and markdown wrap commands in parentheses, and a
// trailing `)` is far more often punctuation than a subshell.
var ShellOperators = []string{"&&", "||", "|", ";", "#", ">", ")"}

// CutAtShellOperator truncates s at the first shell operator that is not inside
// single or double quotes.
//
// Backslash escapes the next character inside DOUBLE quotes only, matching POSIX
// sh: inside single quotes a backslash is literal, so `'\'` does not escape the
// closing quote.
//
// Quote awareness is the whole point. A naive strings.Index cut turns
// `--label "Shoe size (EU)"` into `--label "Shoe size (EU` and then validates that
// truncated prefix, reporting success on a command it never finished reading — so
// the test passes while the documented example is wrong.
func CutAtShellOperator(s string) string {
	var inSingle, inDouble bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
			continue
		case inDouble:
			if c == '\\' {
				i++ // skip the escaped character
				continue
			}
			if c == '"' {
				inDouble = false
			}
			continue
		case c == '\'':
			inSingle = true
			continue
		case c == '"':
			inDouble = true
			continue
		}
		for _, op := range ShellOperators {
			if strings.HasPrefix(s[i:], op) {
				return s[:i]
			}
		}
	}
	return s
}

// SameSet reports whether a and b contain the same elements, ignoring order and
// duplicates.
func SameSet(a, b []string) bool {
	seen := make(map[string]struct{}, len(a))
	for _, x := range a {
		seen[x] = struct{}{}
	}
	other := make(map[string]struct{}, len(b))
	for _, x := range b {
		other[x] = struct{}{}
	}
	if len(seen) != len(other) {
		return false
	}
	for x := range seen {
		if _, ok := other[x]; !ok {
			return false
		}
	}
	return true
}
