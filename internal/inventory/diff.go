// Package inventory contains helpers for the inventory CLI v1 namespace.
//
// This file (diff.go) is Lane D of the parallelization plan: a renderer
// for dry-run mutation responses.
//
// # Diff envelope shape (canonical)
//
// The CLI v1 spec (api/inventory/cli-v1.yaml, schema MutationResult)
// formally types `data.diff` as a single object with `before` and `after`
// sub-objects, each holding the resource state:
//
//	diff:
//	  type: object
//	  properties:
//	    before: { type: object, additionalProperties: true }
//	    after:  { type: object, additionalProperties: true }
//
// We follow that shape verbatim. The renderer derives per-field changes
// by diffing the two maps client-side. If the spec ever evolves to a
// richer representation (e.g. server-side per-field changes with
// metadata) FieldChange below is the natural extension point.
//
// # Output
//
// RenderDiff writes a human-readable, optionally ANSI-coloured before /
// after diff to the supplied writer. ANSI is emitted only when the
// writer is a TTY (detected via mattn/go-isatty); piped or captured
// output gets plain text. JSON output mode is not handled here — the
// runMutation helper (Lane H) pretty-prints the response in that case.
//
// # Tuned vs generic renderers
//
// RenderDiff is generic: any envelope, falls back to fmt.Sprintf("%v",
// value). The five top-blast-radius resources (Product, Booking,
// Discount, GiftCertificate, PricingTier) get tuned renderers that
// order known fields, format Money values as currency, and format ISO
// date-times in a human-readable way.
package inventory

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
)

// DiffEnvelope is the dry-run / commit response payload (spec
// MutationResult schema).
//
// SideEffects is parsed and surfaced as a list at the bottom of the diff;
// full payload-summary detail is emitted to stdout in JSON output mode.
type DiffEnvelope struct {
	WouldApply  bool         `json:"would_apply"`
	Diff        DiffPayload  `json:"diff"`
	SideEffects []SideEffect `json:"side_effects,omitempty"`
}

// DiffPayload mirrors the spec's `diff` sub-object.
type DiffPayload struct {
	Before map[string]interface{} `json:"before"`
	After  map[string]interface{} `json:"after"`
}

// SideEffect mirrors the spec's `side_effects[]` sub-object.
type SideEffect struct {
	Type           string `json:"type"`
	Identifier     string `json:"identifier"`
	PayloadSummary string `json:"payload_summary"`
}

// FieldChange is the derived per-field change after diffing Before vs
// After. Created represents a field that was absent in Before; Removed
// represents a field that was absent in After.
type FieldChange struct {
	Path    string
	Before  interface{}
	After   interface{}
	Created bool // field was absent in Before
	Removed bool // field was absent in After
}

// renderOptions controls the formatting passes; the public Render APIs
// configure these from the writer + resource type.
type renderOptions struct {
	color           bool
	resourceType    string
	resourceID      string
	moneyFields     map[string]bool // field name → treat value as Money (minor units)
	timestampFields map[string]bool // field name → treat value as ISO date-time
	fieldOrder      []string        // preferred render order (tuned renderers)
	currency        string          // tenant currency hint (USD, EUR, …) when known
}

const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiDim    = "\x1b[2m"
)

// RenderDiff writes a generic dry-run diff. resourceType is used in the
// header (e.g. "Product", "ProductOption"). It performs no money or
// timestamp formatting beyond fmt.Sprintf.
func RenderDiff(w io.Writer, resourceType string, env DiffEnvelope) error {
	opts := renderOptions{
		color:        isWriterTTY(w),
		resourceType: resourceType,
		resourceID:   extractID(env.Diff),
	}
	return renderDiff(w, env, opts)
}

// RenderProductDiff renders a Product dry-run with money/timestamp
// tuning and a deliberate field order.
func RenderProductDiff(w io.Writer, productID string, env DiffEnvelope) error {
	opts := renderOptions{
		color:        isWriterTTY(w),
		resourceType: "Product",
		resourceID:   productID,
		moneyFields:  set("from_price"),
		timestampFields: set(
			"created_at", "updated_at", "deleted_at", "published_at",
		),
		fieldOrder: []string{
			"title", "slug", "status", "schedule_type", "from_price",
			"currency", "capacity", "cancellation_policy", "category_ids",
			"timezone", "description",
			"created_at", "updated_at", "deleted_at",
		},
		currency: extractStr(env.Diff, "currency"),
	}
	return renderDiff(w, env, opts)
}

