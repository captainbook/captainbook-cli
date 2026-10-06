package docscan

import "testing"

// TestCutAtShellOperator_IsQuoteAware pins the behaviour whose absence let a
// documentation-drift test pass on a command it never finished reading.
//
// A naive strings.Index cut truncates at the first operator byte regardless of
// quoting, so `--label "Shoe size (EU)"` became `--label "Shoe size (EU` and the
// scanner then validated that prefix — reporting success while the documented
// example was wrong. The inventory scanner was hardened against this; a second
// scanner written later for the non-inventory docs reintroduced it, which is why
// both now call this one function.
func TestCutAtShellOperator_IsQuoteAware(t *testing.T) {
	cases := []struct{ in, want string }{
		// Operators INSIDE quotes are data, not operators.
		{`inventory questions create --label "Shoe size (EU)" --dry-run`, `inventory questions create --label "Shoe size (EU)" --dry-run`},
		{`stats browsing --from 2026-09-01 | jq .data`, `stats browsing --from 2026-09-01 `},
		{`segments create --conditions '{"filters":[{"op":"gte"}]}'`, `segments create --conditions '{"filters":[{"op":"gte"}]}'`},
		{`products list # a comment`, `products list `},
	}
	for _, tc := range cases {
		if got := CutAtShellOperator(tc.in); got != tc.want {
			t.Errorf("CutAtShellOperator(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// TestSameSet covers the order- and duplicate-insensitive comparison the two
// spec-drift suites share.
func TestSameSet(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{[]string{"a", "b"}, []string{"b", "a"}, true},
		{[]string{"a", "a", "b"}, []string{"b", "a"}, true},
		{[]string{"a"}, []string{"a", "b"}, false},
		{[]string{"a", "b"}, []string{"a", "c"}, false},
		{nil, nil, true},
		{[]string{"a"}, nil, false},
	}
	for _, tc := range cases {
		if got := SameSet(tc.a, tc.b); got != tc.want {
			t.Errorf("SameSet(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
