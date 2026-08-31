package inventory

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// answersDefFor returns the single CommandDef with the given Use, failing
// the test if it isn't there exactly once.
func answersDefFor(t *testing.T, use string) CommandDef {
	t.Helper()
	var found []CommandDef
	for _, d := range answersDefs() {
		if d.Use == use {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one %q CommandDef, got %d", use, len(found))
	}
	return found[0]
}

// TestAnswersList_FiltersReachQueryString pins the whole param-building
// closure of the new `answers list`. TestSpecQueryParamsAreExposedAsFlags
// only asserts the FLAGS exist; nothing asserted their values reach the
// wire, and every failure mode here is silent:
//
//   - A dropped filter on THIS endpoint does not return an error, it
//     returns the unfiltered cross-booking page — passport numbers, dates
//     of birth and medical notes for every booking the token can see. The
//     caller reads it as "the filter matched a lot".
//   - The unset-guards are deliberately not uniform: string filters use
//     `!= ""` while the int and bool filters use FlagSet, because 0 and
//     false are values a user can legitimately type and the `!= 0` idiom
//     would silently turn `--question-id 0` into "every question".
//   - --limit 0 must NOT be sent: the spec pins limit to 1-200, so a zero
//     forwarded from an unset flag is a 422 on a request the user never
//     asked to constrain.
func TestAnswersList_FiltersReachQueryString(t *testing.T) {
	def := answersDefFor(t, "answers list")

	cases := []struct {
		name   string
		flags  map[string]any
		want   map[string]string // query param → exact expected value
		absent []string          // query params that must not appear at all
	}{
		{
			// The bare read. Every optional filter must be omitted, not
			// sent as its Go zero value: `limit=0` is outside the spec's
			// 1-200 range and `include_trashed=false` would pin a default
			// the server owns.
			name:  "no flags sends no filters at all",
			flags: map[string]any{},
			absent: []string{
				"limit", "cursor", "booking_id", "question_id", "granularity",
				"product_option_id", "from", "to", "since", "include_trashed",
			},
		},
		{
			name: "pagination and booking filter",
			flags: map[string]any{
				"limit": 25, "cursor": "cur_9", "booking-id": "bk_42",
			},
			want: map[string]string{"limit": "25", "cursor": "cur_9", "booking_id": "bk_42"},
		},
		{
			// --question-id 0 is a user-typed value, not an absent flag.
			// If the closure used the usual `!= 0` guard the filter would
			// vanish and the response would be every answer to every
			// question — a bulk PII page the caller asked to narrow. Sending
			// it and letting the server reject it is the safe failure.
			name:  "explicit zero question-id still reaches the wire",
			flags: map[string]any{"question-id": 0},
			want:  map[string]string{"question_id": "0"},
		},
		{
			// Same reasoning for the other int filter.
			name:  "explicit zero product-option-id still reaches the wire",
			flags: map[string]any{"product-option-id": 0},
			want:  map[string]string{"product_option_id": "0"},
		},
		{
			name:  "granularity is passed through as the spec token",
			flags: map[string]any{"granularity": "guest"},
			want:  map[string]string{"granularity": "guest"},
		},
		{
			// `--include-trashed=false` is the only way to say "default,
			// explicitly". It must survive as a real false rather than
			// being swallowed by a truthiness check — the questions
			// resource cascade-soft-deletes answers, so the trashed
			// dimension is load-bearing on historical reads.
			name:  "include-trashed false is sent, not swallowed",
			flags: map[string]any{"include-trashed": false},
			want:  map[string]string{"include_trashed": "false"},
		},
		{
			name:  "include-trashed true is sent",
			flags: map[string]any{"include-trashed": true},
			want:  map[string]string{"include_trashed": "true"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery url.Values
			_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":[],"meta":{"request_id":"req_1"}}`))
			})

			if _, err := def.Run(context.Background(), runner, RunArgs{Flags: tc.flags}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			for k, want := range tc.want {
				if got := gotQuery.Get(k); got != want {
					t.Errorf("query %s = %q; want %q (full query: %v)", k, got, want, gotQuery)
				}
			}
			for _, k := range tc.absent {
				if _, ok := gotQuery[k]; ok {
					t.Errorf("query carries %s=%q; an unset flag must not be sent (full query: %v)",
						k, gotQuery.Get(k), gotQuery)
				}
			}
		})
	}
}