// RenderBookingDiff renders a Booking dry-run with money/timestamp
// tuning.
func RenderBookingDiff(w io.Writer, bookingID string, env DiffEnvelope) error {
	opts := renderOptions{
		color:        isWriterTTY(w),
		resourceType: "Booking",
		resourceID:   bookingID,
		moneyFields: set(
			"total_amount", "paid_amount", "refunded_amount",
			"discount_total",
		),
		timestampFields: set(
			"starts_at", "ends_at", "held_until", "confirmed_at",
			"expires_at", "cancelled_at", "created_at", "updated_at",
			"deleted_at",
		),
		fieldOrder: []string{
			"booking_status", "reference", "starts_at", "ends_at",
			"total_amount", "paid_amount", "refunded_amount",
			"discount_total", "applied_discount_ids",
			"confirmed_at", "cancelled_at", "expires_at", "held_until",
			"created_at", "updated_at", "deleted_at",
		},
		currency: extractStr(env.Diff, "currency"),
	}
	return renderDiff(w, env, opts)
}

// RenderDiscountDiff renders a Discount dry-run.
func RenderDiscountDiff(w io.Writer, discountID string, env DiffEnvelope) error {
	opts := renderOptions{
		color:        isWriterTTY(w),
		resourceType: "Discount",
		resourceID:   discountID,
		moneyFields:  set("discounted_price"),
		timestampFields: set(
			"start_date", "end_date", "validity_start", "validity_end",
			"created_at", "updated_at", "deleted_at",
		),
		fieldOrder: []string{
			"code", "product_option_id", "discount_pct",
			"discounted_price", "nb_offers", "auto_apply",
			"start_date", "end_date", "validity_start", "validity_end",
			"discount_text", "discount_image",
			"created_at", "updated_at", "deleted_at",
		},
	}
	return renderDiff(w, env, opts)
}

// RenderGiftCertificateDiff renders a GiftCertificate dry-run.
func RenderGiftCertificateDiff(w io.Writer, giftCertID string, env DiffEnvelope) error {
	opts := renderOptions{
		color:        isWriterTTY(w),
		resourceType: "GiftCertificate",
		resourceID:   giftCertID,
		moneyFields:  set("amount", "remaining_amount"),
		timestampFields: set(
			"valid_from", "expires_at", "issued_at", "last_used_at",
			"deleted_at",
		),
		fieldOrder: []string{
			"code", "status", "recipient_name", "recipient_email",
			"amount", "remaining_amount", "currency",
			"valid_from", "expires_at", "issued_at", "last_used_at",
			"void_reason", "deleted_at",
		},
		currency: extractStr(env.Diff, "currency"),
	}
	return renderDiff(w, env, opts)
}

// RenderPricingTierDiff renders a PricingTier dry-run.
func RenderPricingTierDiff(w io.Writer, tierID string, env DiffEnvelope) error {
	opts := renderOptions{
		color:           isWriterTTY(w),
		resourceType:    "PricingTier",
		resourceID:      tierID,
		moneyFields:     set("amount"),
		timestampFields: set("deleted_at"),
		fieldOrder: []string{
			"name", "amount", "currency", "product_option_id",
			"availability_id", "deleted_at",
		},
		currency: extractStr(env.Diff, "currency"),
	}
	return renderDiff(w, env, opts)
}

