package inventory

// Wire-level capture for the fields and parameters added by the cli-v1 1.34.0
// sync — spec 1.29.0 → 1.34.0.
//
// Same reason as wire_capture_test.go, which states it plainly: both drift
// directions prove a flag EXISTS, never that its value reaches the wire, so a
// declared-but-unwired flag is invisible to the whole suite while --help
// advertises it. Four surfaces landed here:
//
//   - 1.30.0 `companions` on pricing-categories create/update
//   - 1.31.0 the availability-rule dry run's collision `notes` (response-only;
//     nothing to wire, so nothing here)
//   - 1.32.0 `availability_ids` on availabilities bulk-delete
//   - 1.34.0 `include_trashed` on extras get
//
// The bulk-delete scope tests deliberately mirror TestWire_BulkUpdateScopes
// case for case. The two endpoints now share resolveAvailabilityScope, and a
// shared gate needs its contract pinned at BOTH call sites: an ordering rule
// that belongs to one of them leaking into the other is exactly the regression
// the extraction could introduce, and it would show up as a CLI-only refusal
// of a request the server accepts.

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestWire_BulkDeleteScopes covers both selectors bulk-delete offers as of
// spec 1.32.0, and the refusals between them. Before 1.32.0 the range was the
// endpoint's only shape, and the gap had a measured cost: an operator with
// 1,137 duplicate departures across six options had no call that could express
// the cleanup, because old and new departures sit on the same option inside
// the same dates and no range separates them.
func TestWire_BulkDeleteScopes(t *testing.T) {
	t.Run("id scope sends availability_ids and no range", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-delete",
			"--availability-ids", "4821,4822")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		ids, ok := body["availability_ids"].([]any)
		if !ok || len(ids) != 2 {
			t.Fatalf("availability_ids = %#v, want a two-element array", body["availability_ids"])
		}
		// Sending both scopes is a 422, so the range keys must be absent
		// rather than present-and-empty.
		for _, k := range []string{"from", "to", "product_option_id"} {
			if _, present := body[k]; present {
				t.Errorf("id scope must not send %q: %v", k, body)
			}
		}
	})

	t.Run("range scope sends the trio and no ids", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-delete",
			"--product-option-id", "po_1", "--from", "2026-08-01", "--to", "2026-09-01")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		for k, want := range map[string]any{
			"product_option_id": "po_1", "from": "2026-08-01", "to": "2026-09-01",
		} {
			if got := body[k]; got != want {
				t.Errorf("%s = %v, want %v", k, got, want)
			}
		}
		if _, present := body["availability_ids"]; present {
			t.Errorf("range scope must not send availability_ids: %v", body)
		}
	})

	// An earlier version of this test asserted the OPPOSITE — that bulk-delete
	// accepts `from == to` while bulk-update refuses it — on the reasoning that
	// only the bulk-update SCHEMA documents the rule, so refusing here would be
	// a CLI-only refusal of a request the server accepts. That reasoning was
	// wrong, and the spec says so in its changelog rather than its schema
	// (cli-v1.yaml, the 1.22.0 entry):
	//
	//	POST /availabilities/bulk-delete also refuses `from` equal to `to` with
	//	a 422, matching /availabilities/bulk-update. `to` is EXCLUSIVE on both,
	//	so that range matched nothing and the delete answered 200 with
	//	total_deleted: 0 — success, having removed nothing.
	//
	// So the old test pinned a contract violation as intended behaviour: the
	// CLI spent a destructive round trip and an audit row discovering a 422 it
	// could have named locally. Both endpoints now go through
	// parseAvailabilityRange and refuse it before the request.
	//
	// Value: protects=an empty or reversed half-open range is refused locally
	// on BOTH bulk endpoints; fails_when=the shared range parser stops
	// checking the ordering, or one endpoint goes back to parsing its own
	// dates; why_new=the previous spelling asserted the inverse;
	// seam=none; no_claim=the server's exact 422 body (nothing is sent)
	t.Run("from equals to is refused, as on bulk-update", func(t *testing.T) {
		for _, tc := range []struct{ name, from, to string }{
			{"equal dates", "2026-08-01", "2026-08-01"},
			{"reversed dates", "2026-09-01", "2026-08-01"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, body, hit, err := capture(t, "availabilities", "bulk-delete",
					"--product-option-id", "po_1", "--from", tc.from, "--to", tc.to)
				if err == nil {
					t.Fatalf("expected a refusal; command succeeded with body %v", body)
				}
				if hit {
					t.Errorf("the refusal still reached the server with body %v", body)
				}
				if !strings.Contains(err.Error(), "strictly after") {
					t.Errorf("error = %v, want it to name the half-open range", err)
				}
			})
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
				args: []string{"availabilities", "bulk-delete",
					"--availability-ids", "1", "--product-option-id", "po_1",
					"--from", "2026-08-01", "--to", "2026-09-01"},
				want: "mutually exclusive",
			},
			{
				name: "neither scope",
				args: []string{"availabilities", "bulk-delete"},
				want: "one scope is required",
			},
			{
				name: "partial range",
				args: []string{"availabilities", "bulk-delete", "--product-option-id", "po_1"},
				want: "needs all of",
			},
			{
				name: "zero id inside the list",
				args: []string{"availabilities", "bulk-delete", "--availability-ids", "4821,0"},
				want: "not a valid availability id",
			},
			{
				name: "list past the 1000 cap",
				args: []string{"availabilities", "bulk-delete", "--availability-ids", manyIDs(1001)},
				want: "exceeds the 1000 cap",
			},
			// The range arm's date parse, which 1.32.0 moved from the top of
			// the closure into the else branch. Nothing else in the suite
			// reaches it: the range-scope test above passes dates that are
			// already canonical, so dropping parseDate and sending
			// args.FlagString("from") straight through would keep that test
			// green while "2026-8-1" — a spelling every other tool accepts —
			// reached the server unnormalised. The refusal has to come from
			// here rather than from the server, because this is the one
			// command whose range arm decides how many departures get
			// destroyed.
			//
			// Value: protects=a malformed --from is refused locally instead of
			// being sent as typed; fails_when=the range arm stops parsing the
			// dates and forwards the raw flag strings; why_new=no test in cmd/
			// passes a malformed date to any command, and the canonical-date
			// wire test cannot tell a parse from a pass-through; seam=none;
			// no_claim=which date spellings the API itself accepts (the refusal
			// is local; no request is made)
			// Both arms, because parseAvailabilityRange labels them separately
			// and the function was created by merging two call sites — a
			// swapped label would send the operator to the wrong flag on the
			// widest-blast-radius command in the tree.
			{
				name: "malformed to date",
				args: []string{"availabilities", "bulk-delete",
					"--product-option-id", "po_1", "--from", "2026-08-01", "--to", "2026-9-1"},
				want: "--to:",
			},
			{
				name: "malformed from date",
				args: []string{"availabilities", "bulk-delete",
					"--product-option-id", "po_1", "--from", "2026-8-1", "--to", "2026-09-01"},
				want: "--from:",
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
			})
		}
	})

	// --data is the escape hatch for anything the typed flags do not cover, and
	// it is merged UNDER them everywhere else in this package. On a two-scope
	// endpoint that merge is not enough: a leftover scope key in --data turns
	// an otherwise valid id-scoped call into a 422, because the server reads
	// "both scopes present" and cannot tell which is in force. Typed flags win
	// by DELETING the other scope, not merely by overwriting their own keys.
	t.Run("the typed scope evicts the other scope from --data", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-delete",
			"--availability-ids", "4821",
			"--data", `{"product_option_id":"po_9","from":"2026-08-01","to":"2026-09-01"}`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		for _, k := range []string{"product_option_id", "from", "to"} {
			if _, present := body[k]; present {
				t.Errorf("--data's %q survived alongside the id scope, which is a 422: %v", k, body)
			}
		}
		if ids, ok := body["availability_ids"].([]any); !ok || len(ids) != 1 {
			t.Errorf("availability_ids = %#v, want the flag's single id", body["availability_ids"])
		}
	})

	t.Run("the range scope evicts ids from --data", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-delete",
			"--product-option-id", "po_1", "--from", "2026-08-01", "--to", "2026-09-01",
			"--data", `{"availability_ids":[7,8]}`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if _, present := body["availability_ids"]; present {
			t.Errorf("--data's availability_ids survived alongside the range scope: %v", body)
		}
	})
}

