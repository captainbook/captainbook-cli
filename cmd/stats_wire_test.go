package cmd

// Wire-level tests for `ceebee stats`.
//
// These exist because the bug that motivated them returned HTTP 200. The CLI
// sent `time_from`, a query key the statistics API has never accepted; Laravel
// ignores unknown query keys, so the request succeeded, the filter was never
// applied, and the caller received the whole period presented as a filtered
// one. No error, no signal, wrong number.
//
// Asserting "the command exited 0" would have passed against that bug. So these
// tests assert on the REQUEST THE SERVER ACTUALLY RECEIVED, which is the only
// thing that can tell a working filter from a silently dropped one.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/captainbook/captainbook-cli/internal/api"
	"github.com/spf13/cobra"
)

// runStats executes the real command tree against a stub server and returns the
// query the server saw, plus any error the CLI produced.
func runStats(t *testing.T, args ...string) (url.Values, error) {
	t.Helper()

	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	// The inventory and statistics clients share one base, which must already
	// include /api/v1/cli.
	t.Setenv("CEEBEE_API_URL", srv.URL+"/api/v1/cli")
	t.Setenv("CEEBEE_API_TOKEN", "test-token")

	// A FRESH tree per call. rootCmd is a package singleton built in init(), and
	// cobra retains flag values across Execute calls, so reusing it would leak
	// one subtest's flags into the next. statsCmd() builds new subcommands each
	// time, which is what makes per-test isolation possible here.
	root := &cobra.Command{Use: "ceebee", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "")
	root.PersistentFlags().StringVar(&profileName, "profile", "", "")
	root.PersistentFlags().StringVarP(&formatFlag, "format", "f", "json", "")
	root.AddCommand(statsCmd())
	root.SetArgs(args)
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	err := root.Execute()
	return got, err
}

// TestStatsWire_TimeOfDayWindowReachesTheServer is the regression test for the
// original bug. The old flag names sent `time_from`/`time_to`; the server only
// ever accepted `time_of_day_from`/`time_of_day_to`.
func TestStatsWire_TimeOfDayWindowReachesTheServer(t *testing.T) {
	got, err := runStats(t, "stats", "bookings",
		"--from", "2026-01-01", "--to", "2026-01-31",
		"--time-of-day-from", "09:00", "--time-of-day-to", "12:00")
	if err != nil {
		t.Fatalf("stats bookings: unexpected error: %v", err)
	}
	if got.Get("time_of_day_from") != "09:00" {
		t.Errorf("time_of_day_from = %q, want %q (query: %v)", got.Get("time_of_day_from"), "09:00", got)
	}
	if got.Get("time_of_day_to") != "12:00" {
		t.Errorf("time_of_day_to = %q, want %q (query: %v)", got.Get("time_of_day_to"), "12:00", got)
	}
	// The keys the server never accepted must not appear at all.
	for _, dead := range []string{"time_from", "time_to"} {
		if _, present := got[dead]; present {
			t.Errorf("query still carries %q, a key the server ignores: %v", dead, got)
		}
	}
}

// TestStatsWire_OldTimeFlagsAreGone proves the rename is a rename, not an alias.
// A caller still passing the dead name must be told, not silently handed an
// unfiltered result.
func TestStatsWire_OldTimeFlagsAreGone(t *testing.T) {
	for _, dead := range []string{"--time-from", "--time-to"} {
		_, err := runStats(t, "stats", "bookings", dead, "09:00")
		if err == nil {
			t.Errorf("%s was accepted; it must fail as an unknown flag", dead)
			continue
		}
		// Assert WHY it failed. "some error occurred" would stay green if the
		// flags came back and the command happened to fail for an unrelated
		// reason, which is the whole failure this test is supposed to rule out.
		if !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%s failed with %v, want an unknown-flag parse error", dead, err)
		}
	}
}

