package api

import "testing"

// endpointByName is a test helper that looks up an endpoint by name.
func endpointByName(name string) *Endpoint {
	for i := range Endpoints {
		if Endpoints[i].Name == name {
			return &Endpoints[i]
		}
	}
	return nil
}

// TestEndpointsCount is a tripwire, not coverage. It asserts a literal against
// the table that produced it, so it can only catch an accidental deletion — it
// cannot tell whether the table matches the server. That is what the statistics
// drift tests in statistics_drift_test.go do, by reading the vendored spec.
func TestEndpointsCount(t *testing.T) {
	if got := len(Endpoints); got != 13 {
		t.Errorf("len(Endpoints) = %d, want 13", got)
	}
}

func TestAllEndpointsExist(t *testing.T) {
	expected := []struct {
		name string
		path string
	}{
		{"revenue", "/revenue"},
		{"bookings", "/bookings"},
		{"products", "/products"},
		{"resources", "/resources"},
		{"customers", "/customers"},
		{"channels", "/channels"},
		{"occupancy", "/occupancy"},
		{"extras", "/extras"},
		{"discounts", "/discounts"},
		{"locations", "/locations"},
		{"gift-certs", "/gift-certificates"},
		{"browsing", "/browsing"},
		{"summary", "/summary"},
	}

	for _, tt := range expected {
		t.Run(tt.name, func(t *testing.T) {
			ep := endpointByName(tt.name)
			if ep == nil {
				t.Fatalf("endpointByName(%q) = nil", tt.name)
			}
			if ep.Path != tt.path {
				t.Errorf("endpoint %q path = %q, want %q", tt.name, ep.Path, tt.path)
			}
			if ep.Description == "" {
				t.Errorf("endpoint %q has empty description", tt.name)
			}
		})
	}
}

func TestEndpointByName(t *testing.T) {
	tests := []struct {
		name    string
		wantNil bool
	}{
		{"revenue", false},
		{"bookings", false},
		{"summary", false},
		{"nonexistent", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := endpointByName(tt.name)
			if tt.wantNil && ep != nil {
				t.Errorf("endpointByName(%q) = %v, want nil", tt.name, ep)
			}
			if !tt.wantNil && ep == nil {
				t.Errorf("endpointByName(%q) = nil, want non-nil", tt.name)
			}
		})
	}
}

// TestSupportsFilter pins the per-metric filter vocabulary. The server REFUSES
// a filter a metric does not implement (422, not a silent drop), so these are
// not preferences — a wrong entry here ships a flag that cannot work.
//
// The reasons behind each refusal live in endpoints.go next to the entry.
func TestSupportsFilter(t *testing.T) {
	tests := []struct {
		endpoint string
		filter   string
		want     bool
	}{
		// gift-certs is an amount, not a trip: business unit only.
		{"gift-certs", FilterBusinessUnitID, true},
		{"gift-certs", FilterProductID, false},
		{"gift-certs", FilterChannelID, false},
		// occupancy counts slots, so it joins no bookings: no channel, no
		// resource, and it is dated by the departure already.
		{"occupancy", FilterLocationID, true},
		{"occupancy", FilterChannelID, false},
		{"occupancy", FilterResourceID, false},
		{"occupancy", FilterDateBasis, false},
		{"occupancy", FilterDayOfWeek, false},
		// browsing reads the event spine, which joins no bookings. day_of_week
		// is the one filter it can take.
		{"browsing", FilterBusinessUnitID, true},
		{"browsing", FilterDayOfWeek, true},
		{"browsing", FilterProductID, false},
		// summary reports only what ALL its sections can honour.
		{"summary", FilterProductID, true},
		{"summary", FilterLocationID, true},
		{"summary", FilterChannelID, false},
		{"summary", FilterResourceID, false},
		{"summary", FilterDateBasis, false},
		// revenue is dated by when the money arrived, so no trip basis.
		{"revenue", FilterDayOfWeek, true},
		{"revenue", FilterDateBasis, false},
		// bookings is the only metric taking the time-of-day window.
		{"bookings", FilterTimeOfDayFrom, true},
		{"bookings", FilterTimeOfDayTo, true},
		{"bookings", FilterDateBasis, true},
		{"revenue", FilterTimeOfDayFrom, false},
		{"channels", FilterTimeOfDayFrom, false},
		// channels takes the departure's weekday but not the trip basis.
		{"channels", FilterDayOfWeek, true},
		{"channels", FilterDateBasis, false},
		// locations is booking-grain with the trip basis, like products.
		{"locations", FilterDateBasis, true},
		{"locations", FilterResourceID, true},
		// extras and discounts are deferred the basis they would share.
		{"extras", FilterDateBasis, false},
		{"discounts", FilterDateBasis, false},
	}

	for _, tt := range tests {
		ep := endpointByName(tt.endpoint)
		if ep == nil {
			t.Fatalf("endpointByName(%q) = nil", tt.endpoint)
		}
		if got := ep.SupportsFilter(tt.filter); got != tt.want {
			t.Errorf("%s.SupportsFilter(%q) = %v, want %v", tt.endpoint, tt.filter, got, tt.want)
		}
	}
}