// renderDiff is the shared implementation. All public Render*Diff funcs
// configure opts and dispatch here.
func renderDiff(w io.Writer, env DiffEnvelope, opts renderOptions) error {
	header := buildHeader(opts.resourceType, opts.resourceID, env.WouldApply)
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}

	changes := diffMaps(env.Diff.Before, env.Diff.After)
	if len(changes) == 0 {
		if _, err := fmt.Fprintln(w, "  (no field changes)"); err != nil {
			return err
		}
	} else {
		ordered := orderChanges(changes, opts.fieldOrder)
		pathWidth := longestPath(ordered)
		for _, ch := range ordered {
			line := formatChange(ch, pathWidth, opts)
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}

	if len(env.SideEffects) > 0 {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		label := "Side effects (would run on apply):"
		if !env.WouldApply {
			label = "Side effects:"
		}
		if _, err := fmt.Fprintln(w, label); err != nil {
			return err
		}
		for _, se := range env.SideEffects {
			if _, err := fmt.Fprintf(w, "  - %s %s: %s\n", se.Type, se.Identifier, se.PayloadSummary); err != nil {
				return err
			}
		}
	}

	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if env.WouldApply {
		_, err := fmt.Fprintln(w, "Run without --dry-run to apply.")
		return err
	}
	_, err := fmt.Fprintln(w, "Server reports would_apply=false; this preview will not be applied.")
	return err
}

func buildHeader(resourceType, resourceID string, wouldApply bool) string {
	var b strings.Builder
	b.WriteString("Dry-run preview")
	if resourceType != "" {
		b.WriteString(" for ")
		b.WriteString(resourceType)
	}
	if resourceID != "" {
		b.WriteString(" ")
		b.WriteString(resourceID)
	}
	b.WriteString(" (would_apply: ")
	if wouldApply {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	b.WriteString("):\n\n")
	return b.String()
}

// diffMaps returns the set of FieldChange entries between before and
// after. Keys present in both with deeply-equal values are skipped, as
// are fields that are null in a unilateral create/remove (no point
// telling the user "X was created with value null").
func diffMaps(before, after map[string]interface{}) []FieldChange {
	keys := map[string]struct{}{}
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}
	out := make([]FieldChange, 0, len(keys))
	for k := range keys {
		bv, bok := before[k]
		av, aok := after[k]
		switch {
		case bok && aok:
			if reflect.DeepEqual(bv, av) {
				continue
			}
			out = append(out, FieldChange{Path: k, Before: bv, After: av})
		case bok:
			if bv == nil {
				continue
			}
			out = append(out, FieldChange{Path: k, Before: bv, Removed: true})
		case aok:
			if av == nil {
				continue
			}
			out = append(out, FieldChange{Path: k, After: av, Created: true})
		}
	}
	return out
}

// orderChanges sorts changes by the configured fieldOrder; unlisted
// fields fall to the end in alphabetical order. With no order, the
// caller gets a stable alphabetical sort.
func orderChanges(changes []FieldChange, order []string) []FieldChange {
	if len(order) == 0 {
		sort.Slice(changes, func(i, j int) bool {
			return changes[i].Path < changes[j].Path
		})
		return changes
	}
	rank := make(map[string]int, len(order))
	for i, k := range order {
		rank[k] = i
	}
	sort.SliceStable(changes, func(i, j int) bool {
		ri, oki := rank[changes[i].Path]
		rj, okj := rank[changes[j].Path]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true
		case okj:
			return false
		default:
			return changes[i].Path < changes[j].Path
		}
	})
	return changes
}

func longestPath(changes []FieldChange) int {
	n := 0
	for _, c := range changes {
		if len(c.Path) > n {
			n = len(c.Path)
		}
	}
	return n
}

// formatChange renders a single FieldChange line.
func formatChange(c FieldChange, pathWidth int, opts renderOptions) string {
	pathField := c.Path + ":"
	// pad path to align values across rows
	if pathWidth+1 > len(pathField) {
		pathField = pathField + strings.Repeat(" ", pathWidth+1-len(pathField))
	}
	var pathRendered string
	if opts.color {
		pathRendered = ansiYellow + pathField + ansiReset
	} else {
		pathRendered = pathField
	}

	beforeStr := formatValue(c.Before, c.Path, opts)
	afterStr := formatValue(c.After, c.Path, opts)
	if c.Created {
		beforeStr = "(unset)"
	}
	if c.Removed {
		afterStr = "(unset)"
	}

	var beforeRendered, afterRendered, arrow string
	if opts.color {
		beforeRendered = ansiRed + beforeStr + ansiReset
		afterRendered = ansiGreen + afterStr + ansiReset
		arrow = ansiDim + "→" + ansiReset
	} else {
		beforeRendered = beforeStr
		afterRendered = afterStr
		arrow = "→"
	}

	return fmt.Sprintf("  - %s  %s  %s  %s", pathRendered, beforeRendered, arrow, afterRendered)
}

