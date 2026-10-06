package inventory

// Wire-level capture for the fields and parameters added by the cli-v1 1.29.0
// sync.
//
// Why this exists, stated plainly: `spec_coverage_test.go` says so itself —
// "rule 2 proves a flag EXISTS, not that it reaches the wire. A
// declared-but-unwired flag would satisfy it." It names
// TestSpecDrift_FieldMapKeysExistInSpec as the guard, but that test only
// validates keys ALREADY PRESENT in a field map, and JSONBodyFromArgs sends
// exactly the field map's keys. So a flag that is declared and never wired is
// invisible to BOTH directions of the biconditional: --help advertises it, the
// command accepts it, and the value is silently dropped.
//
// That is the same believed-applied-but-absent failure as the statistics
// `time_from` bug, except self-inflicted. With 8 new body fields and 20 new
// query params landing at once, one unwired flag slipping through is the
// expected outcome rather than the unlucky one.
//
// These tests drive the REAL registered tree from Cmd() through the
// testNewRunner seam, so they exercise production wiring — argument parsing,
// flag collection, field maps and body assembly — not a fixture.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// capture runs the real inventory tree against a stub server and returns what
// the server actually received.
//
// `hit` reports whether a request reached the server at all, and it is what the
// assertions key off rather than `err`. These tests are about the REQUEST; the
// stub's canned response will not match every endpoint's declared shape, and a
// response-parse error after a correct request is not the thing under test. A
// refusal, by contrast, must produce hit=false — that is the assertion.
func capture(t *testing.T, args ...string) (q url.Values, body map[string]any, hit bool, err error) {
	q, body, _, hit, err = captureReq(t, args...)
	return q, body, hit, err
}

// capturedReq is what the server saw, beyond the query and body.
type capturedReq struct {
	Method string
	Path   string
	Header http.Header
}