// TestFlagName pins the parameter-to-flag spelling. The statistics CLI derives
// every filter flag from its spec parameter name, which is how a renamed
// parameter cannot drift into a flag that sends a key the server ignores.
func TestFlagName(t *testing.T) {
	cases := map[string]string{
		FilterTimeOfDayFrom:  "time-of-day-from",
		FilterTimeOfDayTo:    "time-of-day-to",
		FilterBusinessUnitID: "business-unit-id",
		FilterDateBasis:      "date-basis",
		FilterChannelID:      "channel-id",
	}
	for param, want := range cases {
		if got := FlagName(param); got != want {
			t.Errorf("FlagName(%q) = %q, want %q", param, got, want)
		}
	}
}

func TestEndpointExtraFlags(t *testing.T) {
	tests := []struct {
		endpoint  string
		wantFlags []string
	}{
		// day-of-week and the time-of-day window are FILTERS now, not
		// endpoint-specific extras: they are part of the shared vocabulary the
		// server gates per metric, and `--time-from`/`--time-to` were renamed to
		// the spec's own `time_of_day_from`/`time_of_day_to` because the old
		// names sent query keys the server has never accepted. See
		// TestSupportsFilter and TestFlagName.
		{"revenue", []string{"payment-method", "origin"}},
		{"bookings", []string{"status", "product-option-id"}},
		{"locations", []string{"sort-by", "sort-direction", "limit"}},
		{"browsing", nil},
		{"products", []string{"sort-by", "sort-direction", "limit"}},
		{"channels", nil},
		{"discounts", nil},
		{"summary", nil},
	}

	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			ep := endpointByName(tt.endpoint)
			if ep == nil {
				t.Fatalf("endpointByName(%q) = nil", tt.endpoint)
			}
			if tt.wantFlags == nil {
				if len(ep.ExtraFlags) != 0 {
					t.Errorf("endpoint %q has %d extra flags, want 0", tt.endpoint, len(ep.ExtraFlags))
				}
				return
			}
			if len(ep.ExtraFlags) != len(tt.wantFlags) {
				t.Fatalf("endpoint %q has %d extra flags, want %d", tt.endpoint, len(ep.ExtraFlags), len(tt.wantFlags))
			}
			for i, wantName := range tt.wantFlags {
				if ep.ExtraFlags[i].Name != wantName {
					t.Errorf("ExtraFlags[%d].Name = %q, want %q", i, ep.ExtraFlags[i].Name, wantName)
				}
			}
		})
	}
}

func TestEndpointExtraFlagEnums(t *testing.T) {
	ep := endpointByName("revenue")
	if ep == nil {
		t.Fatal("revenue endpoint not found")
	}

	// payment-method should have enums
	pmFlag := ep.ExtraFlags[0]
	if pmFlag.Name != "payment-method" {
		t.Fatalf("ExtraFlags[0].Name = %q, want payment-method", pmFlag.Name)
	}
	wantEnums := []string{"card", "cash", "paypal", "gift", "voucher"}
	if len(pmFlag.Enum) != len(wantEnums) {
		t.Fatalf("payment-method enum count = %d, want %d", len(pmFlag.Enum), len(wantEnums))
	}
	for i, e := range wantEnums {
		if pmFlag.Enum[i] != e {
			t.Errorf("Enum[%d] = %q, want %q", i, pmFlag.Enum[i], e)
		}
	}
}