// TestAnswersList_TimestampsReachWireAsInstants covers the three time
// filters together, because they are the ones the doc warns are NOT
// interchangeable: --from/--to bound the trip's departure while --since
// bounds updated_at. All three must survive the RFC3339 round-trip as the
// same instant — a dropped timezone here silently shifts a manifest query
// onto the wrong departure.
func TestAnswersList_TimestampsReachWireAsInstants(t *testing.T) {
	def := answersDefFor(t, "answers list")

	var gotQuery url.Values
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	})

	args := RunArgs{Flags: map[string]any{
		"from":  "2026-09-01T00:00:00Z",
		"to":    "2026-09-02T00:00:00Z",
		"since": "2026-08-01T09:30:00+02:00",
	}}
	if _, err := def.Run(context.Background(), runner, args); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for param, want := range map[string]string{
		"from":  "2026-09-01T00:00:00Z",
		"to":    "2026-09-02T00:00:00Z",
		"since": "2026-08-01T09:30:00+02:00",
	} {
		raw := gotQuery.Get(param)
		if raw == "" {
			t.Errorf("%s missing from query (full query: %v)", param, gotQuery)
			continue
		}
		got, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Errorf("%s = %q, which is not RFC3339: %v", param, raw, err)
			continue
		}
		wantT, _ := time.Parse(time.RFC3339, want)
		if !got.Equal(wantT) {
			t.Errorf("%s = %q (%v); want the same instant as %q", param, raw, got, want)
		}
	}
}

// TestAnswersList_RejectsInvalidTimestamps covers the three parse-error
// branches. They are near-identical copy-pasted blocks, so the assertion
// that matters is that each names the flag the USER got wrong: mislabelling
// --to as --from sends the operator to the wrong line of their script.
//
// The bare date is the realistic mistake, not a nonsense string: the doc
// describes --from/--to as bounding a departure DATE, so "2026-09-01" is
// exactly what an operator types first.
func TestAnswersList_RejectsInvalidTimestamps(t *testing.T) {
	def := answersDefFor(t, "answers list")

	cases := []struct {
		name  string
		flags map[string]any
		want  string // substring the error must contain
	}{
		{"date without a time is not RFC3339", map[string]any{"from": "2026-09-01"}, "--from"},
		{"to is reported as to, not from", map[string]any{"to": "2026-09-02"}, "--to"},
		{"since is reported as since", map[string]any{"since": "yesterday"}, "--since"},
		{
			// With two bad values the first one checked wins; the point is
			// that the message still names a flag rather than dumping a
			// bare time-parse error.
			name:  "unix epoch seconds are rejected",
			flags: map[string]any{"since": "1756684800"},
			want:  "--since",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
			})

			_, err := def.Run(context.Background(), runner, RunArgs{Flags: tc.flags})
			if err == nil {
				t.Fatalf("expected an error for %v, got nil", tc.flags)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q; want it to name %s", err, tc.want)
			}
			if !strings.Contains(err.Error(), "RFC3339") {
				t.Errorf("error = %q; want it to say what format is expected", err)
			}
			if called {
				t.Error("a malformed timestamp must fail before the request is sent")
			}
		})
	}
}