// captureReq is capture() plus the request line and headers.
//
// The method and path matter because every other guard reads the DECLARED
// CommandDef.Verb/Path annotations; none of them watches the request a Run
// closure actually issues, so a closure calling the wrong generated operation
// would pass both drift directions. The headers matter because the
// Idempotency-Key is set by the transport, not by the body, and a mutation whose
// wire key diverges from the key the audit recorded is invisible to everything
// else.
func captureReq(t *testing.T, args ...string) (q url.Values, body map[string]any, req capturedReq, hit bool, err error) {
	t.Helper()

	var gotBody []byte
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		q = r.URL.Query()
		req = capturedReq{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()}
		if r.Body != nil {
			gotBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Lists want an array under data, single resources an object. Either way
		// the request is already captured by the time this is written.
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[],"meta":{"request_id":"r1"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{},"meta":{"request_id":"r1"}}`))
	})

	// sharedRunner is a package singleton and PersistentPreRunE short-circuits
	// once its Client is set, so without this reset every call after the first
	// would keep talking to the first (already closed) stub server.
	prevShared := sharedRunner
	sharedRunner = &Runner{}
	prev := testNewRunner
	testNewRunner = func() (*Runner, error) { return runner, nil }
	t.Cleanup(func() {
		testNewRunner = prev
		sharedRunner = prevShared
	})

	root := Cmd()
	root.SetArgs(args)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err = root.Execute()

	if len(gotBody) > 0 {
		if jerr := json.Unmarshal(gotBody, &body); jerr != nil {
			t.Fatalf("server received a body that is not JSON: %q", gotBody)
		}
	}
	return q, body, req, hit, err
}

// TestWire_NewQueryParamsReachTheServer covers the 20 query parameters exposed
// by this sync. A flag declared but not wired would pass both drift directions
// and fail here.
func TestWire_NewQueryParamsReachTheServer(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want map[string]string
	}{
		{
			name: "bookings list gained seven filters",
			args: []string{"bookings", "list",
				"--since", "2026-01-02T03:04:05Z",
				"--include-trashed",
				"--customer-id", "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
				"--status", "CONFIRMED",
				"--date-field", "confirmed_at",
				"--origin-type", "ota",
				"--partner-id", "7"},
			want: map[string]string{
				"since":           "2026-01-02T03:04:05Z",
				"include_trashed": "true",
				"customer_id":     "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
				"status":          "CONFIRMED",
				"date_field":      "confirmed_at",
				"origin_type":     "ota",
				"partner_id":      "7",
			},
		},
		{
			name: "availabilities list gained product_id and include_trashed",
			args: []string{"availabilities", "list", "--product-id", "prod_9", "--include-trashed"},
			want: map[string]string{"product_id": "prod_9", "include_trashed": "true"},
		},
		{
			name: "gift-certificates list-available gained since",
			args: []string{"gift-certificates", "list-available", "--since", "2026-01-02T03:04:05Z"},
			want: map[string]string{"since": "2026-01-02T03:04:05Z"},
		},
		{
			name: "gift-certificates list-issued gained since and include_trashed",
			args: []string{"gift-certificates", "list-issued", "--since", "2026-01-02T03:04:05Z", "--include-trashed"},
			want: map[string]string{"since": "2026-01-02T03:04:05Z", "include_trashed": "true"},
		},
		{
			name: "product-options list gained since",
			args: []string{"product-options", "list", "--since", "2026-01-02T03:04:05Z"},
			want: map[string]string{"since": "2026-01-02T03:04:05Z"},
		},
		{
			name: "guests list gained customer_id and q",
			args: []string{"guests", "list", "--customer-id", "6ba7b810-9dad-11d1-80b4-00c04fd430c8", "--q", "ana"},
			want: map[string]string{"customer_id": "6ba7b810-9dad-11d1-80b4-00c04fd430c8", "q": "ana"},
		},
		{
			name: "transactions list gained include_trashed",
			args: []string{"transactions", "list", "--include-trashed"},
			want: map[string]string{"include_trashed": "true"},
		},
		{
			name: "pricing-tiers get gained include_trashed",
			args: []string{"pricing-tiers", "get", "pt_1", "--include-trashed"},
			want: map[string]string{"include_trashed": "true"},
		},
		{
			name: "bookings transactions gained since",
			args: []string{"bookings", "transactions", "bk_1", "--since", "2026-01-02T03:04:05Z"},
			want: map[string]string{"since": "2026-01-02T03:04:05Z"},
		},
		{
			name: "partners list exposes its whole filter set",
			args: []string{"partners", "list", "--since", "2026-01-02T03:04:05Z", "--include-trashed",
				"--q", "acme", "--partner-type", "channel", "--limit", "5"},
			want: map[string]string{
				"since": "2026-01-02T03:04:05Z", "include_trashed": "true",
				"q": "acme", "partner_type": "channel", "limit": "5",
			},
		},
		{
			name: "resource-calendar list sends its required and optional params",
			args: []string{"resource-calendar", "list", "--resource-id", "res_1",
				"--from", "2026-01-01", "--to", "2026-01-31",
				"--event-types", "booking", "--product-option-id", "4"},
			want: map[string]string{
				"resource_id": "res_1", "from": "2026-01-01", "to": "2026-01-31",
				"event_types": "booking", "product_option_id": "4",
			},
		},
		{
			name: "segments list sends status and type",
			args: []string{"segments", "list", "--status", "active", "--type", "smart",
				"--since", "2026-01-02T03:04:05Z"},
			want: map[string]string{"status": "active", "type": "smart", "since": "2026-01-02T03:04:05Z"},
		},
		{
			name: "workflows nodes sends kind",
			args: []string{"workflows", "nodes", "--kind", "action"},
			want: map[string]string{"kind": "action"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, _, hit, err := capture(t, tc.args...)
			if !hit {
				t.Fatalf("no request reached the server: %v", err)
			}
			for key, want := range tc.want {
				if got := q.Get(key); got != want {
					t.Errorf("query %s = %q, want %q (full query: %v)", key, got, want, q)
				}
			}
		})
	}
}

// TestWire_NewBodyFieldsReachTheServer covers the 8 request-body fields exposed
// by this sync, including the four nested ones that needed the json flag type.
func TestWire_NewBodyFieldsReachTheServer(t *testing.T) {
	t.Run("availabilities update gained day_count, start_time, end_time", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "update", "av_1",
			"--day-count", "3", "--start-time", "09:30", "--end-time", "17:00")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if got := body["day_count"]; got != float64(3) {
			t.Errorf("day_count = %v (%T), want 3", got, got)
		}
		if got := body["start_time"]; got != "09:30" {
			t.Errorf("start_time = %v, want 09:30", got)
		}
		if got := body["end_time"]; got != "17:00" {
			t.Errorf("end_time = %v, want 17:00", got)
		}
	})

	t.Run("availability rule gained capacity and times", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "create-rule",
			"--product-option-id", "po_1",
			"--start-date", "2026-01-01", "--end-date", "2026-01-31",
			"--weekdays", "1,2",
			"--capacity", "12",
			"--times", `[{"start_time":"11:30","end_time":"14:00"}]`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if got := body["capacity"]; got != float64(12) {
			t.Errorf("capacity = %v, want 12", got)
		}
		// The nested value must arrive as a JSON ARRAY, not a quoted string.
		times, ok := body["times"].([]any)
		if !ok {
			t.Fatalf("times = %#v (%T), want a JSON array — a quoted string here is the bug the json flag type exists to prevent", body["times"], body["times"])
		}
		first, ok := times[0].(map[string]any)
		if !ok || first["start_time"] != "11:30" {
			t.Errorf("times[0] = %#v, want an object with start_time 11:30", times[0])
		}
	})

	t.Run("workflows create gained steps and trigger", func(t *testing.T) {
		_, body, hit, err := capture(t, "workflows", "create", "--name", "W",
			"--trigger", `{"action_type":"booking_confirmed"}`,
			"--steps", `[{"step_type":"action","action_type":"send_notification"}]`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		trig, ok := body["trigger"].(map[string]any)
		if !ok {
			t.Fatalf("trigger = %#v (%T), want a JSON object", body["trigger"], body["trigger"])
		}
		if trig["action_type"] != "booking_confirmed" {
			t.Errorf("trigger.action_type = %v, want booking_confirmed", trig["action_type"])
		}
		steps, ok := body["steps"].([]any)
		if !ok || len(steps) != 1 {
			t.Fatalf("steps = %#v, want a one-element JSON array", body["steps"])
		}
	})

	t.Run("segments create sends the nested condition tree as an object", func(t *testing.T) {
		_, body, hit, err := capture(t, "segments", "create", "--name", "VIPs",
			"--conditions", `{"filters":[{"field":"total_spend","operator":"gte","value":1000}]}`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		cond, ok := body["conditions"].(map[string]any)
		if !ok {
			t.Fatalf("conditions = %#v (%T), want a JSON object", body["conditions"], body["conditions"])
		}
		if _, ok := cond["filters"].([]any); !ok {
			t.Errorf("conditions.filters = %#v, want an array", cond["filters"])
		}
	})

	t.Run("products duplicate sends include as an array", func(t *testing.T) {
		_, body, hit, err := capture(t, "products", "duplicate", "prod_1",
			"--title", "Copy", "--include", "photos,extras")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if got := body["title"]; got != "Copy" {
			t.Errorf("title = %v, want Copy", got)
		}
		inc, ok := body["include"].([]any)
		if !ok || len(inc) != 2 {
			t.Fatalf("include = %#v, want a two-element array", body["include"])
		}
	})

	t.Run("availabilities create sends every required field", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "create",
			"--product-option-id", "po_1", "--date", "2026-03-01",
			"--start-time", "09:00", "--end-time", "12:00", "--capacity", "8")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		for k, want := range map[string]any{
			"product_option_id": "po_1", "date": "2026-03-01",
			"start_time": "09:00", "end_time": "12:00", "capacity": float64(8),
		} {
			if got := body[k]; got != want {
				t.Errorf("%s = %v (%T), want %v", k, got, got, want)
			}
		}
	})
}

// TestWire_BulkUpdateScopes covers both selectors the spec now offers, and the
// refusals between them. The spec is explicit: "Either availability_ids, or
// product_option_id + from + to. Sending both is a 422; sending neither is a
// 422." These gates mirror that rather than spending a round trip on it.
func TestWire_BulkUpdateScopes(t *testing.T) {
	t.Run("id scope sends availability_ids and no range", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-update", "capacity",
			"--availability-ids", "4821,4822", "--operator", "set_to", "--value", "10")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		ids, ok := body["availability_ids"].([]any)
		if !ok || len(ids) != 2 {
			t.Fatalf("availability_ids = %#v, want a two-element array", body["availability_ids"])
		}
		for _, k := range []string{"from", "to", "product_option_id"} {
			if _, present := body[k]; present {
				t.Errorf("id scope must not send %q: %v", k, body)
			}
		}
	})

	t.Run("range scope sends the trio and no ids", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-update", "capacity",
			"--product-option-id", "po_1", "--from", "2026-01-01", "--to", "2026-01-08",
			"--operator", "set_to", "--value", "10")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		for k, want := range map[string]any{
			"product_option_id": "po_1", "from": "2026-01-01", "to": "2026-01-08",
		} {
			if got := body[k]; got != want {
				t.Errorf("%s = %v, want %v", k, got, want)
			}
		}
		if _, present := body["availability_ids"]; present {
			t.Errorf("range scope must not send availability_ids: %v", body)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		cases := []struct {
			name string
			args []string
			want string
		}{
			{
				name: "both scopes",
				args: []string{"availabilities", "bulk-update", "capacity",
					"--availability-ids", "1", "--product-option-id", "po_1",
					"--from", "2026-01-01", "--to", "2026-01-08",
					"--operator", "set_to", "--value", "1"},
				want: "mutually exclusive",
			},
			{
				name: "neither scope",
				args: []string{"availabilities", "bulk-update", "capacity",
					"--operator", "set_to", "--value", "1"},
				want: "one scope is required",
			},
			{
				name: "partial range",
				args: []string{"availabilities", "bulk-update", "capacity",
					"--product-option-id", "po_1", "--operator", "set_to", "--value", "1"},
				want: "needs all of",
			},
			{
				name: "from equals to",
				args: []string{"availabilities", "bulk-update", "capacity",
					"--product-option-id", "po_1", "--from", "2026-01-01", "--to", "2026-01-01",
					"--operator", "set_to", "--value", "1"},
				want: "strictly after",
			},
			// The id-list validation below is the other half of the id scope. It
			// is the largest blast radius in this command tree — one call changes
			// exactly the departures named — so a 0 that slipped into the list
			// must not be handed to the server to interpret, and a list past the
			// job's chunk size must fail here rather than after the server has
			// already started working through it.
			//
			// Value: protects=bulk-update refuses an availability id below 1 and an id list past the 1000 cap before any departure changes; fails_when=either id-list guard is dropped, sending an invalid or oversized scope to the widest-blast-radius mutation; why_new=the scope table only covered choosing BETWEEN the two scopes, never the contents of the id list; seam=none
			{
				name: "zero id inside the list",
				args: []string{"availabilities", "bulk-update", "capacity",
					"--availability-ids", "4821,0", "--operator", "set_to", "--value", "1"},
				want: "not a valid availability id",
			},
			{
				name: "list past the 1000 cap",
				args: []string{"availabilities", "bulk-update", "capacity",
					"--availability-ids", manyIDs(1001), "--operator", "set_to", "--value", "1"},
				want: "exceeds the 1000 cap",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, body, hit, err := capture(t, tc.args...)
				if err == nil {
					t.Fatalf("expected a refusal; command succeeded with body %v", body)
				}
				if hit {
					t.Errorf("refusal still reached the server with body %v", body)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("error = %v, want it to mention %q", err, tc.want)
				}
				if len(body) > 0 {
					t.Errorf("refusal still sent a body: %v", body)
				}
			})
		}
	})
}

// TestWire_JSONFlagRejectsBadInputBeforeTheWire pins the json flag type's
// failure behaviour: bad input must never reach the server, and a large integer
// must survive verbatim rather than being rounded through float64.
func TestWire_JSONFlagRejectsBadInputBeforeTheWire(t *testing.T) {
	bad := []struct{ name, value string }{
		{"malformed", `[{"step_type":`},
		{"empty", ``},
		{"missing file", `@/nonexistent/steps.json`},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, body, hit, err := capture(t, "workflows", "create", "--name", "W", "--steps", tc.value)
			if err == nil {
				t.Fatalf("expected a validation failure for %s input", tc.name)
			}
			if hit {
				t.Errorf("%s input still reached the wire: %v", tc.name, body)
			}
		})
	}

	t.Run("large integers are not rounded through float64", func(t *testing.T) {
		// 2^53 + 1. Decoding into `any` would ship this as ...992.
		const big = "9007199254740993"
		var gotRaw []byte
		_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotRaw, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{},"meta":{}}`))
		})
		// Same singleton reset capture() documents as mandatory: PersistentPreRunE
		// short-circuits on `sharedRunner.Client != nil`, so without this the
		// subtest is order-dependent and leaves the package singleton pointing at
		// a server that is about to close.
		prevShared := sharedRunner
		sharedRunner = &Runner{}
		prev := testNewRunner
		testNewRunner = func() (*Runner, error) { return runner, nil }
		t.Cleanup(func() {
			testNewRunner = prev
			sharedRunner = prevShared
		})

		root := Cmd()
		root.SetArgs([]string{"workflows", "create", "--name", "W",
			"--trigger", `{"action_type":"x","config":{"id":` + big + `}}`})
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		if err := root.Execute(); err != nil {
			t.Fatalf("command failed: %v", err)
		}
		if !strings.Contains(string(gotRaw), big) {
			t.Errorf("request body lost precision: want %s verbatim, got %s", big, gotRaw)
		}
	})
}

