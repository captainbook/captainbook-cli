package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestDryRunBodyEditor_AttachesBodyAndHeaders verifies that the editor used
// by `availabilities delete <id> --dry-run` actually attaches the body to
// the gen-built DELETE request. The OpenAPI spec doesn't declare a request
// body on the DELETE, so codegen produces no *WithBody variant — without
// this editor, dry-run would silently send an empty DELETE and the server
// would treat it as a real soft-delete.
//
// The editor MUST also set GetBody so the transport's retry layer (D25)
// can replay the body on a 5xx; otherwise a single 5xx would corrupt the
// retried request.
func TestDryRunBodyEditor_AttachesBodyAndHeaders(t *testing.T) {
	body := []byte(`{"dry_run":true}`)
	editor := dryRunBodyEditor(body)

	req, err := http.NewRequest("DELETE", "https://example.test/availabilities/av_1", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := editor(context.Background(), req); err != nil {
		t.Fatalf("editor returned error: %v", err)
	}

	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", got)
	}
	if req.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d; want %d", req.ContentLength, len(body))
	}

	// Body is consumed once; assert contents.
	read, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(read) != string(body) {
		t.Errorf("body = %q; want %q", read, body)
	}

	// GetBody MUST return a fresh, readable copy so retries replay
	// correctly. Without this the transport's retryRT corrupts retries.
	if req.GetBody == nil {
		t.Fatal("GetBody is nil; retries would send empty body on replay")
	}
	rc, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody returned error: %v", err)
	}
	replay, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read replayed body: %v", err)
	}
	if string(replay) != string(body) {
		t.Errorf("replayed body = %q; want %q", replay, body)
	}
}

// TestParseFaresFlag covers the one input class the wire cannot recover
// from: a fare entry whose `amount` key is absent. The spec makes `amount`
// required-but-nullable precisely so that case is a 422 rather than a
// silent override wipe, and both write paths (`availabilities update
// --fares`, `bulk-update pricing --fares`) share this parser, so a gap here
// is a gap in both.
func TestParseFaresFlag(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr string // substring; "" means the input must parse
		wantLen int
	}{
		{
			name:    "amount present and non-null writes an override",
			in:      `[{"pricing_tier_id":"pt_7","amount":4500}]`,
			wantLen: 1,
		},
		{
			// The documented way to un-pin a slot. Must survive the
			// round-trip as an explicit null, not be dropped: a body
			// missing `amount` means something else entirely.
			name:    "explicit null amount is legal and means delete the override",
			in:      `[{"pricing_tier_id":"pt_7","amount":null}]`,
			wantLen: 1,
		},
		{
			name:    "multiple tiers in one call",
			in:      `[{"pricing_tier_id":"pt_7","amount":4500},{"pricing_tier_id":"pt_8","amount":null}]`,
			wantLen: 2,
		},
		{
			name:    "missing amount key is rejected before it reaches the wire",
			in:      `[{"pricing_tier_id":"pt_7"}]`,
			wantErr: `missing "amount" key`,
		},
		{
			// Server-side this queued a job per chunk and fired
			// PriceScheduleUpdated at the channel managers for zero data
			// change. It is a 422 now; we never send it.
			name:    "empty array is rejected",
			in:      `[]`,
			wantErr: "empty array",
		},
		{
			name:    "missing pricing_tier_id is rejected",
			in:      `[{"amount":4500}]`,
			wantErr: `"pricing_tier_id"`,
		},
		{
			name:    "empty pricing_tier_id is rejected",
			in:      `[{"pricing_tier_id":"","amount":4500}]`,
			wantErr: `"pricing_tier_id" is empty`,
		},
		{
			// An unquoted id is the likeliest hand-written mistake, since ids
			// appear bare in several docs examples. Reporting it as "missing"
			// sends the operator hunting for a key that is visibly present, so
			// the error must name the type problem instead.
			name:    "present but non-string pricing_tier_id names the type, not absence",
			in:      `[{"pricing_tier_id":7,"amount":4500}]`,
			wantErr: `must be a JSON string, got 7`,
		},
		{
			// Verbatim bytes: decoding through map[string]any would route
			// this via float64 and ship ...992.
			name:    "amount above 2^53 reaches the wire unrounded",
			in:      `[{"pricing_tier_id":"pt_7","amount":9007199254740993}]`,
			wantLen: 1,
		},
		{
			name:    "quoted amount is rejected",
			in:      `[{"pricing_tier_id":"pt_7","amount":"4500"}]`,
			wantErr: `"amount" must be an integer in minor units`,
		},
		{
			name:    "object amount is rejected",
			in:      `[{"pricing_tier_id":"pt_7","amount":{"nested":true}}]`,
			wantErr: `"amount" must be an integer in minor units`,
		},
		{
			name:    "fractional amount is rejected (minor units are whole)",
			in:      `[{"pricing_tier_id":"pt_7","amount":45.5}]`,
			wantErr: `whole number of minor units`,
		},
		{
			name:    "whitespace-only pricing_tier_id is rejected",
			in:      `[{"pricing_tier_id":"   ","amount":4500}]`,
			wantErr: `"pricing_tier_id" is empty`,
		},
		{
			// `--fares null` is not the same input as `--fares '[]'` and
			// must not borrow the empty-array message.
			name:    "JSON null names itself rather than reporting an empty array",
			in:      `null`,
			wantErr: "expected a JSON array, got null",
		},
		{
			name:    "null element names itself rather than reporting a missing amount",
			in:      `[null]`,
			wantErr: "expected an object, got null",
		},
		{
			name:    "non-array JSON is rejected",
			in:      `{"pricing_tier_id":"pt_7","amount":4500}`,
			wantErr: "invalid JSON",
		},
		{
			name:    "malformed JSON is rejected",
			in:      `[{"pricing_tier_id":`,
			wantErr: "invalid JSON",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFaresFlag(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseFaresFlag(%s) = %v, nil; want error containing %q", tc.in, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q; want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFaresFlag(%s) returned error: %v", tc.in, err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("got %d fares; want %d", len(got), tc.wantLen)
			}
		})
	}
}