// TestWire_BulkUpdateScopeEvictsTheOtherScopeFromData pins the eviction at the
// OTHER call site of resolveAvailabilityScope.
//
// Three reviewers found the same defect here and a probe confirmed it: before
// this, an id-scoped bulk-update put `availability_ids` on the wire and left
// --data's `product_option_id` / `from` / `to` sitting beside it, which the
// spec makes a 422 on both endpoints. bulk-delete evicted them; bulk-update
// only overwrote its own keys. One endpoint of a shared gate being right is
// how the pair drifts, so the contract is pinned at both sites now.
//
// Value: protects=neither bulk endpoint ships two mutually exclusive scopes
// when --data carries the one the flags did not select; fails_when=the
// eviction is implemented at only one of resolveAvailabilityScope's two call
// sites, or is written as an overwrite rather than a delete; why_new=the
// bulk-delete eviction cases cover the delete path only and
// TestWire_BulkUpdateScopes never combines --data with a scope flag;
// seam=none; no_claim=that the server actually 422s both scopes (the refusal
// is the spec's; the stub answers 200 to any body)
func TestWire_BulkUpdateScopeEvictsTheOtherScopeFromData(t *testing.T) {
	t.Run("the id scope evicts the range from --data", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-update", "capacity",
			"--availability-ids", "4821", "--operator", "set_to", "--value", "10",
			"--data", `{"product_option_id":"po_9","from":"2026-08-01","to":"2026-09-01"}`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		for _, k := range []string{"product_option_id", "from", "to"} {
			if _, present := body[k]; present {
				t.Errorf("--data's %q survived alongside the id scope, which is a 422: %v", k, body)
			}
		}
		if ids, ok := body["availability_ids"].([]any); !ok || len(ids) != 1 {
			t.Errorf("availability_ids = %#v, want the flag's single id", body["availability_ids"])
		}
	})

	t.Run("the range scope evicts ids from --data", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-update", "capacity",
			"--product-option-id", "po_1", "--from", "2026-01-01", "--to", "2026-01-08",
			"--operator", "set_to", "--value", "10",
			"--data", `{"availability_ids":[7,8]}`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if _, present := body["availability_ids"]; present {
			t.Errorf("--data's availability_ids survived alongside the range scope: %v", body)
		}
	})
}

