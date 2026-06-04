package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	c := NewClient("https://example.com/", "tok123")

	if c.BaseURL != "https://example.com" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	if c.Token != "tok123" {
		t.Errorf("Token = %q, want %q", c.Token, "tok123")
	}
	if c.HTTPClient == nil {
		t.Fatal("HTTPClient is nil")
	}
}

func TestBuildURL(t *testing.T) {
	c := NewClient("https://api.example.com", "tok")

	tests := []struct {
		name     string
		endpoint *Endpoint
		params   *QueryParams
		wantSub  string // substring the URL must contain
	}{
		{
			name:     "basic with from/to",
			endpoint: &Endpoint{Path: "/revenue"},
			params:   &QueryParams{From: "2025-01-01", To: "2025-01-31"},
			wantSub:  "/statistics/revenue?",
		},
		{
			name:     "with granularity",
			endpoint: &Endpoint{Path: "/bookings"},
			params:   &QueryParams{From: "2025-01-01", To: "2025-01-31", Granularity: "day"},
			wantSub:  "granularity=day",
		},
		{
			name:     "with business_unit_id",
			endpoint: &Endpoint{Path: "/revenue"},
			params:   &QueryParams{BusinessUnitID: 42},
			wantSub:  "business_unit_id=42",
		},
		{
			name:     "excluded product_id flag is omitted",
			endpoint: &Endpoint{Path: "/gift-certificates", ExcludeCommonFlags: []string{"product_id"}},
			params:   &QueryParams{ProductID: 99},
			wantSub:  "/statistics/gift-certificates",
		},
		{
			name:     "extra params with kebab-to-snake conversion",
			endpoint: &Endpoint{Path: "/revenue"},
			params:   &QueryParams{Extra: map[string]string{"payment-method": "card"}},
			wantSub:  "payment_method=card",
		},
		{
			name:     "compare dates",
			endpoint: &Endpoint{Path: "/revenue"},
			params:   &QueryParams{CompareFrom: "2024-01-01", CompareTo: "2024-01-31"},
			wantSub:  "compare_from=2024-01-01",
		},
		{
			name:     "empty extra value is omitted",
			endpoint: &Endpoint{Path: "/revenue"},
			params:   &QueryParams{Extra: map[string]string{"payment-method": ""}},
			wantSub:  "/statistics/revenue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.buildURL(tt.endpoint, tt.params)
			if err != nil {
				t.Fatalf("buildURL() error: %v", err)
			}
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("buildURL() = %q, want substring %q", got, tt.wantSub)
			}
		})
	}

	// Verify excluded flag is NOT in the URL
	t.Run("excluded product_id not in URL", func(t *testing.T) {
		ep := &Endpoint{Path: "/gift-certificates", ExcludeCommonFlags: []string{"product_id"}}
		got, err := c.buildURL(ep, &QueryParams{ProductID: 99})
		if err != nil {
			t.Fatalf("buildURL() error: %v", err)
		}
		if strings.Contains(got, "product_id") {
			t.Errorf("buildURL() = %q, should NOT contain product_id", got)
		}
	})
}