// TestWire_JSONFlagSourcesAndPrecedence covers the three paths into a `json`
// flag that the rejection test above cannot see, because it only asserts
// refusals.
//
// Each one is a distinct way the flag could be wrong while still looking right:
// `@file.json` is a second READER (readDataFlag) that could silently send the
// literal string "@steps.json"; `[]` is the value most likely to be mistaken for
// "unset" and dropped, which would turn "clear the steps" into "leave them
// alone"; and `--data` + a flag together is the only place the merge ORDER is
// observable — a json flag written before the --data unmarshal would lose to it
// instead of overriding it.
//
// Value: protects=@file reads, an explicit empty JSON array, and flag-over---data precedence on json flags; fails_when=parseJSONFlag stops resolving @file, an empty array is treated as unset, or JSONBodyFromArgs applies --data after the flag overrides; why_new=TestWire_JSONFlagRejectsBadInputBeforeTheWire only asserts bad input is refused and nothing asserts the accepting paths; seam=testNewRunner
func TestWire_JSONFlagSourcesAndPrecedence(t *testing.T) {
	t.Run("@file.json is read and sent as JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "steps.json")
		const doc = `[{"step_type":"action","action_type":"send_notification"}]`
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}

		_, body, hit, err := capture(t, "workflows", "create", "--name", "W",
			"--steps", "@"+path)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		steps, ok := body["steps"].([]any)
		if !ok {
			t.Fatalf("steps = %#v (%T), want a JSON array — a string here means the @file was sent literally", body["steps"], body["steps"])
		}
		first, ok := steps[0].(map[string]any)
		if !ok || first["action_type"] != "send_notification" {
			t.Errorf("steps[0] = %#v, want the object from the file", steps[0])
		}
	})

	t.Run("an explicit empty array is sent, not dropped", func(t *testing.T) {
		_, body, hit, err := capture(t, "workflows", "create", "--name", "W",
			"--steps", "[]")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		// Present AND empty. A missing key would mean "leave the steps alone",
		// which is the opposite of what `--steps '[]'` asks for.
		raw, ok := body["steps"]
		if !ok {
			t.Fatalf("steps key absent from %#v — an explicit [] was dropped as if unset", body)
		}
		if arr, ok := raw.([]any); !ok || len(arr) != 0 {
			t.Errorf("steps = %#v (%T), want an empty JSON array", raw, raw)
		}
	})

	t.Run("the flag overrides the same key from --data", func(t *testing.T) {
		// --name is passed as a flag because cobra enforces MarkFlagRequired
		// BEFORE the body is assembled, so a key supplied only via --data cannot
		// satisfy a required flag. That is a separate rough edge, recorded in
		// TODOS.md; here it just means the required flags come from flags.
		_, body, hit, err := capture(t, "workflows", "create", "--name", "W",
			"--data", `{"description":"FromData","steps":[{"step_type":"action","action_type":"from_data"}]}`,
			"--steps", `[{"step_type":"action","action_type":"from_flag"}]`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		steps, ok := body["steps"].([]any)
		if !ok || len(steps) != 1 {
			t.Fatalf("steps = %#v, want a one-element array", body["steps"])
		}
		first, _ := steps[0].(map[string]any)
		if first["action_type"] != "from_flag" {
			t.Errorf("steps[0].action_type = %v, want from_flag — the flag must win over --data", first["action_type"])
		}
		// Keys --data supplied and no flag overrode must survive the merge.
		if body["description"] != "FromData" {
			t.Errorf("description = %v, want FromData carried through from --data", body["description"])
		}
	})
}