// TestStatsWire_FiltersReachTheServer covers the filters added with the 1.4.0
// sync, including channel_id, which the spec types as a STRING.
func TestStatsWire_FiltersReachTheServer(t *testing.T) {
	// business-unit-id and product-id are asserted here because this sync moved
	// them OFF their dedicated QueryParams fields and onto params.Extra. The
	// pre-existing business_unit_id test drives QueryParams.BusinessUnitID
	// directly, a path cmd/stats.go no longer uses, so it would stay green while
	// the flag sent nothing at all.
	got, err := runStats(t, "stats", "bookings",
		"--business-unit-id", "3",
		"--product-id", "12",
		"--channel-id", "ch_42",
		"--location-id", "7",
		"--resource-id", "9",
		"--date-basis", "trip",
		"--day-of-week", "sat,sun")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for key, want := range map[string]string{
		"business_unit_id": "3",
		"product_id":       "12",
		"channel_id":       "ch_42",
		"location_id":      "7",
		"resource_id":      "9",
		"date_basis":       "trip",
		"day_of_week":      "sat,sun",
	} {
		if got.Get(key) != want {
			t.Errorf("%s = %q, want %q (query: %v)", key, got.Get(key), want, got)
		}
	}
}

// TestStatsWire_UnsupportedFilterHasNoFlag is the per-metric matrix at the CLI
// surface. The server REFUSES a filter a metric does not implement (400 — these
// routes render validation failures as 400, not 422), so the
// flag must not exist rather than existing and failing.
func TestStatsWire_UnsupportedFilterHasNoFlag(t *testing.T) {
	// Each probe value must be LEGAL for the flag it probes. Probing
	// `--date-basis` with "1" proved nothing: a metric that DOES declare
	// --date-basis also rejects "1", as an invalid enum value, so those rows
	// stayed green whether the flag existed or not. The assertion has to be able
	// to fail for exactly one reason — the flag is absent.
	cases := []struct{ metric, flag, value string }{
		{"occupancy", "--channel-id", "ch_42"}, // counts slots, joins no bookings
		{"occupancy", "--resource-id", "9"},    // an empty slot has nobody assigned
		{"occupancy", "--date-basis", "trip"},  // already dated by the departure
		{"gift-certs", "--product-id", "12"},   // a certificate is an amount, not a trip
		{"browsing", "--product-id", "12"},     // reads the event spine, joins no bookings
		{"summary", "--channel-id", "ch_42"},   // would make one object internally inconsistent
		{"revenue", "--date-basis", "trip"},    // dated by when the money arrived
	}
	for _, tc := range cases {
		_, err := runStats(t, "stats", tc.metric, tc.flag, tc.value)
		if err == nil {
			t.Errorf("stats %s accepted %s; the server refuses that filter with 400, so the flag should not exist",
				tc.metric, tc.flag)
			continue
		}
		if !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("stats %s %s failed with %v, want an unknown-flag parse error — anything else means the flag EXISTS and rejected the value",
				tc.metric, tc.flag, err)
		}
	}
}

// TestStatsWire_ExplicitlyEmptyFilterIsRefused covers the uniform empty-value
// rule. An ABSENT flag means unfiltered, which is correct; a flag the caller set
// to "" is a mistake, and dropping it silently widens the result set while
// presenting it as filtered.
func TestStatsWire_ExplicitlyEmptyFilterIsRefused(t *testing.T) {
	// A free-form string filter, and an enum one that existed before this change.
	for _, flag := range []string{"--channel-id", "--status"} {
		got, err := runStats(t, "stats", "bookings", flag, "")
		if err == nil {
			t.Errorf("stats bookings %s \"\" was accepted; an explicitly empty filter must be refused", flag)
		}
		if len(got) > 0 {
			t.Errorf("stats bookings %s \"\" issued a request (%v); it must fail before the wire", flag, got)
		}
	}
}

