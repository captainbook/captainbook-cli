package api

import (
	"fmt"
	"strings"
	"testing"
)

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"AuthError", &AuthError{}, ExitAuth},
		{"ForbiddenError", &ForbiddenError{}, ExitForbidden},
		{"ValidationError", &ValidationError{}, ExitValidation},
		{"NetworkError", &NetworkError{}, ExitNetwork},
		{"TimeoutError", &TimeoutError{Duration: "30s"}, ExitNetwork},
		{"JSONParseError", &JSONParseError{}, ExitJSONParse},
		{"ServerError", &ServerError{StatusCode: 500}, ExitServer},
		{"RateLimitError", &RateLimitError{}, ExitRateLimit},
		{"UnexpectedStatusError", &UnexpectedStatusError{StatusCode: 418}, ExitUnexpected},
		{"generic error", fmt.Errorf("generic"), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExitCodeFor(tt.err)
			if got != tt.want {
				t.Errorf("ExitCodeFor(%T) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestExitCodeConstants(t *testing.T) {
	// Verify all exit codes are unique and in range 1-9
	codes := map[int]string{
		ExitAuth:       "ExitAuth",
		ExitForbidden:  "ExitForbidden",
		ExitValidation: "ExitValidation",
		ExitNetwork:    "ExitNetwork",
		ExitJSONParse:  "ExitJSONParse",
		ExitConfig:     "ExitConfig",
		ExitServer:     "ExitServer",
		ExitRateLimit:  "ExitRateLimit",
		ExitUnexpected: "ExitUnexpected",
	}

	if len(codes) != 9 {
		t.Errorf("expected 9 unique exit codes, got %d (some codes collide)", len(codes))
	}

	for code, name := range codes {
		if code < 1 || code > 9 {
			t.Errorf("%s = %d, want value in range 1-9", name, code)
		}
	}
}

func TestAuthError_Error(t *testing.T) {
	tests := []struct {
		name    string
		err     *AuthError
		wantSub string
	}{
		{"with message", &AuthError{Message: "token expired"}, "token expired"},
		{"without message", &AuthError{}, "invalid or missing token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("Error() = %q, want substring %q", got, tt.wantSub)
			}
			if !strings.Contains(got, "Authentication failed") {
				t.Errorf("Error() = %q, want prefix 'Authentication failed'", got)
			}
		})
	}
}

func TestForbiddenError_Error(t *testing.T) {
	tests := []struct {
		name    string
		err     *ForbiddenError
		wantSub string
	}{
		{"with message", &ForbiddenError{Message: "not allowed"}, "not allowed"},
		{"without message", &ForbiddenError{}, "view_reports permission"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("Error() = %q, want substring %q", got, tt.wantSub)
			}
			if !strings.Contains(got, "Access denied") {
				t.Errorf("Error() = %q, want prefix 'Access denied'", got)
			}
		})
	}
}

func TestValidationError_Error(t *testing.T) {
	tests := []struct {
		name    string
		err     *ValidationError
		wantSub string
	}{
		{
			name:    "with field errors",
			err:     &ValidationError{Errors: map[string][]string{"from": {"required"}}},
			wantSub: "from: required",
		},
		{
			name:    "with message only",
			err:     &ValidationError{Message: "bad input"},
			wantSub: "bad input",
		},
		{
			name:    "empty",
			err:     &ValidationError{},
			wantSub: "Validation error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("Error() = %q, want substring %q", got, tt.wantSub)
			}
		})
	}
}

func TestNetworkError_Error(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	err := &NetworkError{Err: inner}
	got := err.Error()
	if !strings.Contains(got, "Network error") {
		t.Errorf("Error() = %q, want prefix 'Network error'", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("Error() = %q, want substring 'connection refused'", got)
	}
}

func TestNetworkError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	err := &NetworkError{Err: inner}
	if err.Unwrap() != inner {
		t.Errorf("Unwrap() returned wrong error")
	}
}

func TestTimeoutError_Error(t *testing.T) {
	err := &TimeoutError{Duration: "30s"}
	got := err.Error()
	if !strings.Contains(got, "timed out") {
		t.Errorf("Error() = %q, want substring 'timed out'", got)
	}
	if !strings.Contains(got, "30s") {
		t.Errorf("Error() = %q, want substring '30s'", got)
	}
}

func TestJSONParseError_Error(t *testing.T) {
	inner := fmt.Errorf("unexpected EOF")
	err := &JSONParseError{Err: inner}
	got := err.Error()
	if !strings.Contains(got, "parse") {
		t.Errorf("Error() = %q, want substring 'parse'", got)
	}
	if !strings.Contains(got, "unexpected EOF") {
		t.Errorf("Error() = %q, want substring 'unexpected EOF'", got)
	}
}

func TestJSONParseError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("unexpected EOF")
	err := &JSONParseError{Err: inner}
	if err.Unwrap() != inner {
		t.Errorf("Unwrap() returned wrong error")
	}
}

func TestServerError_Error(t *testing.T) {
	err := &ServerError{StatusCode: 503, Body: "service unavailable"}
	got := err.Error()
	if !strings.Contains(got, "503") {
		t.Errorf("Error() = %q, want substring '503'", got)
	}
	if !strings.Contains(got, "Server error") {
		t.Errorf("Error() = %q, want substring 'Server error'", got)
	}
}

func TestRateLimitError_Error(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		wantSub    string
	}{
		{"with retry-after", "30", "30s"},
		{"without retry-after", "", "later"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &RateLimitError{RetryAfter: tt.retryAfter}
			got := err.Error()
			if !strings.Contains(got, "Rate limited") {
				t.Errorf("Error() = %q, want substring 'Rate limited'", got)
			}
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("Error() = %q, want substring %q", got, tt.wantSub)
			}
		})
	}
}

func TestUnexpectedStatusError_Error(t *testing.T) {
	err := &UnexpectedStatusError{StatusCode: 418, Body: "I'm a teapot"}
	got := err.Error()
	if !strings.Contains(got, "418") {
		t.Errorf("Error() = %q, want substring '418'", got)
	}
	if !strings.Contains(got, "I'm a teapot") {
		t.Errorf("Error() = %q, want substring body", got)
	}
}