// TestBulkDeleteNoOpWarnings covers the stderr signal for a delete that
// removed nothing.
//
// On a destructive command, 200 + total_deleted: 0 and 200 + total_deleted: 412
// must not read the same. The equal-dates typo is NOT what this covers — both
// bulk endpoints refuse `from >= to` locally via parseAvailabilityRange, since
// the server 422s it too (see the "from equals to is refused" subtest above).
// What reaches here is a well-formed scope that found nothing left to delete.
//
// Value: protects=a committed bulk-delete that matched nothing says so on
// stderr; fails_when=the no-op signal is dropped, or starts firing on a dry
// run (whose zero is expected) and trains operators to ignore it;
// why_new=nothing else inspects a bulk-delete response body; seam=none;
// no_claim=that the server sends these exact shapes (the envelopes here are
// the spec's documented ones, not observed traffic)
func TestBulkDeleteNoOpWarnings(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		dryRun bool
		want   bool
	}{
		{name: "explicit no_op status", body: `{"data":{"status":"no_op","total_deleted":0}}`, want: true},
		{name: "committed delete that matched nothing", body: `{"data":{"status":"deleted","total_deleted":0}}`, want: true},
		{name: "committed delete that removed rows", body: `{"data":{"status":"deleted","total_deleted":412}}`, want: false},
		{name: "dry run previewing zero stays quiet", body: `{"data":{"status":"preview","total_matched":0,"total_deleted":0}}`, want: false},
		{name: "dry run previewing rows", body: `{"data":{"status":"preview","total_matched":9}}`, want: false},
		// The dry-run suppression must not depend on the server labelling the
		// response "preview": `status` is not required on the 200 and the
		// spec's no-op paragraph has no dry-run exception, so a preview that
		// matched nothing may answer `status: "no_op"`. Only the REQUEST knows.
		{name: "dry run reported as no_op stays quiet", body: `{"data":{"status":"no_op","total_deleted":0}}`, dryRun: true, want: false},
		{name: "unparseable body stays quiet", body: `not json`, want: false},
		{name: "absent counters stay quiet", body: `{"data":{}}`, want: false},
		{name: "an error envelope stays quiet", body: `{"error":{"code":"VALIDATION_FAILED"}}`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bulkDeleteNoOpWarnings([]byte(tc.body), tc.dryRun)
			if tc.want {
				if len(got) != 1 {
					t.Fatalf("got %d warnings for %s; a destructive call that removed nothing must emit exactly one", len(got), tc.body)
				}
				// The token is machine-readable and reproduced in
				// skills/availabilities.md, in the same key=value shape as
				// BULK_UPDATE_ACCEPTED, so operator scripts grep for it.
				// Renaming it silently breaks them.
				if !strings.HasPrefix(got[0], "BULK_DELETE_NO_OP ") {
					t.Errorf("warning = %q, want it to start with the BULK_DELETE_NO_OP signal token", got[0])
				}
				return
			}
			if len(got) != 0 {
				t.Errorf("unexpected warning %q for %s", got, tc.body)
			}
		})
	}
}

// TestWire_BulkDeleteNoOpWarningReachesStderr proves the signal is WIRED, not
// merely implemented.
//
// TestBulkDeleteNoOpWarnings calls bulkDeleteNoOpWarnings directly, which says
// nothing about whether the command still invokes it — replacing the call in
// bulkDeleteDef with `_ = bulkDeleteNoOpWarnings` leaves that table green. This
// is the declared-but-unwired class wire_capture_test.go exists for, applied to
// a response-side signal instead of a request field.
//
// Follows TestWire_ResourceCalendarSignalsIncompleteAnswers, the sibling for
// the only other stderr signal in this package: own the stub's response body,
// own both buffers, and assert across every output format, since the renderers
// are what drop fields outside `data`.
//
// Value: protects=a committed bulk-delete that removed nothing emits
// BULK_DELETE_NO_OP on stderr in every format; fails_when=the closure stops
// calling bulkDeleteNoOpWarnings, the Warnings channel stops being rendered,
// or the signal is written to stdout where it would corrupt the data contract;
// why_new=the unit table cannot see the wiring and no other test drives this
// command's response path; seam=testNewRunner; no_claim=that the server sends
// this envelope (the body here is the spec's documented no-op shape)
func TestWire_BulkDeleteNoOpWarningReachesStderr(t *testing.T) {
	run := func(t *testing.T, body string, extra ...string) (stdout, stderr string, err error) {
		t.Helper()
		_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		})
		prevShared := sharedRunner
		sharedRunner = &Runner{}
		prev := testNewRunner
		testNewRunner = func() (*Runner, error) { return runner, nil }
		t.Cleanup(func() {
			testNewRunner = prev
			sharedRunner = prevShared
		})

		outBuf, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
		runner.Out, runner.Err = outBuf, errBuf
		root := Cmd()
		root.SetArgs(append([]string{"availabilities", "bulk-delete",
			"--product-option-id", "po_1", "--from", "2026-08-01", "--to", "2026-09-01"}, extra...))
		root.SetOut(outBuf)
		root.SetErr(errBuf)
		err = root.Execute()
		return outBuf.String(), errBuf.String(), err
	}

	const noOpBody = `{"data":{"status":"no_op","total_deleted":0},"meta":{"request_id":"r1"}}`
	const deletedBody = `{"data":{"status":"deleted","total_deleted":412},"meta":{"request_id":"r1"}}`

	for _, format := range []string{"json", "table", "csv"} {
		t.Run("warns in "+format, func(t *testing.T) {
			stdout, stderr, err := run(t, noOpBody, "--format", format)
			if err != nil {
				t.Fatalf("unexpected error: %v (stderr %q)", err, stderr)
			}
			if !strings.Contains(stderr, "BULK_DELETE_NO_OP") {
				t.Errorf("stderr = %q, want the BULK_DELETE_NO_OP signal", stderr)
			}
			// stdout is the data contract; a signal written there would break
			// any script piping this command into jq.
			if strings.Contains(stdout, "BULK_DELETE_NO_OP") {
				t.Errorf("the signal leaked into stdout, which must stay parseable: %q", stdout)
			}
		})
	}

	t.Run("silent when rows were actually deleted", func(t *testing.T) {
		_, stderr, err := run(t, deletedBody)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(stderr, "BULK_DELETE_NO_OP") {
			t.Errorf("stderr = %q, want no signal when total_deleted is non-zero", stderr)
		}
	})

	t.Run("silent on a dry run that matched nothing", func(t *testing.T) {
		_, stderr, err := run(t, noOpBody, "--dry-run")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(stderr, "BULK_DELETE_NO_OP") {
			t.Errorf("stderr = %q; a preview deletes nothing by design, so warning there "+
				"trains operators to ignore the signal", stderr)
		}
	})
}