func TestDoRequest_Success(t *testing.T) {
	body := `{"data": "ok"}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-token")
	ep := &Endpoint{Path: "/revenue"}
	got, err := c.Do(context.Background(), ep, &QueryParams{From: "2025-01-01", To: "2025-01-31"})
	if err != nil {
		t.Fatalf("Do() error: %v", err)
	}
	if string(got) != body {
		t.Errorf("Do() = %q, want %q", string(got), body)
	}
}

func TestDoRequest_StatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		headers    map[string]string
		wantType   string
	}{
		{
			name:       "401 with message",
			statusCode: http.StatusUnauthorized,
			body:       `{"message":"bad token"}`,
			wantType:   "*api.AuthError",
		},
		{
			name:       "401 without message",
			statusCode: http.StatusUnauthorized,
			body:       `{}`,
			wantType:   "*api.AuthError",
		},
		{
			name:       "403 with message",
			statusCode: http.StatusForbidden,
			body:       `{"message":"no access"}`,
			wantType:   "*api.ForbiddenError",
		},
		{
			name:       "403 without message",
			statusCode: http.StatusForbidden,
			body:       `{}`,
			wantType:   "*api.ForbiddenError",
		},
		{
			name:       "422 validation error with fields",
			statusCode: http.StatusUnprocessableEntity,
			body:       `{"message":"invalid","errors":{"from":["required"]}}`,
			wantType:   "*api.ValidationError",
		},
		{
			name:       "422 non-json body",
			statusCode: http.StatusUnprocessableEntity,
			body:       `not json`,
			wantType:   "*api.ValidationError",
		},
		{
			name:       "429 rate limit with retry-after",
			statusCode: http.StatusTooManyRequests,
			body:       `{}`,
			headers:    map[string]string{"Retry-After": "1"},
			wantType:   "*api.RateLimitError",
		},
		{
			name:       "500 server error",
			statusCode: http.StatusInternalServerError,
			body:       `internal server error`,
			wantType:   "*api.ServerError",
		},
		{
			name:       "502 server error",
			statusCode: http.StatusBadGateway,
			body:       `bad gateway`,
			wantType:   "*api.ServerError",
		},
		{
			name:       "418 unexpected status",
			statusCode: http.StatusTeapot,
			body:       `i am a teapot`,
			wantType:   "*api.UnexpectedStatusError",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer ts.Close()

			c := NewClient(ts.URL, "tok")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err := c.Do(ctx, &Endpoint{Path: "/test"}, &QueryParams{})
			if err == nil {
				t.Fatal("Do() returned nil error")
			}

			gotType := fmt.Sprintf("%T", err)
			if gotType != tt.wantType {
				t.Errorf("error type = %s, want %s (error: %v)", gotType, tt.wantType, err)
			}
		})
	}
}

func TestDoRequest_Retry(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("error"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	got, err := c.Do(context.Background(), &Endpoint{Path: "/test"}, &QueryParams{})
	if err != nil {
		t.Fatalf("Do() error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if string(got) != `{"ok":true}` {
		t.Errorf("Do() = %q", string(got))
	}
}

func TestDoRequest_NoRetryOnNonRetriable(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"bad"}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	_, err := c.Do(context.Background(), &Endpoint{Path: "/test"}, &QueryParams{})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (should not retry auth error)", attempts)
	}
}

func TestDoRequest_MaxRetriesExhausted(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	_, err := c.Do(context.Background(), &Endpoint{Path: "/test"}, &QueryParams{})
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	// maxRetries=2, so we expect 3 total attempts (initial + 2 retries)
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if _, ok := err.(*ServerError); !ok {
		t.Errorf("error type = %T, want *ServerError", err)
	}
}

func TestDoRequest_VerboseLogging(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	var buf bytes.Buffer
	c := NewClient(ts.URL, "abcdef123456")
	c.Verbose = true
	c.VerboseW = &buf

	_, err := c.Do(context.Background(), &Endpoint{Path: "/test"}, &QueryParams{})
	if err != nil {
		t.Fatalf("Do() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "GET") {
		t.Error("verbose output missing GET")
	}
	if !strings.Contains(output, "abc***") {
		t.Error("verbose output should contain redacted token abc***")
	}
	if !strings.Contains(output, "200 OK") {
		t.Error("verbose output missing 200 OK")
	}
}

func TestDoRequest_RateLimitRetryAfter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := c.Do(ctx, &Endpoint{Path: "/test"}, &QueryParams{})
	if err == nil {
		t.Fatal("expected error")
	}
	rlErr, ok := err.(*RateLimitError)
	if !ok {
		t.Fatalf("error type = %T, want *RateLimitError", err)
	}
	if rlErr.RetryAfter != "1" {
		t.Errorf("RetryAfter = %q, want %q", rlErr.RetryAfter, "1")
	}
}

func TestDoRequest_ValidationErrorFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		resp := ValidationErrorResponse{
			Message: "invalid params",
			Errors:  map[string][]string{"from": {"required"}, "to": {"must be after from"}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "tok")
	_, err := c.Do(context.Background(), &Endpoint{Path: "/test"}, &QueryParams{})
	if err == nil {
		t.Fatal("expected error")
	}
	vErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if len(vErr.Errors) == 0 {
		t.Error("expected validation errors map to be non-empty")
	}
	if msgs, ok := vErr.Errors["from"]; !ok || len(msgs) == 0 {
		t.Error("expected 'from' field error")
	}
}

func TestIsRetriable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"RateLimitError", &RateLimitError{}, true},
		{"ServerError", &ServerError{StatusCode: 500}, true},
		{"TimeoutError", &TimeoutError{Duration: "30s"}, true},
		{"NetworkError", &NetworkError{Err: nil}, true},
		{"AuthError", &AuthError{}, false},
		{"ForbiddenError", &ForbiddenError{}, false},
		{"ValidationError", &ValidationError{}, false},
		{"UnexpectedStatusError", &UnexpectedStatusError{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetriable(tt.err)
			if got != tt.want {
				t.Errorf("isRetriable(%T) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRetryBackoff(t *testing.T) {
	tests := []struct {
		name    string
		lastErr error
		attempt int
		want    time.Duration
	}{
		{"rate limit with Retry-After", &RateLimitError{RetryAfter: "30"}, 1, 30 * time.Second},
		{"rate limit without Retry-After", &RateLimitError{}, 1, 1 * time.Second},
		{"rate limit with invalid Retry-After", &RateLimitError{RetryAfter: "abc"}, 1, 1 * time.Second},
		{"server error attempt 1", &ServerError{StatusCode: 500}, 1, 1 * time.Second},
		{"server error attempt 2", &ServerError{StatusCode: 500}, 2, 2 * time.Second},
		{"network error attempt 1", &NetworkError{}, 1, 1 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retryBackoff(tt.lastErr, tt.attempt)
			if got != tt.want {
				t.Errorf("retryBackoff() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedactToken(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{"abc", "***"},
		{"abcdef", "***"},
		{"abcdefg", "abc***"},
		{"a-very-long-token-here", "a-v***"},
	}

	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			got := redactToken(tt.token)
			if got != tt.want {
				t.Errorf("redactToken(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{2048, "2.0KB"},
		{1536, "1.5KB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytes(tt.n)
			if got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short string", "short", 10, "short"},
		{"exact length", "exactly10!", 10, "exactly10!"},
		{"long string", "this is a longer string", 10, "this is a ..."},
		{"empty string", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
			}
		})
	}
}