// TestParseFaresFlag_NullAmountSurvivesMarshal asserts the null amount
// reaches the wire as `"amount":null` rather than being dropped. Dropping
// it would turn "delete this override" into "leave pricing untouched" — a
// no-op the operator would read as success while the slot stays pinned to
// a stale price.
func TestParseFaresFlag_NullAmountSurvivesMarshal(t *testing.T) {
	fares, err := parseFaresFlag(`[{"pricing_tier_id":"pt_7","amount":null}]`)
	if err != nil {
		t.Fatalf("parseFaresFlag: %v", err)
	}
	body, err := overlayJSONField([]byte(`{"dry_run":true}`), "fares", fares)
	if err != nil {
		t.Fatalf("overlayJSONField: %v", err)
	}
	if !strings.Contains(string(body), `"amount":null`) {
		t.Errorf("wire body = %s; want it to carry \"amount\":null", body)
	}
	// RawMessage marshals verbatim, so a large amount must survive byte-exact
	// rather than round-tripping through float64.
	big, err := parseFaresFlag(`[{"pricing_tier_id":"pt_7","amount":9007199254740993}]`)
	if err != nil {
		t.Fatalf("parseFaresFlag(big): %v", err)
	}
	bigBody, err := overlayJSONField([]byte(`{}`), "fares", big)
	if err != nil {
		t.Fatalf("overlayJSONField(big): %v", err)
	}
	if !strings.Contains(string(bigBody), "9007199254740993") {
		t.Errorf("wire body = %s; amount was rounded (float64 round-trip)", bigBody)
	}
	if !strings.Contains(string(body), `"dry_run":true`) {
		t.Errorf("wire body = %s; overlay dropped the pre-existing dry_run field", body)
	}
}