// TestWire_CapBoundariesAreAccepted pins the ACCEPTING side of the two numeric
// caps this sync introduced.
//
// The refusal tables cover 1001 ids and min_quantity 1000, and both of those
// are rejected by `>` and `>=` alike — so an off-by-one in either gate refuses
// the documented cap itself and no test notices. A CLI-only refusal of a
// payload the server accepts is the specific failure these caps must not
// produce: the operator read "max 1000" in --help and built exactly that list.
//
// Value: protects=the documented caps themselves reach the wire rather than
// being refused locally; fails_when=either cap comparison is written >=
// instead of >, or a const is edited down; why_new=the refusal rows only pin
// values PAST each cap, which both spellings reject; seam=none;
// no_claim=server acceptance of either payload (the stub answers 200)
func TestWire_CapBoundariesAreAccepted(t *testing.T) {
	t.Run("exactly 1000 availability ids", func(t *testing.T) {
		_, body, hit, err := capture(t, "availabilities", "bulk-delete",
			"--availability-ids", manyIDs(availabilityIDsMaxItems))
		if !hit {
			t.Fatalf("the CLI refused a %d-id list, which is the documented cap: %v",
				availabilityIDsMaxItems, err)
		}
		ids, ok := body["availability_ids"].([]any)
		if !ok || len(ids) != availabilityIDsMaxItems {
			t.Errorf("availability_ids = %d entries, want %d", len(ids), availabilityIDsMaxItems)
		}
	})

	t.Run("min_quantity exactly at the maximum", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "update", "31",
			"--companions", `[{"category_id":30,"min_quantity":999}]`)
		if !hit {
			t.Fatalf("the CLI refused min_quantity %d, which is the spec maximum: %v",
				companionMaxQuantity, err)
		}
		if _, ok := body["companions"].([]any); !ok {
			t.Errorf("companions = %#v, want the array on the wire", body["companions"])
		}
	})
}

// TestWire_CompanionsReachTheWire covers the 1.30.0 "Must travel with" links
// on both write surfaces.
//
// The array cannot travel through JSONBodyFromArgs's flag→key map alone: that
// map copies the flag's Go value, and for a json-typed flag that value is a
// json.RawMessage which marshals verbatim — correct bytes, but entirely
// UNVALIDATED. So the closure parses and overlays instead, and these tests
// pin both halves: the bytes land as a JSON array, and the scalar flags the
// map does handle survive the overlay.
func TestWire_CompanionsReachTheWire(t *testing.T) {
	t.Run("create sends companions as an array alongside the scalars", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "create",
			"--product-id", "44", "--name", "Children", "--type", "CHILD",
			"--companions", `[{"category_id":30,"min_quantity":2}]`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		rows, ok := body["companions"].([]any)
		if !ok {
			t.Fatalf("companions = %#v (%T), want a JSON array — a quoted string would 422 on type",
				body["companions"], body["companions"])
		}
		if len(rows) != 1 {
			t.Fatalf("companions has %d rows, want 1: %v", len(rows), body)
		}
		row, ok := rows[0].(map[string]any)
		if !ok {
			t.Fatalf("companions[0] = %T, want an object", rows[0])
		}
		if row["category_id"] != float64(30) || row["min_quantity"] != float64(2) {
			t.Errorf("companions[0] = %v, want {category_id:30, min_quantity:2}", row)
		}
		// An overlay that rebuilt the body from scratch would drop these, and
		// the operator would read the 200 as both the category and its links
		// having landed.
		if body["name"] != "Children" || body["product_id"] != "44" {
			t.Errorf("the overlay dropped a scalar the field map set: %v", body)
		}
	})

	t.Run("update sends companions", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "update", "31",
			"--companions", `[{"category_id":"30","min_quantity":1}]`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		rows, ok := body["companions"].([]any)
		if !ok || len(rows) != 1 {
			t.Fatalf("companions = %#v, want a one-row array", body["companions"])
		}
		// The quoted spelling is accepted by the spec's PositiveIntegerId and
		// must reach the wire AS SENT. Normalising it to a number here would
		// be the CLI editing a payload the server already accepts.
		row := rows[0].(map[string]any)
		if row["category_id"] != "30" {
			t.Errorf("category_id = %#v, want the string \"30\" the operator typed", row["category_id"])
		}
	})

	// The whole point of the field: `[]` CLEARS every link, while omitting the
	// flag leaves them untouched. A truthiness or len() guard anywhere on this
	// path would collapse the two, and clearing a rule would silently become a
	// no-op — the category stays ungated and the 200 reads like it worked.
	t.Run("an empty array is sent, not dropped", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "update", "31", "--companions", `[]`)
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		rows, present := body["companions"]
		if !present {
			t.Fatalf("companions was dropped from the body; `[]` must reach the server to clear the links: %v", body)
		}
		if arr, ok := rows.([]any); !ok || len(arr) != 0 {
			t.Errorf("companions = %#v, want an empty array", rows)
		}
	})

	t.Run("omitting the flag omits the key", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "update", "31", "--name", "Kids")
		if !hit {
			t.Fatalf("no request reached the server: %v", err)
		}
		if _, present := body["companions"]; present {
			t.Errorf("companions must be absent when the flag is unset, or every unrelated "+
				"update would clear the links: %v", body)
		}
	})

	// The cap's accepting side. The refusal table pins 21 links; on its own
	// that passes whether the gate reads `>` or `>=`, and the `>=` spelling
	// refuses a 20-link payload the server accepts. This file's own header
	// argues that a CLI-only refusal is the worse of the two failures,
	// because nothing on the operator's side explains it — and a 20-link set
	// is the one an operator who has read `max 20` in --help will build.
	//
	// Value: protects=a companions array at exactly the 20-link cap is sent
	// rather than refused; fails_when=the maxItems gate is written `>=` (or
	// companionsMaxItems is edited down) and the CLI refuses a payload the
	// server accepts; why_new=the refusal table only pins 21, which both
	// spellings of the comparison reject; seam=none;
	// no_claim=server acceptance of 20 links (the stub answers 200 to any
	// body; this pins only that the CLI does not refuse it)
	t.Run("exactly the 20-link cap is accepted", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "update", "31",
			"--companions", manyCompanions(companionsMaxItems))
		if !hit {
			t.Fatalf("the CLI refused a %d-link array, which is the documented cap and not past it: %v",
				companionsMaxItems, err)
		}
		rows, ok := body["companions"].([]any)
		if !ok || len(rows) != companionsMaxItems {
			t.Errorf("companions = %#v, want all %d links on the wire", body["companions"], companionsMaxItems)
		}
	})

	// Verbatim bytes, for the reason parseFaresFlag returns RawMessage:
	// decoding through `any` routes every number via float64 and silently
	// rounds anything past 2^53. Asserted on the parser's own output because
	// the capture harness unmarshals the body into map[string]any and would
	// itself do the rounding this test is about.
	t.Run("a large category_id survives byte-exact", func(t *testing.T) {
		rows, err := parseCompanionsValue(json.RawMessage(`[{"category_id":9007199254740993,"min_quantity":1}]`), "", "--companions")
		if err != nil {
			t.Fatalf("parseCompanionsValue: %v", err)
		}
		out, err := overlayJSONField([]byte(`{}`), "companions", rows)
		if err != nil {
			t.Fatalf("overlayJSONField: %v", err)
		}
		if !strings.Contains(string(out), "9007199254740993") {
			t.Errorf("wire body = %s; category_id was rounded by a float64 round-trip", out)
		}
	})
}

