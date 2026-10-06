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

// TestSameValues pins the comparison the two spec-drift suites share — including
// its DUPLICATE sensitivity, which an earlier extraction of this helper silently
// removed by reaching for a map.
//
// Both callers compare a spec enum against a CLI enum. A value repeated on one side
// and not the other is itself drift, so a set comparison that ignores it makes the
// guard strictly more permissive and stops reporting a real mismatch.
func TestSameValues(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"same values, different order", []string{"a", "b"}, []string{"b", "a"}, true},
		{"a duplicate on one side only is DRIFT", []string{"a", "a", "b"}, []string{"b", "a"}, false},
		{"equal duplicates match", []string{"a", "a", "b"}, []string{"a", "b", "a"}, true},
		{"missing value", []string{"a"}, []string{"a", "b"}, false},
		{"different value", []string{"a", "b"}, []string{"a", "c"}, false},
		{"both empty", nil, nil, true},
		{"one empty", []string{"a"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameValues(tc.a, tc.b); got != tc.want {
				t.Errorf("SameValues(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