// TestWire_ZeroAndNegativeIdsAreRefusedBeforeTheWire covers the twelve query
// params this sync retyped from string to int. Each one grew the same guard, and
// the guard is the whole point of the retype: the old `if v := FlagString(...); v
// != ""` shape treated a 0 as "not given", so `--product-id 0` quietly dropped
// the filter and returned EVERY product's rows presented as one product's. The
// spec pins these params to minimum 1, so a 0 or a negative can only be a
// mistake, and a mistake must be refused rather than widened.
//
// Twelve call sites with one copy-pasted guard is exactly the shape where one
// site silently loses the guard, which is why this enumerates all of them rather
// than sampling.
//
// Value: protects=zero/negative values on the retyped integer id query params are refused before any request; fails_when=a `v < 1` guard is dropped or inverted at any of the 12 call sites, silently widening the result set; why_new=wire_capture only asserts positive values reach the wire and nothing anywhere exercises the refusal; seam=none
func TestWire_ZeroAndNegativeIdsAreRefusedBeforeTheWire(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"availabilities list product-option-id", []string{"availabilities", "list", "--product-option-id", "0"}, "--product-option-id must be >= 1"},
		{"bookings list product-option-id", []string{"bookings", "list", "--product-option-id", "0"}, "--product-option-id must be >= 1"},
		{"bookings list partner-id negative", []string{"bookings", "list", "--partner-id=-3"}, "--partner-id must be >= 1"},
		{"bookings list resource-id", []string{"bookings", "list", "--resource-id", "0"}, "--resource-id must be >= 1"},
		{"extras list product-id", []string{"extras", "list", "--product-id", "0"}, "--product-id must be >= 1"},
		{"pricing-categories list product-id", []string{"pricing-categories", "list", "--product-id", "0"}, "--product-id must be >= 1"},
		{"pricing-tiers list product-id", []string{"pricing-tiers", "list", "--product-id", "0"}, "--product-id must be >= 1"},
		{"pricing-tiers list availability-id", []string{"pricing-tiers", "list", "--availability-id", "0"}, "--availability-id must be >= 1"},
		{"pricing-tiers get availability-id", []string{"pricing-tiers", "get", "pt_1", "--availability-id", "0"}, "--availability-id must be >= 1"},
		{"product-options list product-id", []string{"product-options", "list", "--product-id", "0"}, "--product-id must be >= 1"},
		{"products list category", []string{"products", "list", "--category", "0"}, "--category must be >= 1"},
		{"questions list product-option-id", []string{"questions", "list", "--product-option-id", "0"}, "--product-option-id must be >= 1"},
		{"resource-calendar list product-option-id", []string{"resource-calendar", "list", "--resource-id", "res_1", "--product-option-id", "0"}, "--product-option-id must be >= 1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, _, hit, err := capture(t, tc.args...)
			if err == nil {
				t.Fatalf("expected a refusal; command succeeded (query: %v)", q)
			}
			if hit {
				t.Errorf("refusal still reached the server; the unfiltered page would read as a real answer (query: %v)", q)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestWire_BookingsIncludeUnionReachesTheServer pins the one flag the whole
// tools/speccompat hoist rule exists to keep reachable.
//
// 1.29.0 respells `include` as "one value or a list of them", so the generated
// type stopped being a *string and became a union struct whose only field is an
// UNEXPORTED json.RawMessage. Whether `include=resources` still reaches the
// server is therefore a property of the generated form-encoder, not of anything
// visible in bookings.go: if codegen ever emits the union without a usable form
// tag, the flag silently sends nothing or sends a JSON-quoted `"resources"`, and
// the caller gets bookings with no resources attached under a 200.
//
// Neither drift direction can see this — both only ask whether the flag and the
// spec parameter exist.
//
// Value: protects=bookings list --include sends include=resources verbatim through the generated union type; fails_when=a codegen bump or union-plumbing change makes the flag send nothing or a JSON-quoted value; why_new=no test passes --include to bookings list at all, and both drift directions only check that the flag and param exist; seam=none
func TestWire_BookingsIncludeUnionReachesTheServer(t *testing.T) {
	q, _, hit, err := capture(t, "bookings", "list", "--include", "resources")
	if !hit {
		t.Fatalf("no request reached the server: %v", err)
	}
	got := q.Get("include")
	if got != "resources" {
		t.Errorf("include = %q, want %q (full query: %v)", got, "resources", q)
	}
	if strings.Contains(got, `"`) {
		t.Errorf("include = %q is JSON-quoted; the union marshalled itself onto the query string", got)
	}
}

// TestWire_MalformedTypedFlagValuesAreRefused covers the parse-and-refuse gates
// this sync added for the three value types it introduced: UUIDs (--customer-id,
// and segment ids, which are UUIDs rather than the opaque prefixed ids every
// other resource uses), RFC3339 timestamps (--since, on seven endpoints) and
// YYYY-MM-DD dates (resource-calendar's window).
//
// Each gate exists so a typo fails locally, naming the flag, instead of becoming
// a server 404 or 422 that reads as "no such booking". Every one of them is a
// brand-new error branch with no coverage, and the failure mode if one is
// dropped is the quiet kind: `uuid.Parse` error ignored means a zero UUID goes
// out as a filter and matches nothing, which looks exactly like an empty result.
//
// Value: protects=malformed UUID, RFC3339 and date flag values are refused locally, naming the flag, instead of reaching the server; fails_when=any new parse gate drops its error, sending a zero value that reads as an empty result rather than a typo; why_new=no test exercises any of the UUID, --since or window-date parse failures added by this sync; seam=none
func TestWire_MalformedTypedFlagValuesAreRefused(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bookings list customer-id", []string{"bookings", "list", "--customer-id", "not-a-uuid"}, "--customer-id"},
		{"guests list customer-id", []string{"guests", "list", "--customer-id", "not-a-uuid"}, "--customer-id"},
		{"segments get positional id", []string{"segments", "get", "not-a-uuid"}, "is not a UUID"},
		{"segments members positional id", []string{"segments", "members", "not-a-uuid"}, "is not a UUID"},
		{"bookings list since is date-only", []string{"bookings", "list", "--since", "2026-01-02"}, "invalid RFC3339"},
		{"bookings transactions since", []string{"bookings", "transactions", "bk_1", "--since", "yesterday"}, "invalid RFC3339"},
		{"partners list since", []string{"partners", "list", "--since", "yesterday"}, "invalid RFC3339"},
		{"segments list since", []string{"segments", "list", "--since", "yesterday"}, "invalid RFC3339"},
		{"product-options list since", []string{"product-options", "list", "--since", "yesterday"}, "invalid RFC3339"},
		{"gift-certificates list-available since", []string{"gift-certificates", "list-available", "--since", "yesterday"}, "invalid RFC3339"},
		{"gift-certificates list-issued since", []string{"gift-certificates", "list-issued", "--since", "yesterday"}, "invalid RFC3339"},
		{"resource-calendar from", []string{"resource-calendar", "list", "--resource-id", "res_1", "--from", "01-01-2026"}, "--from"},
		{"resource-calendar to", []string{"resource-calendar", "list", "--resource-id", "res_1", "--to", "31/01/2026"}, "--to"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, _, hit, err := capture(t, tc.args...)
			if err == nil {
				t.Fatalf("expected a refusal; command succeeded (query: %v)", q)
			}
			if hit {
				t.Errorf("malformed value still reached the server (query: %v)", q)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// manyIDs builds a comma-separated list of n sequential availability ids, so the
// 1000-cap case above states the boundary instead of hiding a thousand literals
// in the table.
func manyIDs(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = strconv.Itoa(i + 1)
	}
	return strings.Join(ids, ",")
}

// TestWire_MutuallyExclusiveFlagsAreRefused covers the one client-side
// exclusion gate in the inventory tree.
//
// It exists because the gate was DEAD and nothing noticed. `--product-id` and
// `--availability-id` were retyped string to int by this sync, and the gate kept
// reading them with `args.FlagString`, which type-asserts to string and so
// returns "" for an int flag no matter what the caller passed. The condition
// could not be true. Both params went to the wire together and the server
// answered the documented 422 that the gate was written to pre-empt.
//
// Every other guard missed it. The code compiles — FlagString is valid Go on any
// flag name. Both drift directions pass — the flags exist and match the spec.
// The type-drift test passes — the DECLARED type is correct; it is the READER
// that disagrees. And the gate still reads plausibly at the call site. Only
// driving the real command and looking at the wire shows it.
//
// Value: protects=the --product-id/--availability-id exclusion gate actually refuses; fails_when=the gate is read through an accessor whose type does not match the flag's declared type, or is dropped; why_new=nothing in the suite exercised this gate, and a type-mismatched accessor is invisible to the compiler and to both drift directions; seam=testNewRunner
func TestWire_MutuallyExclusiveFlagsAreRefused(t *testing.T) {
	t.Run("both together are refused before the wire", func(t *testing.T) {
		q, _, hit, err := capture(t, "pricing-tiers", "list",
			"--product-id", "7", "--availability-id", "9")
		if err == nil {
			t.Fatal("--product-id with --availability-id was accepted; the server answers 422 for that pair")
		}
		if hit {
			t.Errorf("the refused pair still reached the wire as %v", q)
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error = %v, want it to name the exclusion", err)
		}
	})

	// Either flag ALONE must still work, or a gate that refuses everything would
	// also satisfy the assertion above.
	for _, solo := range []struct{ flag, key, value string }{
		{"--product-id", "product_id", "7"},
		{"--availability-id", "availability_id", "9"},
	} {
		t.Run("alone: "+solo.flag, func(t *testing.T) {
			q, _, hit, err := capture(t, "pricing-tiers", "list", solo.flag, solo.value)
			if !hit {
				t.Fatalf("%s alone was refused: %v", solo.flag, err)
			}
			if got := q.Get(solo.key); got != solo.value {
				t.Errorf("%s = %q, want %q (query: %v)", solo.key, got, solo.value, q)
			}
		})
	}
}

// TestWire_EnumListFlagAcceptsASubset covers the one flag whose value is a
// comma-separated SUBSET of its enum rather than a single member.
//
// The generic enum gate is single-valued by design: for an ordinary enum flag,
// "busy,unavailable" IS an invalid value and refusing it is correct. That made
// `--event-types busy,unavailable` — the spec's own example for this parameter —
// refused locally and never sent, so narrowing to two of the three event
// families was unreachable from the CLI. The flag now opts in via
// FlagDef.EnumList.
//
// Both directions matter here. Accepting the list is the fix; still refusing a
// bogus ELEMENT is what keeps the fix from being a hole, since the reason the
// gate exists is that some endpoints ignore unknown enum values and answer with
// unfiltered data.
//
// Value: protects=EnumList flags accept a comma-separated subset AND still reject an unknown element; fails_when=EnumList is dropped (the list form is refused) or the per-element check is lost (a typo reaches the wire and returns unfiltered data); why_new=no test drove a multi-value enum flag, and the spec-enum drift test passes either way because the token set is unchanged; seam=testNewRunner
func TestWire_EnumListFlagAcceptsASubset(t *testing.T) {
	t.Run("the documented list form reaches the wire", func(t *testing.T) {
		q, _, hit, err := capture(t, "resource-calendar", "list",
			"--resource-id", "res_7", "--event-types", "busy,unavailable")
		if !hit {
			t.Fatalf("the spec's own example value was refused locally: %v", err)
		}
		if got := q.Get("event_types"); got != "busy,unavailable" {
			t.Errorf("event_types = %q, want %q (query: %v)", got, "busy,unavailable", q)
		}
	})

	t.Run("a single value still works", func(t *testing.T) {
		q, _, hit, err := capture(t, "resource-calendar", "list",
			"--resource-id", "res_7", "--event-types", "unavailable")
		if !hit {
			t.Fatalf("single value refused: %v", err)
		}
		if got := q.Get("event_types"); got != "unavailable" {
			t.Errorf("event_types = %q, want unavailable", got)
		}
	})

	for _, bad := range []struct{ name, value string }{
		{"unknown element", "busy,bogus"},
		{"unknown single value", "bogus"},
		{"empty element", "busy,"},
	} {
		t.Run("refused: "+bad.name, func(t *testing.T) {
			q, _, hit, err := capture(t, "resource-calendar", "list",
				"--resource-id", "res_7", "--event-types", bad.value)
			if err == nil {
				t.Fatalf("--event-types %q was accepted", bad.value)
			}
			if hit {
				t.Errorf("--event-types %q reached the wire as %v", bad.value, q)
			}
		})
	}
}

// TestWire_IdempotencyKeyReachesTheWire asserts the key the audit records is the
// key the server receives.
//
// The existing guard, TestSpecDrift_IdempotencyKeyThreaded, walks the AST for a
// `gen.*Params` COMPOSITE LITERAL and checks it sets IdempotencyKey. That cannot
// see a mutation which constructs no Params at all — the guard is blind to
// exactly the absence it exists to catch. `segments create` is that case: POST
// /segments is the only one of the spec's 70 mutations declaring no
// IdempotencyKey parameter, so codegen emits no Params type for it, and the
// closure originally passed nothing. The transport then minted a second key, and
// the audit's idempotency_key named a key the server never saw.
//
// Driving the real tree and reading the request header is the only way to see
// it, which is why capture() now records headers.
//
// Value: protects=the Idempotency-Key on the wire equals the key the audit records, for mutations with and without a generated Params type; fails_when=a mutation stops threading the resolved key and the transport mints its own, silently decorrelating the audit trail from server state; why_new=the AST guard only fires on a gen.*Params literal, so a closure passing no Params is skipped in silence; seam=testNewRunner
func TestWire_IdempotencyKeyReachesTheWire(t *testing.T) {
	const key = "018f5e2c-6c4a-7c5a-9d2c-83a1b1f6e4cd"

	cases := []struct {
		name string
		args []string
	}{
		// No generated Params type exists for this one — the AST guard skips it.
		{"segments create (no gen Params)", []string{"segments", "create",
			"--name", "VIPs",
			"--conditions", `{"filters":[{"field":"total_spend","operator":"gte","value":1000}]}`}},
		// A conventional mutation, as the control.
		{"workflows create (has gen Params)", []string{"workflows", "create", "--name", "W"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "--idempotency-key", key)
			_, _, req, hit, err := captureReq(t, args...)
			if !hit {
				t.Fatalf("no request reached the server: %v", err)
			}
			got := req.Header.Get("Idempotency-Key")
			if got == "" {
				t.Fatal("no Idempotency-Key on the wire")
			}
			if got != key {
				t.Errorf("Idempotency-Key = %q, want %q — the wire key diverges from the key the audit records, "+
					"so an incident responder cannot correlate the audit entry with server-side state", got, key)
			}
		})
	}
}

// TestWire_NewOperationsHitTheRightEndpoint pins the request line for the
// operations this sync added.
//
// Everything else compares the DECLARED CommandDef.Verb/Path annotations against
// the spec. Nothing watched the request a Run closure actually issues, so a
// closure wired to the wrong generated operation — easy with three new resources
// whose method names are similar — would satisfy both drift directions and every
// field-map check.
//
// Value: protects=each new command issues the method and path its CommandDef declares; fails_when=a Run closure calls the wrong generated operation while the annotations still read correctly; why_new=coverage and drift tests both read the annotations, never the issued request; seam=testNewRunner
func TestWire_NewOperationsHitTheRightEndpoint(t *testing.T) {
	const segID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	cases := []struct {
		args       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"partners", "list"}, "GET", "/partners"},
		{[]string{"partners", "get", "42"}, "GET", "/partners/42"},
		{[]string{"segments", "list"}, "GET", "/segments"},
		{[]string{"segments", "get", segID}, "GET", "/segments/" + segID},
		{[]string{"segments", "members", segID}, "GET", "/segments/" + segID + "/members"},
		{[]string{"segments", "fields"}, "GET", "/segment-fields"},
		{[]string{"resource-calendar", "list", "--resource-id", "res_7"}, "GET", "/resource-calendar"},
		{[]string{"workflows", "nodes"}, "GET", "/workflow-nodes"},
	}

	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, _, req, hit, err := captureReq(t, tc.args...)
			if !hit {
				t.Fatalf("no request reached the server: %v", err)
			}
			if req.Method != tc.wantMethod {
				t.Errorf("method = %s, want %s", req.Method, tc.wantMethod)
			}
			if !strings.HasSuffix(req.Path, tc.wantPath) {
				t.Errorf("path = %s, want it to end in %s", req.Path, tc.wantPath)
			}
		})
	}
}