// TestAvailabilitiesUpdate_FaresOverlayReachesWire covers the `availabilities
// update` closure end to end — the part TestParseFaresFlag does not see.
// --fares cannot travel through JSONBodyFromArgs's flag→key map (that map
// copies the flag's Go value verbatim, which would put the array on the wire
// as a QUOTED STRING and 422 with a type error), so it is parsed separately
// and overlaid. Three things have to hold at once, and each is silent if it
// drifts:
//
//   - fares lands as a JSON ARRAY, not a string;
//   - the scalar flags the map does handle survive the overlay (an overlay
//     that rebuilt the body from scratch would drop the capacity change,
//     and the operator would read the 200 as both edits having landed —
//     the closure documents them as one transaction);
//   - is_bookable is sent as a real `false`, not omitted. `false` is the
//     whole point of the flag (it closes the slot), so a truthiness guard
//     would make closing a slot a no-op.
func TestAvailabilitiesUpdate_FaresOverlayReachesWire(t *testing.T) {
	def := availabilitiesDefFor(t, "update <id>")

	t.Run("fares ride alongside the scalar field flags", func(t *testing.T) {
		var gotPath string
		var gotBody []byte
		_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"av_42"},"meta":{}}`))
		})

		args := RunArgs{
			PathArgs: []string{"av_42"},
			Flags: map[string]any{
				"capacity":    12,
				"is-bookable": false,
				"fares":       `[{"pricing_tier_id":"pt_7","amount":null}]`,
			},
		}
		res, err := def.Run(context.Background(), runner, args)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if gotPath != "/availabilities/av_42" {
			t.Errorf("path = %q; want /availabilities/av_42", gotPath)
		}

		var body map[string]any
		if err := json.Unmarshal(gotBody, &body); err != nil {
			t.Fatalf("wire body is not JSON (%s): %v", gotBody, err)
		}
		if got, ok := body["capacity"].(float64); !ok || got != 12 {
			t.Errorf("capacity = %v; want 12 — the overlay dropped a field the flag map set (body: %s)", body["capacity"], gotBody)
		}
		if got, ok := body["is_bookable"].(bool); !ok || got != false {
			t.Errorf("is_bookable = %v; want an explicit false (body: %s)", body["is_bookable"], gotBody)
		}
		fares, ok := body["fares"].([]any)
		if !ok {
			t.Fatalf("fares = %T (%v); want a JSON array — a quoted string is a 422 (body: %s)",
				body["fares"], body["fares"], gotBody)
		}
		if len(fares) != 1 {
			t.Fatalf("fares has %d entries; want 1 (body: %s)", len(fares), gotBody)
		}
		entry, ok := fares[0].(map[string]any)
		if !ok {
			t.Fatalf("fares[0] = %T; want an object (body: %s)", fares[0], gotBody)
		}
		if entry["pricing_tier_id"] != "pt_7" {
			t.Errorf("fares[0].pricing_tier_id = %v; want pt_7", entry["pricing_tier_id"])
		}
		// The delete-the-override signal. It has to be a PRESENT key holding
		// null; an absent key means "leave pricing alone" and the slot stays
		// pinned to a stale price while the operator sees a 200.
		if _, present := entry["amount"]; !present {
			t.Errorf("fares[0] has no amount key; null must survive as an explicit null (body: %s)", gotBody)
		} else if entry["amount"] != nil {
			t.Errorf("fares[0].amount = %v; want null", entry["amount"])
		}

		// WireBody is what the audit log records. If it diverged from the
		// bytes sent, the forensic trail for a price change would be fiction.
		if res == nil || string(res.WireBody) != string(gotBody) {
			t.Errorf("RunResult.WireBody = %s; want the bytes actually sent (%s)", res.WireBody, gotBody)
		}
	})

	t.Run("dry_run survives the overlay", func(t *testing.T) {
		var gotBody []byte
		_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"dry_run":true,"diff":{}},"meta":{}}`))
		})

		args := RunArgs{
			DryRun:   true,
			PathArgs: []string{"av_42"},
			Flags:    map[string]any{"fares": `[{"pricing_tier_id":"pt_7","amount":4500}]`},
		}
		if _, err := def.Run(context.Background(), runner, args); err != nil {
			t.Fatalf("Run: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(gotBody, &body); err != nil {
			t.Fatalf("wire body is not JSON (%s): %v", gotBody, err)
		}
		// Losing dry_run turns a preview into a live repricing of the slot.
		if body["dry_run"] != true {
			t.Errorf("dry_run = %v; want true (body: %s)", body["dry_run"], gotBody)
		}
		if _, ok := body["fares"].([]any); !ok {
			t.Errorf("fares = %T; want the array to survive next to dry_run (body: %s)", body["fares"], gotBody)
		}
	})
}