// formatValue renders one of {Before, After}. Money/timestamp formatting
// is opt-in via opts.
func formatValue(v interface{}, path string, opts renderOptions) string {
	if v == nil {
		return "(unset)"
	}
	if opts.moneyFields[path] {
		if s, ok := formatMoney(v, opts.currency); ok {
			return s
		}
	}
	if opts.timestampFields[path] {
		if s, ok := formatTimestamp(v); ok {
			return s
		}
	}
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		// json.Unmarshal puts all numbers in float64. Render integer
		// values without a trailing decimal.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []interface{}:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = formatValue(x, "", renderOptions{})
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]interface{}:
		// Inline rendering for nested objects: fall back to compact JSON
		// so we never surprise the user with a multi-line dump in the
		// middle of a flat field list.
		b, err := json.Marshal(t)
		if err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatMoney accepts a Money value (integer minor units, per spec) and
// renders it with a leading currency symbol when known. Returns ok=false
// when v is not a numeric type.
func formatMoney(v interface{}, currency string) (string, bool) {
	var minor int64
	switch t := v.(type) {
	case float64:
		if t != float64(int64(t)) {
			// non-integer money — render to 2 decimals as a generic
			// fallback rather than truncating.
			return formatMoneyDecimal(t, currency), true
		}
		minor = int64(t)
	case int:
		minor = int64(t)
	case int64:
		minor = t
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return "", false
		}
		minor = i
	default:
		return "", false
	}
	prefix := currencyPrefix(currency)
	if zeroDecimal(currency) {
		return fmt.Sprintf("%s%d", prefix, minor), true
	}
	// 2-decimal currency
	neg := minor < 0
	if neg {
		minor = -minor
	}
	major := minor / 100
	fract := minor % 100
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%s%d.%02d", sign, prefix, major, fract), true
}

func formatMoneyDecimal(f float64, currency string) string {
	prefix := currencyPrefix(currency)
	return fmt.Sprintf("%s%.2f", prefix, f/100)
}

// currencyPrefix returns a short display prefix for a known ISO 4217
// currency code; an unknown code yields "<CODE> " and an empty code
// yields the empty string.
func currencyPrefix(code string) string {
	switch strings.ToUpper(code) {
	case "EUR":
		return "€ "
	case "USD":
		return "$ "
	case "GBP":
		return "£ "
	case "JPY":
		return "¥ "
	case "":
		return ""
	default:
		return strings.ToUpper(code) + " "
	}
}

// zeroDecimal lists Stripe-style zero-decimal currencies. The CLI v1 spec
// notes "whole units for JPY/HUF/etc"; we cover those plus a few others
// the upstream Stripe API treats the same way.
func zeroDecimal(code string) bool {
	switch strings.ToUpper(code) {
	case "JPY", "HUF", "KRW", "VND", "CLP", "ISK":
		return true
	default:
		return false
	}
}

// formatTimestamp renders an RFC3339 string as
// "YYYY-MM-DD HH:MM UTC". Returns ok=false on parse failure so the caller
// falls back to %v.
func formatTimestamp(v interface{}) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", false
	}
	return t.UTC().Format("2006-01-02 15:04 UTC"), true
}

// extractID looks for a likely identifier in env.Diff.After (preferred)
// or Before.
func extractID(d DiffPayload) string {
	for _, src := range []map[string]interface{}{d.After, d.Before} {
		if src == nil {
			continue
		}
		if v, ok := src["id"]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// extractStr returns the string value at key from After or Before, "" if
// missing or non-string.
func extractStr(d DiffPayload, key string) string {
	for _, src := range []map[string]interface{}{d.After, d.Before} {
		if src == nil {
			continue
		}
		if v, ok := src[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// isWriterTTY returns true when w is an *os.File pointing at a terminal.
// Other writer types (bytes.Buffer, pipes, …) always yield false.
func isWriterTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