// TestWire_FlagDefMinIsEnforcedForEveryBoundedFlag derives its cases from the
// command tree itself, so a flag declaring Min cannot be added without its bound
// being exercised.
//
// The bound used to be written out by hand at sixteen call sites with the same
// four-line comment, and TestWire_ZeroAndNegativeIdsAreRefusedBeforeTheWire
// enumerated twelve of them one by one for exactly that reason. It is now one
// declared field validated once in makeRunE, so the test can enumerate itself —
// and `--limit`, which had been left on the old `if v != 0` shape at 27 inventory
// sites, is covered for free. Before this, `inventory products list --limit 0`
// silently returned the server's default page while `stats products --limit 0`
// errored: same flag, same spec rule, opposite behaviour.
//
// Value: protects=every FlagDef declaring Min refuses an out-of-bounds value before any request; fails_when=the central Min check in makeRunE is dropped, inverted, or stops being gated on Changed (which would break "unset means unfiltered"); why_new=the hand-written guards were enumerated by hand and --limit had no guard at all; seam=testNewRunner
func TestWire_FlagDefMinIsEnforcedForEveryBoundedFlag(t *testing.T) {
	// One invocable read command per (command path, flag) pair that declares Min.
	type bounded struct {
		path  []string
		flag  string
		min   int
		extra []string // required flags the command refuses to run without
	}
	var all []bounded

	var walk func(c *cobra.Command, trail []string)
	walk = func(c *cobra.Command, trail []string) {
		// Read the declared bounds off the LIVE command via the annotation
		// bindCommands writes, not the AST: a FlagDef built in a loop reads as
		// empty to an AST walker and the entity is skipped in silence.
		if c.Annotations["verb"] == "GET" && len(c.Annotations["intMins"]) > 0 {
			for _, pair := range strings.Split(c.Annotations["intMins"], ",") {
				name, rawMin, ok := strings.Cut(pair, "=")
				if !ok {
					t.Fatalf("malformed intMins annotation on %s: %q", c.CommandPath(), pair)
				}
				m, err := strconv.Atoi(rawMin)
				if err != nil {
					t.Fatalf("malformed intMins bound on %s: %q", c.CommandPath(), pair)
				}
				// Commands taking a positional id are skipped: they cannot be
				// invoked bare, and none of them carries a bounded query flag.
				if c.Use != c.Name() {
					continue
				}
				all = append(all, bounded{
					path:  append([]string{}, trail...),
					flag:  name,
					min:   m,
					extra: requiredFlagArgs(c, name),
				})
			}
		}
		for _, child := range c.Commands() {
			walk(child, append(append([]string{}, trail...), child.Name()))
		}
	}
	root := Cmd()
	for _, child := range root.Commands() {
		walk(child, []string{child.Name()})
	}

	if len(all) == 0 {
		t.Fatal("found no int flags declaring Min — the walker is broken, not the CLI")
	}

	for _, b := range all {
		// Positional args make some commands unreachable without an id; those
		// carry no bounded query flags, so the GET-list filter above suffices.
		name := strings.Join(b.path, " ") + " --" + b.flag
		t.Run(name, func(t *testing.T) {
			for _, bad := range []string{"0", "-1"} {
				args := append(append([]string{}, b.path...), b.extra...)
				args = append(args, "--"+b.flag+"="+bad)
				q, _, hit, err := capture(t, args...)
				if err == nil {
					t.Errorf("--%s=%s was accepted (min is %d)", b.flag, bad, b.min)
					continue
				}
				if !strings.Contains(err.Error(), b.flag) {
					t.Errorf("--%s=%s failed with %v, want the error to name the flag", b.flag, bad, err)
				}
				if hit {
					t.Errorf("--%s=%s reached the wire as %v", b.flag, bad, q)
				}
			}
			// The bound itself must be accepted, or a guard that refused
			// everything would satisfy the assertions above.
			args := append(append([]string{}, b.path...), b.extra...)
			args = append(args, "--"+b.flag+"="+strconv.Itoa(b.min))
			if _, _, hit, err := capture(t, args...); !hit {
				t.Errorf("--%s=%d (the minimum) was refused: %v", b.flag, b.min, err)
			}
		})
	}
	t.Logf("exercised %d bounded flags across the live tree", len(all))
}

