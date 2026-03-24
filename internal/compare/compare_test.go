package compare

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name        string
		shorthand   string
		from        string
		to          string
		wantFrom    string
		wantTo      string
		wantErr     bool
		errContains string
	}{
		{
			name:      "previous: Mar 1-24 gives Feb 5-28",
			shorthand: "previous",
			from:      "2026-03-01",
			to:        "2026-03-24",
			wantFrom:  "2026-02-05",
			wantTo:    "2026-02-28",
		},
		{
			name:      "previous: single day",
			shorthand: "previous",
			from:      "2026-03-15",
			to:        "2026-03-15",
			wantFrom:  "2026-03-14",
			wantTo:    "2026-03-14",
		},
		{
			name:      "previous: full month Jan",
			shorthand: "previous",
			from:      "2026-01-01",
			to:        "2026-01-31",
			wantFrom:  "2025-12-01",
			wantTo:    "2025-12-31",
		},
		{
			name:      "previous: two days",
			shorthand: "previous",
			from:      "2026-03-10",
			to:        "2026-03-11",
			wantFrom:  "2026-03-08",
			wantTo:    "2026-03-09",
		},
		{
			name:      "year-ago: basic",
			shorthand: "year-ago",
			from:      "2026-03-01",
			to:        "2026-03-24",
			wantFrom:  "2025-03-01",
			wantTo:    "2025-03-24",
		},
		{
			name:      "year-ago: leap year Feb 29 clamps to Feb 28",
			shorthand: "year-ago",
			from:      "2024-02-29",
			to:        "2024-02-29",
			wantFrom:  "2023-02-28",
			wantTo:    "2023-02-28",
		},
		{
			name:      "year-ago: across year boundary",
			shorthand: "year-ago",
			from:      "2026-01-01",
			to:        "2026-01-31",
			wantFrom:  "2025-01-01",
			wantTo:    "2025-01-31",
		},
		{
			name:        "invalid shorthand",
			shorthand:   "last-week",
			from:        "2026-03-01",
			to:          "2026-03-24",
			wantErr:     true,
			errContains: "unknown comparison shorthand",
		},
		{
			name:        "invalid from date",
			shorthand:   "previous",
			from:        "not-a-date",
			to:          "2026-03-24",
			wantErr:     true,
			errContains: "invalid from date",
		},
		{
			name:        "invalid to date",
			shorthand:   "previous",
			from:        "2026-03-01",
			to:          "not-a-date",
			wantErr:     true,
			errContains: "invalid to date",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFrom, gotTo, err := Resolve(tt.shorthand, tt.from, tt.to)

			if tt.wantErr {
				if err == nil {
					t.Fatal("Resolve() expected error, got nil")
				}
				if tt.errContains != "" {
					if got := err.Error(); !contains(got, tt.errContains) {
						t.Errorf("error = %q, want substring %q", got, tt.errContains)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("Resolve() error: %v", err)
			}
			if gotFrom != tt.wantFrom {
				t.Errorf("compareFrom = %q, want %q", gotFrom, tt.wantFrom)
			}
			if gotTo != tt.wantTo {
				t.Errorf("compareTo = %q, want %q", gotTo, tt.wantTo)
			}
		})
	}
}

func TestResolve_PreviousDurationLogic(t *testing.T) {
	// For "previous": the comparison period is the same duration, immediately before.
	// main period: Mar 10 - Mar 14 (4 day duration: 14-10=4 days)
	// compTo = Mar 9 (fromDate - 1 day)
	// compFrom = compTo - duration = Mar 9 - 4 days = Mar 5
	gotFrom, gotTo, err := Resolve("previous", "2026-03-10", "2026-03-14")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if gotFrom != "2026-03-05" {
		t.Errorf("compareFrom = %q, want %q", gotFrom, "2026-03-05")
	}
	if gotTo != "2026-03-09" {
		t.Errorf("compareTo = %q, want %q", gotTo, "2026-03-09")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