// TestStatsWire_InvalidIntFilterIsRefused covers the integer half of the same
// rule. Spec ids are minimum 1, so 0 and negatives can only be mistakes — and
// dropping them silently returned the whole account's numbers presented as one
// product's.
func TestStatsWire_InvalidIntFilterIsRefused(t *testing.T) {
	for _, bad := range []string{"0", "-5"} {
		got, err := runStats(t, "stats", "bookings", "--product-id", bad)
		if err == nil {
			t.Errorf("stats bookings --product-id %s was accepted; it must be refused", bad)
		}
		if len(got) > 0 {
			t.Errorf("stats bookings --product-id %s issued a request (%v); it must fail before the wire", bad, got)
		}
	}
}

// TestStatsWire_AbsentFilterStaysUnfiltered is the other half of the contract
// above: omitting a filter must keep working and must not send the key.
func TestStatsWire_AbsentFilterStaysUnfiltered(t *testing.T) {
	got, err := runStats(t, "stats", "bookings", "--from", "2026-01-01", "--to", "2026-01-31")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, key := range []string{"channel_id", "product_id", "date_basis", "day_of_week"} {
		if _, present := got[key]; present {
			t.Errorf("absent flag still sent %q: %v", key, got)
		}
	}
	if got.Get("from") != "2026-01-01" {
		t.Errorf("from = %q, want 2026-01-01", got.Get("from"))
	}
}

// TestStatsWire_NewEndpointsAreReachable covers the two metrics the CLI was
// missing entirely.
func TestStatsWire_NewEndpointsAreReachable(t *testing.T) {
	for _, metric := range []string{"locations", "browsing"} {
		if _, err := runStats(t, "stats", metric, "--from", "2026-01-01", "--to", "2026-01-31"); err != nil {
			t.Errorf("stats %s: unexpected error: %v", metric, err)
		}
	}
}

// TestStatsWire_InvalidFilterEnumValueIsRefused covers the enum arm of the new
// per-metric filter collection. `date_basis` is the only filter in the
// vocabulary with an Enum, and it is the one whose wrong value is most
// expensive: `--date-basis tripp` silently unfiltered would date the whole
// period by BOOKING when the caller asked for TRIP, returning a different set of
// numbers under the same heading.
//
// The pre-existing validateEnumFlags only walks ep.ExtraFlags, so it cannot see
// a filter enum at all — this arm is a separate code path that nothing else
// reaches, and the only existing coverage passes a VALID value.
//
// Value: protects=stats refuses a value outside a filter's enum before the wire instead of sending or dropping it; fails_when=the FilterDefs enum check in makeRunFunc is removed, so a misspelled --date-basis silently returns booking-dated numbers; why_new=validateEnumFlags only covers ExtraFlags, and the only existing --date-basis coverage passes a valid value; seam=none
func TestStatsWire_InvalidFilterEnumValueIsRefused(t *testing.T) {
	for _, bad := range []string{"tripp", "TRIP", "departure"} {
		got, err := runStats(t, "stats", "bookings", "--date-basis", bad)
		if err == nil {
			t.Errorf("stats bookings --date-basis %q was accepted; only booking|trip are legal", bad)
		}
		if len(got) > 0 {
			t.Errorf("stats bookings --date-basis %q issued a request (%v); it must fail before the wire", bad, got)
		}
		if err != nil && !strings.Contains(err.Error(), "date-basis") {
			t.Errorf("error = %v, want it to name --date-basis", err)
		}
	}

	// The legal values must still pass, or the guard above is just a ban.
	for _, good := range []string{"booking", "trip"} {
		got, err := runStats(t, "stats", "bookings", "--date-basis", good)
		if err != nil {
			t.Errorf("stats bookings --date-basis %q: unexpected error: %v", good, err)
		}
		if got.Get("date_basis") != good {
			t.Errorf("date_basis = %q, want %q", got.Get("date_basis"), good)
		}
	}
}