// TestWire_UnsetIntFlagStillMeansUnfiltered is the other half of the Min
// contract, and the one that would break quietly.
//
// An unset int flag reads 0, so the bounds check would reject it — every command
// carrying a bounded int flag would refuse to run at all without that flag,
// turning an optional filter into a required one. What prevents that is the
// `if !cmd.Flags().Changed(fd.Name) { continue }` at the top of makeRunE's
// collection loop, and this test pins exactly that line: remove it and
// `extras list` sends `product_id=0` unasked, which is also the original
// silent-widening bug in a new place.
func TestWire_UnsetIntFlagStillMeansUnfiltered(t *testing.T) {
	for _, args := range [][]string{
		{"products", "list"},
		{"extras", "list"},
		{"bookings", "list"},
		{"partners", "list"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			q, _, hit, err := capture(t, args...)
			if !hit {
				t.Fatalf("%v was refused with no flags set: %v — an unset bounded flag must mean unfiltered", args, err)
			}
			for _, key := range []string{"limit", "product_id", "product_option_id", "resource_id", "partner_id"} {
				if q.Has(key) {
					t.Errorf("%v sent %s=%q without being asked to", args, key, q.Get(key))
				}
			}
		})
	}
}

// requiredFlagArgs returns `--flag value` pairs for every flag the command marks
// required, skipping `skip`. Without them cobra refuses to run and the assertion
// under test never executes — which is how `resource-calendar list` (whose
// --resource-id is required) reported a bounds failure that was really a
// missing-flag failure.
//
// Required-ness is read from cobra's own flag annotation rather than a hand list,
// so a newly required flag is handled without editing this.
func requiredFlagArgs(c *cobra.Command, skip string) []string {
	var out []string
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name == skip {
			return
		}
		if f.Annotations[cobra.BashCompOneRequiredFlag] == nil {
			return
		}
		v := "1"
		switch f.Value.Type() {
		case "string":
			v = "res_1"
		case "bool":
			v = "true"
		}
		out = append(out, "--"+f.Name, v)
	})
	return out
}