// TestWire_CompanionsRefusals pins the client-side gates, each of which mirrors
// a documented server 422.
//
// Catching these locally matters more here than on a typical payload: the write
// REPLACES the whole link set, so a call refused before it is sent leaves the
// existing links intact, while a half-believed one does not. The error text is
// asserted because these are the messages an operator acts on — "unknown key"
// sends them to their own JSON, "already linked" to the duplicate.
func TestWire_CompanionsRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		// On the FLAG path a bare `null` never reaches the validator — the json
		// flag type refuses any non-object, non-array first. That is the branch
		// worth pinning here: `--companions null` on a field where `[]` CLEARS
		// the links is an accident with a destructive reading, and it must not
		// reach the wire for the server to interpret. The --data path has no
		// such pre-filter and is covered in TestWire_CompanionsViaDataIsValidated.
		{
			name: "null instead of an array",
			args: []string{"pricing-categories", "update", "31", "--companions", `null`},
			want: "expected a JSON object or array",
		},
		{
			name: "not an array at all",
			args: []string{"pricing-categories", "update", "31", "--companions", `{"category_id":30,"min_quantity":1}`},
			want: "expected a JSON array of",
		},
		{
			name: "past the 20-link cap",
			args: []string{"pricing-categories", "update", "31", "--companions", manyCompanions(21)},
			want: "exceeds the 20-link cap",
		},
		{
			name: "unknown key",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":30,"min_quantity":1,"label":"adults"}]`},
			want: `unknown key "label"`,
		},
		{
			name: "missing category_id",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"min_quantity":1}]`},
			want: `missing "category_id"`,
		},
		{
			name: "missing min_quantity",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":30}]`},
			want: `missing "min_quantity"`,
		},
		{
			name: "duplicate category",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":30,"min_quantity":1},{"category_id":"30","min_quantity":2}]`},
			want: "already linked at index 0",
		},
		{
			name: "self link on update",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":31,"min_quantity":1}]`},
			want: "the category being updated",
		},
		{
			name: "min_quantity below 1",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":30,"min_quantity":0}]`},
			want: "between 1 and 999",
		},
		{
			name: "min_quantity above 999",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":30,"min_quantity":1000}]`},
			want: "between 1 and 999",
		},
		{
			name: "min_quantity as a string",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":30,"min_quantity":"2"}]`},
			want: "must be an integer between 1 and 999",
		},
		{
			name: "null element",
			args: []string{"pricing-categories", "update", "31", "--companions", `[null]`},
			want: "got null",
		},
		// A scalar element, which is the shape of the likeliest first attempt:
		// the flag takes a list of categories, so passing the ids bare reads
		// as the obvious spelling. It lands on the element decode, one branch
		// above the `[null]` case — decoding a number into
		// map[string]json.RawMessage fails, where `null` succeeds into a nil
		// map. Dropping the decode's error check would route `[30]` into the
		// nil-map branch and answer "got null" about an input containing no
		// null, sending the operator looking for a hole that is not there.
		//
		// Value: protects=a non-object companions element is named for what it
		// is ("got 30") instead of being misreported as a null; fails_when=the
		// element decode stops checking its error and every non-object element
		// collapses into the nil-map branch's "got null"; why_new=the table
		// covered `[null]` (nil map) and a top-level object, never a scalar
		// ELEMENT, which is a different branch; seam=none;
		// no_claim=the server's own wording for this 422 (the message is the
		// CLI's; the request is never sent)
		{
			name: "scalar element instead of an object",
			args: []string{"pricing-categories", "update", "31", "--companions", `[30]`},
			want: "expected an object with category_id and min_quantity, got 30",
		},
		// The empty string is not just another rejected spelling: without the
		// `s == ""` guard in checkPositiveIntegerDigits, `strings.TrimLeft("",
		// digits)` returns "" (so the pattern check passes) and the next
		// operand, s[0], indexes a zero-length string — a panic, verified by
		// deleting the guard: "index out of range [0] with length 0". So the
		// guard is load-bearing for a CRASH, not for a message, and the two
		// conditions are order-dependent inside a single `||`.
		//
		// `""` is the ordinary output of a script that looked up the companion
		// id and found nothing — `jq -r '.id // ""'` — so this arrives already
		// quoted and already valid JSON, which is exactly why the json flag
		// type lets it through to here.
		//
		// Value: protects=a quoted-empty category_id is refused with a message
		// instead of crashing the CLI; fails_when=the empty-string guard is
		// removed or reordered after the pattern check, panicking on s[0];
		// why_new=the id-spelling rows all carry at least one character, so
		// none of them reaches the zero-length path; seam=none;
		// no_claim=that the API also rejects a quoted-empty id (the refusal is
		// local, so nothing is sent)
		{
			name: "empty-string id",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":"","min_quantity":1}]`},
			want: "got an empty string",
		},
		// The PositiveIntegerId spellings, measured against the real validator
		// and recorded in the spec: `5` and `"5"` pass; these are 422s. The
		// json.Number decode is what catches the last two — a numeric decode
		// would turn 5.0 and 1e0 into a cheerful 5 and send it.
		{
			name: "leading-zero id",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":"05","min_quantity":1}]`},
			want: "^[1-9][0-9]*$",
		},
		{
			name: "exponent id",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":1e0,"min_quantity":1}]`},
			want: "^[1-9][0-9]*$",
		},
		{
			name: "decimal id",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":5.0,"min_quantity":1}]`},
			want: "^[1-9][0-9]*$",
		},
		{
			name: "zero id",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":0,"min_quantity":1}]`},
			want: "^[1-9][0-9]*$",
		},
		{
			name: "negative id",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":-1,"min_quantity":1}]`},
			want: "^[1-9][0-9]*$",
		},
		{
			name: "boolean id",
			args: []string{"pricing-categories", "update", "31", "--companions", `[{"category_id":true,"min_quantity":1}]`},
			want: "must be a positive integer id",
		},
		// Every row above drives `update`, and the two closures are COPIES of
		// each other — the create one differs only in passing selfID "". So
		// create's parse-and-REFUSE half was never exercised; only its happy
		// path was. The slip that asymmetry invites is the one a copied block
		// makes: keeping the parse and dropping the error check. Verified by
		// doing it — ignoring the validator's error in create only leaves it
		// sending `"companions": null` (the parser returns nil rows on error)
		// on a field where the server REPLACES the whole set, and every other
		// test in this file still passes, including create's own happy path
		// and all 19 update refusals.
		//
		// One row is enough: the parser is shared, so this only has to show
		// that create routes through it, which update's rows cannot.
		// `category_id: 0` is the deepest gate reachable in one input.
		//
		// Value: protects=create refuses a malformed --companions before the
		// request, not just update; fails_when=create's parse error goes
		// unchecked and a nil/invalid companions value reaches the wire;
		// why_new=all 19 existing refusal rows use `update`, so create's
		// refusal path had zero coverage; seam=none;
		// no_claim=that the API rejects category_id 0 (refused before the
		// request; the stub server never sees it)
		{
			name: "create refuses a bad id too, not just update",
			args: []string{"pricing-categories", "create",
				"--product-id", "44", "--name", "Children", "--type", "CHILD",
				"--companions", `[{"category_id":0,"min_quantity":1}]`},
			want: "^[1-9][0-9]*$",
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
		})
	}
}