// TestStatsWire_ExtraIntFlagBounds covers the ExtraFlags integer arm, which had
// no test at all.
//
// It is a SECOND copy of the bounds guard: the per-metric Filters loop has its
// own, and only that one was tested. Two loops, one rule, one of them unguarded
// is exactly the shape where the untested copy quietly loses the check.
//
// The upper bound is new. `--limit` advertised "(1-50)" and refused 0 while
// passing 100 straight through to a server whose spec caps it at 50, so the CLI
// spent a round trip to have its own help text confirmed back as a validation
// failure.
//
// Value: protects=ExtraFlags int values outside the spec's minimum/maximum are refused locally with no request; fails_when=the Min/Max guard in the ExtraFlags arm is dropped, inverted, or diverges from the Filters arm; why_new=the identical guard in the Filters loop is tested and this arm was not, and no upper bound existed; seam=testNewRunner
func TestStatsWire_ExtraIntFlagBounds(t *testing.T) {
	for _, tc := range []struct{ name, metric, flag, value, want string }{
		{"zero", "products", "--limit", "0", "must be >= 1"},
		{"negative", "products", "--limit", "-1", "must be >= 1"},
		{"above the spec maximum", "products", "--limit", "100", "must be <= 50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runStats(t, "stats", tc.metric, tc.flag+"="+tc.value)
			if err == nil {
				t.Fatalf("stats %s %s=%s was accepted", tc.metric, tc.flag, tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "limit") {
				t.Errorf("error = %v, want it to name the flag", err)
			}
			if len(got) > 0 {
				t.Errorf("the refused value still reached the wire: %v", got)
			}
		})
	}

	// Both ends of the legal range must still go out, or a guard that refused
	// everything would also satisfy the assertions above.
	for _, ok := range []string{"1", "50"} {
		t.Run("accepted: "+ok, func(t *testing.T) {
			got, err := runStats(t, "stats", "products", "--limit="+ok)
			if err != nil {
				t.Fatalf("--limit=%s was refused: %v", ok, err)
			}
			if got.Get("limit") != ok {
				t.Errorf("limit = %q, want %q (query: %v)", got.Get("limit"), ok, got)
			}
		})
	}
}

// TestStatsWire_EveryDeclaredFilterReachesTheServer derives its assertions from
// api.Endpoints rather than a hand-written list, so a filter added to a metric
// cannot land without a wire assertion.
//
// This closes the gap that let --business-unit-id move from a dedicated
// QueryParams field to params.Extra with no test following it: a hand-maintained
// want-map only covers what someone remembered to add.
//
// Value: protects=every filter cmd/stats.go DECLARES is actually collected and sent; fails_when=a filter is declared for a metric but the collection loop skips it, so --help advertises a filter that sends nothing; why_new=the existing wire test asserts a hand-written subset and cannot see a newly declared filter; seam=testNewRunner
func TestStatsWire_EveryDeclaredFilterReachesTheServer(t *testing.T) {
	// A legal value per filter. Keyed by spec parameter name.
	values := map[string]string{
		"business_unit_id": "3",
		"product_id":       "12",
		"channel_id":       "ch_42",
		"location_id":      "7",
		"resource_id":      "9",
		"date_basis":       "trip",
		"day_of_week":      "sat",
		"time_of_day_from": "09:00",
		"time_of_day_to":   "12:00",
	}

	checked := 0
	for i := range api.Endpoints {
		ep := &api.Endpoints[i]
		if len(ep.Filters) == 0 {
			continue
		}
		t.Run(ep.Name, func(t *testing.T) {
			args := []string{"stats", ep.Name}
			for _, f := range ep.Filters {
				v, ok := values[f]
				if !ok {
					t.Fatalf("no probe value for filter %q — add one; an unprobed filter is an unasserted one", f)
				}
				args = append(args, "--"+api.FlagName(f), v)
			}
			got, err := runStats(t, args...)
			if err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			for _, f := range ep.Filters {
				if got.Get(f) != values[f] {
					t.Errorf("%s: %s = %q, want %q — declared but not sent (query: %v)",
						ep.Name, f, got.Get(f), values[f], got)
				}
				checked++
			}
		})
	}
	if checked == 0 {
		t.Fatal("asserted no filters — the endpoint table or the runner is broken, not the CLI")
	}
}