// TestAnswersGet_UsesScopedPath pins the single-answer read. An id outside
// the caller's business unit answers 404 rather than 403 so the id space
// stays non-enumerable, which means the 404 has to surface as an error the
// caller sees — swallowing it would read as "this answer is empty".
func TestAnswersGet_UsesScopedPath(t *testing.T) {
	def := answersDefFor(t, "answers get <id>")

	t.Run("hits /answers/{id}", func(t *testing.T) {
		var gotPath string
		_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"ans_1","answer":"no shellfish"},"meta":{}}`))
		})

		res, err := def.Run(context.Background(), runner, RunArgs{PathArgs: []string{"ans_1"}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if gotPath != "/answers/ans_1" {
			t.Errorf("path = %q; want /answers/ans_1", gotPath)
		}
		if res == nil || res.Status != http.StatusOK {
			t.Fatalf("result = %+v; want a 200", res)
		}
	})

	t.Run("404 surfaces as an error", func(t *testing.T) {
		_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"No such answer"}}`))
		})

		_, err := def.Run(context.Background(), runner, RunArgs{PathArgs: []string{"ans_missing"}})
		if err == nil {
			t.Fatal("a 404 must be an error; silently returning nothing reads as an empty answer")
		}
	})

	t.Run("missing positional fails before the network", func(t *testing.T) {
		called := false
		_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})

		if _, err := def.Run(context.Background(), runner, RunArgs{}); err == nil {
			t.Fatal("expected errMissingPathArg with no positional id")
		}
		if called {
			// Without the guard the gen client builds "/answers/" — which
			// is `answers list` under another name, i.e. a PII page.
			t.Error("must not send a request with an empty id segment")
		}
	})
}

// TestAnswersResource_IsRegisteredReadOnly asserts the resource is actually
// wired into `inventory` (a defs slice nobody calls is invisible to every
// other test in this package, all of which walk either Cmd() or the AST)
// and that it exposes reads only. Answers are written by the customer at
// checkout; a create/update/delete appearing here would mean the CLI is
// offering to forge a customer's declared allergy or passport number.
func TestAnswersResource_IsRegisteredReadOnly(t *testing.T) {
	var answers *cobra.Command
	for _, c := range Cmd().Commands() {
		if c.Name() == "answers" {
			answers = c
			break
		}
	}
	if answers == nil {
		t.Fatal("Cmd() does not register an `answers` resource; answersDefs() is dead code")
	}

	wantPaths := map[string]string{"list": "/answers", "get": "/answers/{id}"}
	seen := map[string]bool{}
	for _, sub := range answers.Commands() {
		seen[sub.Name()] = true
		wantPath, ok := wantPaths[sub.Name()]
		if !ok {
			t.Errorf("answers has a %q subcommand; this surface is read-only "+
				"(answers are written by the customer at checkout, not by operators)", sub.Name())
			continue
		}
		if sub.Annotations["ability"] != "cli:read" {
			t.Errorf("answers %s: ability annotation = %q; want cli:read",
				sub.Name(), sub.Annotations["ability"])
		}
		if _, isMutation := sub.Annotations["dryRun"]; isMutation {
			t.Errorf("answers %s: carries a dryRun annotation, so it was bound as a mutation", sub.Name())
		}
		if sub.Annotations["verb"] != "GET" || sub.Annotations["path"] != wantPath {
			t.Errorf("answers %s: annotated %s %s; want GET %s",
				sub.Name(), sub.Annotations["verb"], sub.Annotations["path"], wantPath)
		}
	}
	for name := range wantPaths {
		if !seen[name] {
			t.Errorf("missing `answers %s` subcommand", name)
		}
	}
}

// TestAnswersList_RejectsUnknownGranularity drives the flag through cobra to
// cover the client-side enum gate. It matters here more than elsewhere:
// the server ignores an unknown `granularity` and returns the UNFILTERED
// set, so a typo would hand back every booking-level answer as though it
// were the per-guest slice the operator asked for.
func TestAnswersList_RejectsUnknownGranularity(t *testing.T) {
	called := false
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	})

	parent := makeResourceParent("answers", "Read answers", answersDefs(), runner)
	parent.SetArgs([]string{"list", "--granularity", "guests"}) // plural typo
	parent.SetOut(&bytes.Buffer{})
	parent.SetErr(&bytes.Buffer{})

	err := parent.Execute()
	if err == nil {
		t.Fatal("expected --granularity guests to be rejected client-side")
	}
	if !strings.Contains(err.Error(), "granularity") {
		t.Errorf("error = %q; want it to name --granularity", err)
	}
	if !strings.Contains(err.Error(), "guest") {
		t.Errorf("error = %q; want it to list the allowed tokens", err)
	}
	if called {
		t.Error("must reject before the network — the server ignores a bad granularity and returns everything")
	}
}