// TestWire_CompanionsViaDataIsValidated pins the gates on the OTHER input.
//
// --companions and --data '{"companions":[...]}' assemble the same wire field,
// so a gate that runs only when the flag is set is not a gate. The field's
// semantics are REPLACE, which makes the bypass a silent link wipe: an invalid
// payload the server rejects leaves the links alone, but one it accepts
// replaces them, and nothing local objected.
//
// The last case covers the second half of the same fix. JSONBodyFromArgs
// decodes --data through map[string]any, so a category_id past 2^53 is
// float64-rounded before it reaches the body; reading the raw bytes out of
// --data and overlaying them back keeps the operator's digits.
//
// Value: protects=companions is validated and carried verbatim whichever input
// supplied it; fails_when=the validator is gated on args.FlagSet("companions")
// again, or the value is re-read from the assembled (already-rounded) body;
// why_new=every other companions case drives the flag, so the --data path had
// zero coverage; seam=none; no_claim=that the server would reject these
// payloads (they are refused locally, so nothing is sent)
func TestWire_CompanionsViaDataIsValidated(t *testing.T) {
	refusals := []struct {
		name string
		data string
		want string
	}{
		{
			name: "self link",
			data: `{"companions":[{"category_id":31,"min_quantity":1}]}`,
			want: "the category being updated",
		},
		{
			name: "min_quantity out of range",
			data: `{"companions":[{"category_id":30,"min_quantity":0}]}`,
			want: "between 1 and 999",
		},
		{
			name: "duplicate category",
			data: `{"companions":[{"category_id":30,"min_quantity":1},{"category_id":"30","min_quantity":2}]}`,
			want: "already linked at index 0",
		},
		{
			name: "unknown key",
			data: `{"companions":[{"category_id":30,"min_quantity":1,"label":"adults"}]}`,
			want: `unknown key "label"`,
		},
		{
			name: "past the cap",
			data: `{"companions":` + manyCompanions(companionsMaxItems+1) + `}`,
			want: "exceeds the 20-link cap",
		},
		// The one input shape only --data can produce. `--companions null` is
		// refused by the json flag type before the closure runs, but a null
		// inside --data decodes into a nil slice with NO error, so without an
		// explicit branch it skipped every gate and reached the wire as
		// `companions: null` — out of contract on a non-nullable array, on the
		// field that REPLACES the whole link set.
		{
			name: "json null",
			data: `{"companions":null}`,
			want: "got null",
		},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			_, body, hit, err := capture(t, "pricing-categories", "update", "31", "--data", tc.data)
			if err == nil {
				t.Fatalf("expected a refusal; command succeeded with body %v", body)
			}
			if hit {
				t.Errorf("refusal still reached the server with body %v", body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			// The refusal must name the input the operator actually used.
			if !strings.Contains(err.Error(), "--data") {
				t.Errorf("error = %v, want it to name --data rather than --companions", err)
			}
		})
	}

	t.Run("a valid --data array still reaches the wire", func(t *testing.T) {
		_, body, hit, err := capture(t, "pricing-categories", "update", "31",
			"--data", `{"companions":[{"category_id":30,"min_quantity":2}]}`)
		if !hit {
			t.Fatalf("a valid --data companions array was refused: %v", err)
		}
		rows, ok := body["companions"].([]any)
		if !ok || len(rows) != 1 {
			t.Fatalf("companions = %#v, want a one-row array", body["companions"])
		}
	})

	t.Run("a large category_id from --data is not float64-rounded", func(t *testing.T) {
		raw := json.RawMessage(`{"companions":[{"category_id":9007199254740993,"min_quantity":1}]}`)
		out, err := overlayValidatedCompanions([]byte(`{}`), RunArgs{RawData: raw}, "")
		if err != nil {
			t.Fatalf("overlayValidatedCompanions: %v", err)
		}
		if !strings.Contains(string(out), "9007199254740993") {
			t.Errorf("wire body = %s; category_id was rounded by a float64 round-trip", out)
		}
	})
}