// TestAvailabilitiesUpdate_BadFaresFailBeforeTheNetwork asserts the shared
// validator is wired into this write path and rejects ahead of the request.
// An empty array used to reach the server, which wrote an audit row, queued
// a job per chunk and fired PriceScheduleUpdated at the channel managers for
// zero data change; it is a 422 now, and there is no reason to spend the
// round trip discovering that.
func TestAvailabilitiesUpdate_BadFaresFailBeforeTheNetwork(t *testing.T) {
	def := availabilitiesDefFor(t, "update <id>")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty array", `[]`, "empty array"},
		{"missing amount key", `[{"pricing_tier_id":"pt_7"}]`, `"amount"`},
		{"malformed JSON", `[{`, "invalid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":{},"meta":{}}`))
			})

			_, err := def.Run(context.Background(), runner,
				RunArgs{PathArgs: []string{"av_42"}, Flags: map[string]any{"fares": tc.in}})
			if err == nil {
				t.Fatalf("expected --fares %s to be rejected", tc.in)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q; want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "--fares") {
				t.Errorf("error = %q; want it to name the flag", err)
			}
			if called {
				t.Error("must reject before the PATCH is sent")
			}
		})
	}
}

// TestOverlayJSONField_RejectsNonObjectBody covers the error return of the
// overlay helper. It exists because the helper's whole job is to reopen an
// already-marshalled body: if that body is ever not a JSON object, silently
// swallowing the error would send a body with the structured field MISSING —
// i.e. `availabilities update --fares` would report success having changed
// no price at all.
func TestOverlayJSONField_RejectsNonObjectBody(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"a JSON array is not an object", `[1,2]`},
		{"a bare scalar is not an object", `"nope"`},
		{"empty bytes are not valid JSON", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := overlayJSONField([]byte(tc.body), "fares", []map[string]any{{"pricing_tier_id": "pt_7"}})
			if err == nil {
				t.Fatalf("overlayJSONField(%q) = %s, nil; want an error", tc.body, got)
			}
			if got != nil {
				t.Errorf("returned %s alongside the error; callers must get nothing to send", got)
			}
		})
	}

	// The success case must not disturb keys it isn't overlaying.
	out, err := overlayJSONField([]byte(`{"capacity":12,"dry_run":true}`), "fares", []map[string]any{})
	if err != nil {
		t.Fatalf("overlayJSONField on a valid object: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if m["capacity"] != float64(12) || m["dry_run"] != true {
		t.Errorf("overlay clobbered existing keys: %s", out)
	}
	if _, ok := m["fares"].([]any); !ok {
		t.Errorf("fares = %T; want an array (%s)", m["fares"], out)
	}
}

// TestBulkUpdatePricing_ThroughCobra drives the user-facing flow for the
// other --fares path. It covers two things nothing else does: that the
// shared validator is reachable from the cobra command (not just from the
// def's closure), and that the `long` parameter added to bulkUpdateDef this
// change actually lands on the command's help text — a plumbing bug there
// compiles fine and just silently produces a command with no long help, on
// the one setting whose --dry-run semantics need explaining.
func TestBulkUpdatePricing_ThroughCobra(t *testing.T) {
	called := false
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"data":{"job_id":"job_1"},"meta":{}}`))
	})

	parent := bulkUpdateCmd(runner)

	var pricing *cobra.Command
	for _, c := range parent.Commands() {
		if c.Name() == "pricing" {
			pricing = c
		}
	}
	if pricing == nil {
		t.Fatal("bulk-update has no `pricing` subcommand")
	}
	if pricing.Long == "" {
		t.Error("pricing has no Long help; the `long` argument added to bulkUpdateDef is not wired to CommandDef.Long")
	} else if !strings.Contains(pricing.Long, "422") {
		t.Errorf("pricing Long does not mention the empty-array 422:\n%s", pricing.Long)
	}

	parent.SetArgs([]string{
		"pricing",
		"--from", "2026-09-01", "--to", "2026-09-08",
		"--product-option-id", "88",
		"--fares", "[]",
	})
	parent.SetOut(&bytes.Buffer{})
	parent.SetErr(&bytes.Buffer{})

	err := parent.Execute()
	if err == nil {
		t.Fatal("expected `bulk-update pricing --fares []` to be rejected client-side")
	}
	if !strings.Contains(err.Error(), "empty array") {
		t.Errorf("error = %q; want the empty-array explanation", err)
	}
	if called {
		t.Error("must reject before the POST — the server used to accept this and fan out " +
			"PriceScheduleUpdated to the channel managers for zero data change")
	}
}