// TestWire_FlatPlanGateErrorReachesTheUser is the end-to-end proof for the flat
// error shape, driven through the real command tree.
//
// `GET /workflow-nodes` answers 404 with `{"error":"Workflows are not included in
// your subscription plan."}` for a tenant whose plan excludes workflows. Handling
// that shape in ParseError was NOT sufficient and the unit tests for it could not
// tell: oapi-codegen decodes a 404 into gen.NotFound (= ErrorEnvelope, whose
// `error` is an object), the generated method returns (nil, err) when that fails,
// and the closure's `if err != nil { return nil, err }` fires before
// ParseGenResponse is ever reached. The user saw
// `json: cannot unmarshal string into Go struct field ErrorEnvelope.error`.
//
// Only an end-to-end test through the real client sees this, which is why it
// exists alongside the ParseError unit tests rather than instead of them.
//
// Value: protects=a flat {"error":"text"} refusal surfaces the server's sentence through the full client stack; fails_when=the transport normalizer is removed, so generated decoding rejects the body and the user gets a JSON type error instead of the reason; why_new=ParseError tests feed the body in directly and bypass generated decoding entirely; seam=testNewRunner
func TestWire_FlatPlanGateErrorReachesTheUser(t *testing.T) {
	const reason = "Workflows are not included in your subscription plan."

	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"` + reason + `"}`))
	})

	prevShared := sharedRunner
	sharedRunner = &Runner{}
	prev := testNewRunner
	testNewRunner = func() (*Runner, error) { return runner, nil }
	t.Cleanup(func() {
		testNewRunner = prev
		sharedRunner = prevShared
	})

	root := Cmd()
	root.SetArgs([]string{"workflows", "nodes"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.Execute()

	if err == nil {
		t.Fatal("expected the 404 plan-gate refusal to surface as an error")
	}
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("err = %q\nwant it to carry the server's sentence %q.\n"+
			"A JSON type error here means generated decoding rejected the flat body "+
			"before any of this CLI's error handling ran.", err.Error(), reason)
	}
	if strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("err = %q — the raw decoding failure leaked to the user", err.Error())
	}
}