// TestWire_CompanionsSelfLinkOnlyAppliesToUpdate pins the one place the create
// and update gates legitimately differ. On create the category has no id yet,
// so a self-link is not expressible and there is nothing to compare against —
// passing the path id through on create would mean comparing a companion
// against the PRODUCT id (the only id in scope), which would refuse a
// perfectly valid link to category 44 on product 44.
func TestWire_CompanionsSelfLinkOnlyAppliesToUpdate(t *testing.T) {
	_, body, hit, err := capture(t, "pricing-categories", "create",
		"--product-id", "44", "--name", "Children",
		"--companions", `[{"category_id":44,"min_quantity":1}]`)
	if !hit {
		t.Fatalf("create refused a companion whose id matches the product id: %v", err)
	}
	if _, ok := body["companions"].([]any); !ok {
		t.Errorf("companions = %#v, want the array to reach the wire", body["companions"])
	}
}

// TestWire_ExtrasGetIncludeTrashed covers the 1.34.0 parameter.
//
// It is the only way to resolve the catalogue row behind a booking's
// `extras[]` entry once the operator has retired the add-on: the booking keeps
// reporting the row (with deleted_at set, and its money still inside
// total_amount), while `extras get` without the flag 404s on it.
func TestWire_ExtrasGetIncludeTrashed(t *testing.T) {
	q, _, hit, err := capture(t, "extras", "get", "ex_42", "--include-trashed")
	if !hit {
		t.Fatalf("no request reached the server: %v", err)
	}
	if got := q.Get("include_trashed"); got != "true" {
		t.Errorf("include_trashed = %q, want \"true\" (query: %v)", got, q)
	}

	// The unset branch: absent, not `false`. Sending include_trashed=false
	// would be harmless here but is still a parameter the operator never
	// typed, and the same pattern on a filter that DOES narrow is how a
	// believed-applied filter goes missing.
	q2, _, hit2, err2 := capture(t, "extras", "get", "ex_42")
	if !hit2 {
		t.Fatalf("no request reached the server: %v", err2)
	}
	if _, present := q2["include_trashed"]; present {
		t.Errorf("include_trashed must be absent when the flag is unset, got %v", q2)
	}
}

// manyCompanions builds a syntactically valid companions array of n distinct
// links, for the cap check. Uses strconv.Itoa like manyIDs in
// wire_capture_test.go rather than a second spelling of the same conversion.
func manyCompanions(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = `{"category_id":` + strconv.Itoa(i+1) + `,"min_quantity":1}`
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// TestForensicSummary_CapsOversizedValues pins the audit-log bound.
//
// A `json`-typed flag takes @file.json and reads it with no size limit, and
// forensicSummary runs before the closure validates anything — runMutation
// audits a refused call too. So a 60 MB array that the 20-link cap rejects
// still reached audit.jsonl, which rotates at 50 MB keeping 3 files: three
// refused invocations could evict every genuine cancel/refund/void row that
// came before them. The operator would have destroyed their own audit history
// with calls that never left the machine.
//
// Value: protects=no single forensic value can grow the audit row without
// bound, so a refused oversized input cannot evict real history;
// fails_when=the cap is removed or raised past the rotation threshold, or
// forensicSummary records raw flag values again; why_new=nothing bounded the
// forensic payload and the oversized path is reachable through every json
// flag (companions, fares, times, conditions, trigger, steps); seam=none;
// no_claim=that rotation itself behaves correctly (that is audit.go's own
// contract, untouched here)
func TestForensicSummary_CapsOversizedValues(t *testing.T) {
	def := CommandDef{ForensicFields: []string{"companions", "name"}}

	t.Run("a normal value is recorded verbatim", func(t *testing.T) {
		args := RunArgs{Flags: map[string]any{
			"companions": json.RawMessage(`[{"category_id":30,"min_quantity":2}]`),
			"name":       "Children",
		}}
		got := forensicSummary(def, args)
		if _, capped := got["companions"].(map[string]any); capped {
			t.Errorf("a small value was capped: %v", got["companions"])
		}
		if got["name"] != "Children" {
			t.Errorf("name = %v, want it recorded verbatim", got["name"])
		}
	})

	t.Run("an oversized value becomes a bounded digest", func(t *testing.T) {
		args := RunArgs{Flags: map[string]any{
			"companions": json.RawMessage("[" + strings.Repeat(`{"category_id":1,"min_quantity":1},`, 2000) + `{"category_id":2,"min_quantity":1}]`),
		}}
		got := forensicSummary(def, args)
		wrapper, capped := got["companions"].(map[string]any)
		if !capped {
			t.Fatalf("an oversized value was recorded raw; the audit row is unbounded")
		}
		marker, namespaced := wrapper[forensicCapKey].(map[string]any)
		if !namespaced {
			t.Fatalf("the cap marker is not namespaced under %s, so an operator value can forge one: %v",
				forensicCapKey, wrapper)
		}
		for _, k := range []string{"omitted", "bytes", "canonical_json_sha256", "cap_bytes"} {
			if _, ok := marker[k]; !ok {
				t.Errorf("the cap marker is missing %q, so a reader cannot tell what was dropped: %v", k, marker)
			}
		}
		// The whole point is that the recorded row is small.
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if len(raw) > forensicValueMaxBytes {
			t.Errorf("the capped forensic summary is still %d bytes, over the %d cap", len(raw), forensicValueMaxBytes)
		}
	})
}

// TestCapForensicValue_FailsClosedAndResistsForgery covers the two ways the cap
// could stop protecting the audit log.
//
// Value: protects=an unencodable forensic value costs one field instead of the
// whole audit row, and a cap marker cannot be forged by an operator value;
// fails_when=capForensicValue returns the raw value on a marshal error (the
// original spelling, which made json.Marshal of the entry fail so
// FileLogger.Append dropped the row), or the marker stops being namespaced;
// why_new=the size path was covered but neither of these was; seam=none;
// no_claim=that FileLogger.Append itself degrades gracefully (its own contract)
func TestCapForensicValue_FailsClosedAndResistsForgery(t *testing.T) {
	t.Run("an unencodable value still yields a writable row", func(t *testing.T) {
		// pflag's Float64 accepts NaN, and --rating / --discount-pct are both
		// ForensicFields, so this is reachable from the command line.
		got := forensicSummary(CommandDef{ForensicFields: []string{"rating"}},
			RunArgs{Flags: map[string]any{"rating": math.NaN()}})
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("the forensic summary is not encodable (%v), so the whole audit entry is dropped", err)
		}
		wrapper, marked := got["rating"].(map[string]any)
		if !marked {
			t.Fatalf("rating = %#v, want a marker recording that it could not be encoded", got["rating"])
		}
		if _, ok := wrapper[forensicCapKey]; !ok {
			t.Errorf("the unencodable marker is not namespaced: %v", wrapper)
		}
	})

	t.Run("an operator value cannot forge a cap marker", func(t *testing.T) {
		forged := json.RawMessage(`{"omitted":"value exceeds the forensic size cap","bytes":62914560,` +
			`"canonical_json_sha256":"` + strings.Repeat("a", 64) + `","cap_bytes":16384}`)
		got := forensicSummary(CommandDef{ForensicFields: []string{"conditions"}},
			RunArgs{Flags: map[string]any{"conditions": forged}})
		if m, ok := got["conditions"].(map[string]any); ok {
			if _, forgedCap := m[forensicCapKey]; forgedCap {
				t.Errorf("an operator value produced a namespaced cap marker: %v", m)
			}
		}
		// The forged value is small, so it must be recorded verbatim.
		if _, isRaw := got["conditions"].(json.RawMessage); !isRaw {
			t.Errorf("conditions = %#v, want the operator's bytes recorded verbatim", got["conditions"])
		}
	})
}