// availabilitiesDefFor returns the single availabilities CommandDef with the
// given Use.
func availabilitiesDefFor(t *testing.T, use string) CommandDef {
	t.Helper()
	var found []CommandDef
	for _, d := range availabilitiesDefs() {
		if d.Use == use {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one %q CommandDef, got %d", use, len(found))
	}
	return found[0]
}

// TestBulkUpdatePricing_WireShape pins the body of the OTHER --fares path.
// The two paths spell the same data differently — `update` overlays `fares`
// at the top level, bulk-update nests it under `new_value` next to the
// `setting` discriminator — so sharing parseFaresFlag between them is only
// half the job: the wrapping is what the server dispatches on, and a fares
// array that landed at the top level here would be a 422 that reads like a
// validation problem with the operator's amounts.
func TestBulkUpdatePricing_WireShape(t *testing.T) {
	var gotBody []byte
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"data":{"job_id":"job_1"},"meta":{}}`))
	})

	parent := bulkUpdateCmd(runner)
	parent.SetArgs([]string{
		"pricing",
		"--from", "2026-09-01", "--to", "2026-09-08",
		"--product-option-id", "88",
		"--fares", `[{"pricing_tier_id":"pt_7","amount":null}]`,
		"--dry-run",
	})
	parent.SetOut(&bytes.Buffer{})
	parent.SetErr(&bytes.Buffer{})
	if err := parent.Execute(); err != nil {
		t.Fatalf("bulk-update pricing: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("wire body is not JSON (%s): %v", gotBody, err)
	}
	if body["setting"] != "pricing" {
		t.Errorf("setting = %v; want pricing (body: %s)", body["setting"], gotBody)
	}
	if body["dry_run"] != true {
		t.Errorf("dry_run = %v; want true — this setting's preview is the whole reason for its long help (body: %s)", body["dry_run"], gotBody)
	}
	if _, stray := body["fares"]; stray {
		t.Errorf("fares sits at the top level; it belongs under new_value (body: %s)", gotBody)
	}
	newValue, ok := body["new_value"].(map[string]any)
	if !ok {
		t.Fatalf("new_value = %T; want an object (body: %s)", body["new_value"], gotBody)
	}
	fares, ok := newValue["fares"].([]any)
	if !ok || len(fares) != 1 {
		t.Fatalf("new_value.fares = %v; want a one-entry array (body: %s)", newValue["fares"], gotBody)
	}
	entry, ok := fares[0].(map[string]any)
	if !ok {
		t.Fatalf("new_value.fares[0] = %T; want an object", fares[0])
	}
	// Same null-survival contract as the single-slot path: an absent key
	// means "leave pricing alone" across the whole date range.
	if _, present := entry["amount"]; !present {
		t.Errorf("new_value.fares[0] lost its amount key (body: %s)", gotBody)
	} else if entry["amount"] != nil {
		t.Errorf("new_value.fares[0].amount = %v; want null", entry["amount"])
	}
}
