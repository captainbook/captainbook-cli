package inventory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderFn lets each fixture pick its tuned renderer. The signature
// matches all RenderXxxDiff funcs.
type renderFn func(w *bytes.Buffer, id string, env DiffEnvelope) error

func TestRenderDiff_GoldenFiles(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		render   renderFn
		resource string
	}{
		{
			name:     "product price update",
			fixture:  "product_update_price",
			resource: "42",
			render: func(w *bytes.Buffer, id string, env DiffEnvelope) error {
				return RenderProductDiff(w, id, env)
			},
		},
		{
			name:     "product publish status transition",
			fixture:  "product_update_status",
			resource: "99",
			render: func(w *bytes.Buffer, id string, env DiffEnvelope) error {
				return RenderProductDiff(w, id, env)
			},
		},
		{
			name:     "booking cancel",
			fixture:  "booking_cancel",
			resource: "bk_01H8XYZA",
			render: func(w *bytes.Buffer, id string, env DiffEnvelope) error {
				return RenderBookingDiff(w, id, env)
			},
		},
		{
			name:     "discount create",
			fixture:  "discount_create",
			resource: "7",
			render: func(w *bytes.Buffer, id string, env DiffEnvelope) error {
				return RenderDiscountDiff(w, id, env)
			},
		},
		{
			name:     "gift cert void",
			fixture:  "giftcert_void",
			resource: "gc_01H8AAAA",
			render: func(w *bytes.Buffer, id string, env DiffEnvelope) error {
				return RenderGiftCertificateDiff(w, id, env)
			},
		},
		{
			name:     "pricing tier amount update",
			fixture:  "pricingtier_update",
			resource: "tier_42",
			render: func(w *bytes.Buffer, id string, env DiffEnvelope) error {
				return RenderPricingTierDiff(w, id, env)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := loadEnvelope(t, tc.fixture+".golden.json")
			var buf bytes.Buffer
			if err := tc.render(&buf, tc.resource, env); err != nil {
				t.Fatalf("render: %v", err)
			}
			got := buf.String()
			expectedPath := filepath.Join("testdata", "diff", tc.fixture+".expected.txt")
			compareGolden(t, expectedPath, got)
		})
	}
}

func TestRenderDiff_GenericArbitraryFields(t *testing.T) {
	env := DiffEnvelope{
		WouldApply: true,
		Diff: DiffPayload{
			Before: map[string]interface{}{
				"id":    "q_99",
				"label": "Old label",
				"order": float64(3),
			},
			After: map[string]interface{}{
				"id":    "q_99",
				"label": "New label",
				"order": float64(5),
			},
		},
	}
	var buf bytes.Buffer
	if err := RenderDiff(&buf, "Question", env); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	mustContain(t, out, "Dry-run preview for Question q_99 (would_apply: true):")
	mustContain(t, out, "label:")
	mustContain(t, out, `"Old label"`)
	mustContain(t, out, `"New label"`)
	mustContain(t, out, "order:")
	mustContain(t, out, "3")
	mustContain(t, out, "5")
	mustContain(t, out, "Run without --dry-run to apply.")
	// id is unchanged → must NOT show as a diff line
	if strings.Contains(out, "  - id:") {
		t.Errorf("unchanged id field leaked into diff output:\n%s", out)
	}
	// no ANSI when writing to bytes.Buffer
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI escape leaked into non-TTY output:\n%s", out)
	}
}

func TestRenderDiff_EmptyDiff(t *testing.T) {
	env := DiffEnvelope{
		WouldApply: true,
		Diff: DiffPayload{
			Before: map[string]interface{}{"id": "1", "title": "Same"},
			After:  map[string]interface{}{"id": "1", "title": "Same"},
		},
	}
	var buf bytes.Buffer
	if err := RenderDiff(&buf, "Product", env); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	mustContain(t, out, "(no field changes)")
	mustContain(t, out, "Run without --dry-run to apply.")
}

func TestRenderDiff_WouldApplyFalse(t *testing.T) {
	env := DiffEnvelope{
		WouldApply: false,
		Diff: DiffPayload{
			Before: map[string]interface{}{"id": "5", "status": "draft"},
			After:  map[string]interface{}{"id": "5", "status": "published"},
		},
	}
	var buf bytes.Buffer
	if err := RenderDiff(&buf, "Product", env); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	mustContain(t, out, "(would_apply: false)")
	mustContain(t, out, "Server reports would_apply=false")
	if strings.Contains(out, "Run without --dry-run") {
		t.Errorf("would_apply=false should not show 'Run without --dry-run' line:\n%s", out)
	}
}