// TestWire_SelfLinkGateResistsIDAliasing covers the one way the self-link
// refusal could be walked around.
//
// The gate compares a CANONICALISED companion id against the path arg, so
// while the raw arg was used, `update 0007` aliased category 7: every local
// gate passed and a self-link went on the wire, on the field whose semantics
// are REPLACE. selfID is canonicalised before the comparison now.
//
// Value: protects=a non-canonical path id cannot alias the category and slip a
// self-link past the gate; fails_when=selfID is compared raw again, or the
// canonicalisation is dropped; why_new=every self-link case used a canonical
// path id; seam=none; no_claim=the server's own refusal (nothing is sent)
func TestWire_SelfLinkGateResistsIDAliasing(t *testing.T) {
	for _, selfID := range []string{"7", "0007", "+7", " 7"} {
		t.Run("path id "+selfID, func(t *testing.T) {
			_, err := parseCompanionsValue(
				json.RawMessage(`[{"category_id":7,"min_quantity":1}]`), canonicalSelfID(selfID), "--companions")
			if err == nil {
				t.Errorf("a self-link was accepted for path id %q; %q and 7 are the same category", selfID, selfID)
			}
		})
	}

	// A path id that cannot name a category at all disables the check rather
	// than failing the call — the server owns that refusal.
	t.Run("a non-numeric path id disables the self-check", func(t *testing.T) {
		if _, err := parseCompanionsValue(
			json.RawMessage(`[{"category_id":7,"min_quantity":1}]`), canonicalSelfID("abc"), "--companions"); err != nil {
			t.Errorf("a non-numeric path id must not refuse an otherwise valid link: %v", err)
		}
	})
}

// TestForensicCapBoundary pins the cap at its edge, not well past it.
//
// Value: protects=a value exactly at the cap is recorded verbatim and one byte
// past it is a marker; fails_when=the comparison flips between <= and <;
// why_new=the existing case uses ~72 KB against a 16 KB cap, which both
// spellings reject; seam=none; no_claim=none beyond protects
func TestForensicCapBoundary(t *testing.T) {
	def := CommandDef{ForensicFields: []string{"x"}}
	for _, tc := range []struct {
		name      string
		canonical int
		capped    bool
	}{
		{"exactly at the cap is verbatim", forensicValueMaxBytes, false},
		{"one byte past the cap is a marker", forensicValueMaxBytes + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A JSON string of n-2 characters marshals to exactly n bytes.
			in := json.RawMessage(`"` + strings.Repeat("a", tc.canonical-2) + `"`)
			got := forensicSummary(def, RunArgs{Flags: map[string]any{"x": in}})
			wrapper, capped := got["x"].(map[string]any)
			if capped {
				_, capped = wrapper[forensicCapKey]
			}
			if capped != tc.capped {
				t.Fatalf("capped = %v, want %v for a %d-byte canonical value (cap %d)",
					capped, tc.capped, tc.canonical, forensicValueMaxBytes)
			}
			if !tc.capped && !bytes.Equal(got["x"].(json.RawMessage), in) {
				t.Errorf("an uncapped value must be recorded byte-for-byte")
			}
		})
	}
}
