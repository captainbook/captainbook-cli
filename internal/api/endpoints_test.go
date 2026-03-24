package api

import "testing"

func TestEndpointsCount(t *testing.T) {
	if got := len(Endpoints); got != 11 {
		t.Errorf("len(Endpoints) = %d, want 11", got)
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
		{"gift-certs", "/gift-certificates"},
		{"summary", "/summary"},
	}

	for _, tt := range expected {
		t.Run(tt.name, func(t *testing.T) {
			ep := EndpointByName(tt.name)
			if ep == nil {
				t.Fatalf("EndpointByName(%q) = nil", tt.name)
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
			ep := EndpointByName(tt.name)
			if tt.wantNil && ep != nil {
				t.Errorf("EndpointByName(%q) = %v, want nil", tt.name, ep)
			}
			if !tt.wantNil && ep == nil {
				t.Errorf("EndpointByName(%q) = nil, want non-nil", tt.name)
			}
		})
	}
}

func TestHasExcludedFlag(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		flag     string
		want     bool
	}{
		{
			name:     "gift-certs excludes product_id",
			endpoint: "gift-certs",
			flag:     "product_id",
			want:     true,
		},
		{
			name:     "gift-certs does not exclude business_unit_id",
			endpoint: "gift-certs",
			flag:     "business_unit_id",
			want:     false,
		},
		{
			name:     "revenue does not exclude product_id",
			endpoint: "revenue",
			flag:     "product_id",
			want:     false,
		},
		{
			name:     "revenue does not exclude business_unit_id",
			endpoint: "revenue",
			flag:     "business_unit_id",
			want:     false,
		},
		{
			name:     "bookings does not exclude anything",
			endpoint: "bookings",
			flag:     "product_id",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := EndpointByName(tt.endpoint)
			if ep == nil {
				t.Fatalf("EndpointByName(%q) = nil", tt.endpoint)
			}
			got := ep.HasExcludedFlag(tt.flag)
			if got != tt.want {
				t.Errorf("HasExcludedFlag(%q) = %v, want %v", tt.flag, got, tt.want)
			}
		})
	}
}

func TestEndpointExtraFlags(t *testing.T) {
	tests := []struct {
		endpoint  string
		wantFlags []string
	}{
		{"revenue", []string{"payment-method", "origin", "day-of-week"}},
		{"bookings", []string{"status", "product-option-id", "day-of-week", "time-from", "time-to"}},
		{"products", []string{"sort-by", "sort-direction", "limit"}},
		{"channels", nil},
		{"discounts", nil},
		{"summary", nil},
	}

	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			ep := EndpointByName(tt.endpoint)
			if ep == nil {
				t.Fatalf("EndpointByName(%q) = nil", tt.endpoint)
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
	ep := EndpointByName("revenue")
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
