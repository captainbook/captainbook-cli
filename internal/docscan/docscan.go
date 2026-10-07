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

import (
	"sort"
	"strings"
)

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

// SameValues reports whether a and b hold the same values in any order, counting
// DUPLICATES: ["a","a","b"] and ["a","b"] are different.
//
// Duplicate sensitivity is deliberate and was nearly lost. Both callers compare a
// spec enum against a CLI enum, where a repeated value on one side and not the
// other is itself a defect — exactly the drift these tests exist to report. A
// map-based set comparison (which an earlier extraction of this helper used) makes
// the check strictly more permissive and silently stops failing on it.
//
// Named SameValues rather than SameSet because "set" is what invited that mistake.
func SameValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