func TestRenderDiff_TTYColorPath(t *testing.T) {
	// Use an os.Pipe; the read end is a *os.File but not a TTY, so the
	// renderer should emit no ANSI codes. This proves the TTY check is
	// gating the colour output (we cannot easily fake a real PTY in a
	// portable unit test).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	env := DiffEnvelope{
		WouldApply: true,
		Diff: DiffPayload{
			Before: map[string]interface{}{"id": "1", "title": "A"},
			After:  map[string]interface{}{"id": "1", "title": "B"},
		},
	}
	go func() {
		_ = RenderDiff(w, "Product", env)
		w.Close()
	}()
	out, err := readAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("pipe (non-TTY *os.File) produced ANSI escapes:\n%s", out)
	}

	// Sanity check: the colour helper still resolves ANSI when forced
	// on. This locks the colour code constants.
	opts := renderOptions{color: true, resourceType: "Product"}
	line := formatChange(FieldChange{Path: "title", Before: "A", After: "B"}, len("title"), opts)
	if !strings.Contains(line, "\x1b[31m") || !strings.Contains(line, "\x1b[32m") {
		t.Errorf("color=true did not produce red/green ANSI codes: %q", line)
	}
}

func TestRenderDiff_SideEffects(t *testing.T) {
	env := DiffEnvelope{
		WouldApply: true,
		Diff: DiffPayload{
			Before: map[string]interface{}{"id": "1", "status": "active"},
			After:  map[string]interface{}{"id": "1", "status": "void"},
		},
		SideEffects: []SideEffect{
			{Type: "mail", Identifier: "GiftCertVoidedMail", PayloadSummary: "to: alice@example.com"},
			{Type: "stripe", Identifier: "refund:re_xyz", PayloadSummary: "amount: 5000 EUR"},
		},
	}
	var buf bytes.Buffer
	if err := RenderGiftCertificateDiff(&buf, "gc_1", env); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	mustContain(t, out, "Side effects (would run on apply):")
	mustContain(t, out, "mail GiftCertVoidedMail: to: alice@example.com")
	mustContain(t, out, "stripe refund:re_xyz: amount: 5000 EUR")
}

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		name     string
		v        interface{}
		currency string
		want     string
	}{
		{"euro 12.00", float64(1200), "EUR", "€ 12.00"},
		{"euro 0.01", float64(1), "EUR", "€ 0.01"},
		{"euro negative", float64(-2599), "EUR", "-€ 25.99"},
		{"jpy whole units", float64(1500), "JPY", "¥ 1500"},
		{"unknown currency", float64(1500), "ZAR", "ZAR 15.00"},
		{"empty currency", float64(1500), "", "15.00"},
		{"int input", int(500), "EUR", "€ 5.00"},
		{"int64 input", int64(500), "EUR", "€ 5.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := formatMoney(tc.v, tc.currency)
			if !ok {
				t.Fatalf("formatMoney(%v, %q) returned ok=false", tc.v, tc.currency)
			}
			if got != tc.want {
				t.Errorf("formatMoney(%v, %q): got %q, want %q", tc.v, tc.currency, got, tc.want)
			}
		})
	}
}

func TestFormatMoney_NonNumeric(t *testing.T) {
	if _, ok := formatMoney("nope", "EUR"); ok {
		t.Errorf("expected ok=false on non-numeric input")
	}
}

func TestFormatTimestamp(t *testing.T) {
	got, ok := formatTimestamp("2026-04-27T10:30:00Z")
	if !ok || got != "2026-04-27 10:30 UTC" {
		t.Errorf("formatTimestamp: got %q ok=%v, want %q ok=true", got, ok, "2026-04-27 10:30 UTC")
	}
	if _, ok := formatTimestamp("not a timestamp"); ok {
		t.Errorf("expected ok=false on bad timestamp")
	}
	if _, ok := formatTimestamp(123); ok {
		t.Errorf("expected ok=false on non-string input")
	}
}

func loadEnvelope(t *testing.T, name string) DiffEnvelope {
	t.Helper()
	path := filepath.Join("testdata", "diff", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var env DiffEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return env
}

func compareGolden(t *testing.T, expectedPath, got string) {
	t.Helper()
	want, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("read expected %s: %v", expectedPath, err)
	}
	if string(want) != got {
		t.Errorf("rendered output does not match %s\n--- want ---\n%s\n--- got ---\n%s",
			expectedPath, string(want), got)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("output missing %q:\n%s", needle, haystack)
	}
}

// readAll drains a reader to a string. Avoids io.ReadAll just so we
// don't pull in additional imports; the renderer's output is small.
func readAll(r interface{ Read(p []byte) (int, error) }) (string, error) {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				return b.String(), nil
			}
			return b.String(), err
		}
	}
}
