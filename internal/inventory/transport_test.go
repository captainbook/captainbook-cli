package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ----- helpers ---------------------------------------------------------------

// recordingRT is a stub RoundTripper that records every request it sees and
// replays a scripted sequence of responses. Each scripted entry is either a
// fully-formed *http.Response (status + headers + body) or an error.
type recordingRT struct {
	mu       sync.Mutex
	requests []recordedRequest
	scripted []scriptStep
	idx      int
}

type recordedRequest struct {
	Method     string
	URL        *url.URL
	Header     http.Header
	BodyBytes  []byte
	HasGetBody bool
}

type scriptStep struct {
	status     int
	headers    http.Header
	body       string
	err        error
	delay      time.Duration
	bodyCloser func() error // optional override for response Body.Close
}

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := recordedRequest{
		Method:     req.Method,
		URL:        cloneURL(req.URL),
		Header:     req.Header.Clone(),
		HasGetBody: req.GetBody != nil,
	}
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		rec.BodyBytes = b
	}

	r.mu.Lock()
	r.requests = append(r.requests, rec)
	step := scriptStep{status: http.StatusOK}
	if r.idx < len(r.scripted) {
		step = r.scripted[r.idx]
	}
	r.idx++
	r.mu.Unlock()

	if step.delay > 0 {
		time.Sleep(step.delay)
	}
	if step.err != nil {
		return nil, step.err
	}

	resp := &http.Response{
		StatusCode: step.status,
		Status:     fmt.Sprintf("%d %s", step.status, http.StatusText(step.status)),
		Header:     step.headers,
		Body:       io.NopCloser(strings.NewReader(step.body)),
		Request:    req,
	}
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	return resp, nil
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	c := *u
	return &c
}

// newRequest builds a request and wires GetBody for retry-safety.
func newRequest(t *testing.T, method, rawURL string, body []byte) *http.Request {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	return req
}

// ----- requestURLValidator ---------------------------------------------------

func TestRequestURLValidator_RejectsMissingScheme(t *testing.T) {
	rec := &recordingRT{}
	rt := &requestURLValidatorRT{next: rec}

	req := newRequest(t, http.MethodGet, "//example.com/foo", nil)
	// Force the missing-scheme condition; http.NewRequest tolerates this URL.
	if req.URL.Scheme != "" {
		t.Fatalf("test setup: expected empty scheme, got %q", req.URL.Scheme)
	}

	resp, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatalf("expected error for missing scheme, got resp=%v", resp)
	}
	if !strings.Contains(err.Error(), "no scheme") {
		t.Errorf("error should mention scheme; got %q", err.Error())
	}
	if len(rec.requests) != 0 {
		t.Errorf("inner RT should not have been called; got %d", len(rec.requests))
	}
}

func TestRequestURLValidator_PassesHTTPS(t *testing.T) {
	rec := &recordingRT{}
	rt := &requestURLValidatorRT{next: rec}

	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/api/v1/cli/products", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 1 {
		t.Fatalf("expected 1 request to inner, got %d", len(rec.requests))
	}
}

// ----- bearerAuth ------------------------------------------------------------

func TestBearerAuth_SetsAuthorizationHeader(t *testing.T) {
	rec := &recordingRT{}
	rt := &bearerAuthRT{next: rec, token: "supersecrettoken"}

	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	got := rec.requests[0].Header.Get("Authorization")
	if got != "Bearer supersecrettoken" {
		t.Errorf("Authorization mismatch: got %q", got)
	}
}

func TestBearerAuth_VerboseRedactsToken(t *testing.T) {
	rec := &recordingRT{}
	var buf bytes.Buffer
	tok := "supersecrettoken"
	rt := &bearerAuthRT{next: rec, token: tok, verbose: true, verboseW: &buf}

	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, tok) {
		t.Errorf("verbose output leaked full token: %q", out)
	}
	if !strings.Contains(out, "sup***") {
		t.Errorf("verbose output should redact to first-3-chars + ***; got %q", out)
	}
}

func TestBearerAuth_VerboseShortTokenFullyRedacted(t *testing.T) {
	rec := &recordingRT{}
	var buf bytes.Buffer
	rt := &bearerAuthRT{next: rec, token: "short", verbose: true, verboseW: &buf}

	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "short") {
		t.Errorf("short token leaked: %q", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("short token should redact to ***; got %q", out)
	}
}

// ----- idempotencyKey --------------------------------------------------------

func TestIdempotencyKey_MintedAsUUIDv7ForMutations(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := &recordingRT{}
			rt := &idempotencyKeyRT{next: rec, mint: mintUUIDv7}
			req := newRequest(t, method, "https://acme.captainbook.io/foo", []byte(`{}`))
			if _, err := rt.RoundTrip(req); err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			key := rec.requests[0].Header.Get("Idempotency-Key")
			if key == "" {
				t.Fatalf("expected Idempotency-Key to be set")
			}
			parsed, err := uuid.Parse(key)
			if err != nil {
				t.Fatalf("Idempotency-Key %q is not a valid UUID: %v", key, err)
			}
			if parsed.Version() != 7 {
				t.Errorf("expected UUIDv7, got version %d (key=%s)", parsed.Version(), key)
			}
		})
	}
}

func TestIdempotencyKey_PreservesCallerSuppliedKey(t *testing.T) {
	rec := &recordingRT{}
	rt := &idempotencyKeyRT{next: rec, mint: mintUUIDv7}
	req := newRequest(t, http.MethodPost, "https://acme.captainbook.io/foo", []byte(`{}`))
	req.Header.Set("Idempotency-Key", "caller-supplied-key")

	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	got := rec.requests[0].Header.Get("Idempotency-Key")
	if got != "caller-supplied-key" {
		t.Errorf("caller-supplied key was overwritten: got %q", got)
	}
}

func TestIdempotencyKey_AbsentForGET(t *testing.T) {
	rec := &recordingRT{}
	rt := &idempotencyKeyRT{next: rec, mint: mintUUIDv7}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got := rec.requests[0].Header.Get("Idempotency-Key"); got != "" {
		t.Errorf("GET should not have Idempotency-Key; got %q", got)
	}
}

func TestIdempotencyKey_MintErrorSurfaces(t *testing.T) {
	rec := &recordingRT{}
	rt := &idempotencyKeyRT{
		next: rec,
		mint: func() (string, error) { return "", errors.New("clock skew") },
	}
	req := newRequest(t, http.MethodPost, "https://acme.captainbook.io/foo", []byte(`{}`))
	_, err := rt.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "clock skew") {
		t.Errorf("expected mint error to surface, got %v", err)
	}
}

// ----- retry -----------------------------------------------------------------

func TestRetry_ReplaysBodyAndReusesIdempotencyKey(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusServiceUnavailable},
			{status: http.StatusServiceUnavailable},
			{status: http.StatusOK, body: `{"ok":true}`},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}

	body := []byte(`{"name":"snorkel tour"}`)
	req := newRequest(t, http.MethodPost, "https://acme.captainbook.io/foo", body)
	req.Header.Set("Idempotency-Key", "stable-key")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected final 200, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(rec.requests))
	}
	for i, r := range rec.requests {
		if !bytes.Equal(r.BodyBytes, body) {
			t.Errorf("attempt %d body mismatch: got %q want %q", i, r.BodyBytes, body)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "stable-key" {
			t.Errorf("attempt %d Idempotency-Key drifted: got %q", i, got)
		}
	}
}

func TestRetry_OneRetryThenSuccess(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusServiceUnavailable},
			{status: http.StatusOK, body: "ok"},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}

	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 2 {
		t.Errorf("expected 2 attempts, got %d", len(rec.requests))
	}
}

func TestRetry_RetriableStatuses(t *testing.T) {
	for _, code := range []int{
		http.StatusBadGateway,
		http.StatusGatewayTimeout,
		http.StatusTooManyRequests,
	} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			rec := &recordingRT{
				scripted: []scriptStep{
					{status: code, headers: http.Header{}},
					{status: http.StatusOK, body: "ok"},
				},
			}
			rt := &retryRT{next: rec, sleep: func(time.Duration) {}}
			req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			if resp.StatusCode != 200 {
				t.Errorf("expected eventual 200, got %d", resp.StatusCode)
			}
			if len(rec.requests) != 2 {
				t.Errorf("expected 2 attempts for %d, got %d", code, len(rec.requests))
			}
		})
	}
}

func TestRetry_DoesNotRetryOn500(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusInternalServerError, body: "boom"},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 500 {
		t.Errorf("expected surfaced 500, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 1 {
		t.Errorf("expected exactly 1 attempt for 500 (no retry), got %d", len(rec.requests))
	}
}

func TestRetry_DoesNotRetryOn501(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusNotImplemented},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 501 {
		t.Errorf("expected surfaced 501, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 1 {
		t.Errorf("expected 1 attempt for 501, got %d", len(rec.requests))
	}
}

func TestRetry_DoesNotRetryOn4xx(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusUnprocessableEntity, body: "validation"},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 422 {
		t.Errorf("expected 422, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 1 {
		t.Errorf("expected no retry on 4xx, got %d attempts", len(rec.requests))
	}
}

func TestRetry_OnNetworkError(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{err: errors.New("connection reset")},
			{status: http.StatusOK, body: "ok"},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 2 {
		t.Errorf("expected 2 attempts, got %d", len(rec.requests))
	}
}

func TestRetry_RetryAfterHonoredWhenSmall(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{
				status:  http.StatusTooManyRequests,
				headers: http.Header{"Retry-After": []string{"2"}},
			},
			{status: http.StatusOK, body: "ok"},
		},
	}
	var sleeps []time.Duration
	rt := &retryRT{next: rec, sleep: func(d time.Duration) { sleeps = append(sleeps, d) }}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if len(sleeps) != 1 {
		t.Fatalf("expected 1 sleep, got %d", len(sleeps))
	}
	if sleeps[0] != 2*time.Second {
		t.Errorf("expected 2s sleep from Retry-After, got %s", sleeps[0])
	}
}

func TestRetry_RetryAfterIgnoredWhenTooLarge(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{
				status:  http.StatusTooManyRequests,
				headers: http.Header{"Retry-After": []string{strconv.Itoa(maxRetryAfterSecs + 1)}},
			},
			{status: http.StatusOK, body: "ok"},
		},
	}
	var sleeps []time.Duration
	rt := &retryRT{next: rec, sleep: func(d time.Duration) { sleeps = append(sleeps, d) }}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if len(sleeps) != 1 {
		t.Fatalf("expected 1 sleep, got %d", len(sleeps))
	}
	if sleeps[0] != initialBackoff {
		t.Errorf("oversized Retry-After should fall back to backoff %s; got %s", initialBackoff, sleeps[0])
	}
}

func TestRetry_ExhaustedReturnsLastResponse(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusServiceUnavailable, body: "1"},
			{status: http.StatusServiceUnavailable, body: "2"},
			{status: http.StatusServiceUnavailable, body: "3"},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Errorf("expected last 503 surfaced, got %d", resp.StatusCode)
	}
	if len(rec.requests) != maxRetries+1 {
		t.Errorf("expected %d total attempts, got %d", maxRetries+1, len(rec.requests))
	}
}

func TestRetry_ExhaustedReturnsLastError(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{err: errors.New("net1")},
			{err: errors.New("net2")},
			{err: errors.New("net3-final")},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}
	req := newRequest(t, http.MethodGet, "https://acme.captainbook.io/foo", nil)
	resp, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatalf("expected exhausted error, got resp=%v", resp)
	}
	if !strings.Contains(err.Error(), "net3-final") {
		t.Errorf("expected last error surfaced; got %v", err)
	}
}

func TestRetry_BodyWithoutGetBodyFailsClearly(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusServiceUnavailable},
			{status: http.StatusOK, body: "ok"},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}

	// Build a request with a body but no GetBody — violates the contract.
	req, err := http.NewRequest(http.MethodPost, "https://acme.captainbook.io/foo", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.GetBody = nil

	_, err = rt.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "GetBody") {
		t.Errorf("expected clear GetBody error on retry replay; got %v", err)
	}
}

// ----- end-to-end via httptest ------------------------------------------------

func TestNew_EndToEndChainOrder(t *testing.T) {
	// httptest server records request headers and body, returning 503 once
	// then 200. Verifies New() composes the chain correctly: bearer set,
	// idempotency key minted+reused on retry, body replayed.
	var (
		mu      sync.Mutex
		seen    []http.Header
		bodies  [][]byte
		callCnt int32
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		bodies = append(bodies, body)
		mu.Unlock()
		n := atomic.AddInt32(&callCnt, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	srvURL, _ := url.Parse(srv.URL)
	chain := New(Config{
		Token:        "endtoendtoken",
		ExpectedHost: srvURL.Host,
		Verbose:      false,
	}, nil)

	body := []byte(`{"thing":"value"}`)
	req := newRequest(t, http.MethodPost, srv.URL+"/foo", body)

	resp, err := chain.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected eventual 200, got %d", resp.StatusCode)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 server hits, got %d", len(seen))
	}

	// Bearer set on every attempt.
	for i, h := range seen {
		if got := h.Get("Authorization"); got != "Bearer endtoendtoken" {
			t.Errorf("attempt %d Authorization: got %q", i, got)
		}
	}
	// Same Idempotency-Key on every attempt; valid UUIDv7.
	key1 := seen[0].Get("Idempotency-Key")
	key2 := seen[1].Get("Idempotency-Key")
	if key1 == "" || key1 != key2 {
		t.Errorf("Idempotency-Key must be set and reused on retry: %q vs %q", key1, key2)
	}
	if u, err := uuid.Parse(key1); err != nil || u.Version() != 7 {
		t.Errorf("Idempotency-Key %q is not a valid UUIDv7", key1)
	}
	// Body replayed identically.
	for i, b := range bodies {
		if !bytes.Equal(b, body) {
			t.Errorf("attempt %d body: got %q want %q", i, b, body)
		}
	}
}

func TestNew_RejectsHTTPSchemelessURL(t *testing.T) {
	chain := New(Config{Token: "tok"}, nil)
	req := newRequest(t, http.MethodGet, "//missing-scheme.example/foo", nil)
	_, err := chain.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Errorf("expected scheme error from chain; got %v", err)
	}
}

func TestNew_NilBaseUsesDefault(t *testing.T) {
	// Just exercise the nil-base branch. Use a stopped server URL so we
	// don't actually hit anything; we expect a transport-level error.
	chain := New(Config{Token: "tok"}, nil)
	req := newRequest(t, http.MethodGet, "https://127.0.0.1:1/never", nil)
	resp, err := chain.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		// Connection refused is the expected outcome; tolerate weird CI
		// where something happens to listen there.
		t.Logf("unexpected success against 127.0.0.1:1; tolerating")
	}
}

// ----- F5: host validation ---------------------------------------------------

func TestRequestURLValidator_RefusesHostMismatch(t *testing.T) {
	// Token MUST NOT leak to a different host than the configured profile.
	// Construct a chain expecting acme.captainbook.io; send to evil.example.
	rec := &recordingRT{
		scripted: []scriptStep{{status: http.StatusOK, body: "ok"}},
	}
	rt := &requestURLValidatorRT{next: rec, expectedHost: "acme.captainbook.io"}

	req := newRequest(t, http.MethodPost, "https://evil.example/foo", []byte(`{}`))
	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected host-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "evil.example") || !strings.Contains(err.Error(), "acme.captainbook.io") {
		t.Errorf("error must name both hosts; got %v", err)
	}
	// Critical: the inner round-tripper must NOT have been called.
	if len(rec.requests) != 0 {
		t.Errorf("inner RT was called %d times; must be 0 on host mismatch", len(rec.requests))
	}
}

func TestRequestURLValidator_PassesMatchingHost(t *testing.T) {
	rec := &recordingRT{
		scripted: []scriptStep{{status: http.StatusOK, body: "ok"}},
	}
	rt := &requestURLValidatorRT{next: rec, expectedHost: "acme.captainbook.io"}

	req := newRequest(t, http.MethodPost, "https://ACME.captainbook.io/foo", []byte(`{}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected pass with case-insensitive host match; got %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRequestURLValidator_EmptyExpectedHostSkipsCheck(t *testing.T) {
	// expectedHost == "" preserves backward compat for tests / callers that
	// don't want host enforcement. Production callers should always set it.
	rec := &recordingRT{
		scripted: []scriptStep{{status: http.StatusOK, body: "ok"}},
	}
	rt := &requestURLValidatorRT{next: rec, expectedHost: ""}

	req := newRequest(t, http.MethodPost, "https://anywhere.example/foo", []byte(`{}`))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Errorf("empty expectedHost should skip host check; got %v", err)
	}
}

// ----- F4 / D13: IDEMPOTENCY_IN_PROGRESS auto-retry --------------------------

func TestRetry_IdempotencyInProgress_RetriesOnceAt250ms(t *testing.T) {
	inProgressBody := `{"error":{"code":"IDEMPOTENCY_IN_PROGRESS","message":"still processing"}}`
	successBody := `{"data":{"id":"prod_1"}}`

	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusConflict, headers: http.Header{"Content-Type": []string{"application/json"}}, body: inProgressBody},
			{status: http.StatusOK, body: successBody},
		},
	}
	var slept []time.Duration
	rt := &retryRT{
		next:  rec,
		sleep: func(d time.Duration) { slept = append(slept, d) },
	}

	req := newRequest(t, http.MethodPost, "https://acme.captainbook.io/products", []byte(`{}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after IN_PROGRESS retry, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 2 {
		t.Errorf("expected 2 attempts, got %d", len(rec.requests))
	}
	if len(slept) != 1 || slept[0] != idempotencyInProgressDelay {
		t.Errorf("expected one 250ms sleep, got %v", slept)
	}
}

func TestRetry_IdempotencyInProgress_OnlyOneAutoRetry(t *testing.T) {
	// Per D13: ONE auto-retry. If still in-progress, surface the typed error.
	body := `{"error":{"code":"IDEMPOTENCY_IN_PROGRESS","message":"still processing"}}`
	rec := &recordingRT{
		scripted: []scriptStep{
			{status: http.StatusConflict, body: body},
			{status: http.StatusConflict, body: body},
		},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}

	req := newRequest(t, http.MethodPost, "https://acme.captainbook.io/products", []byte(`{}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409 surfaced after one retry, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 2 {
		t.Errorf("expected exactly 2 attempts (one retry), got %d", len(rec.requests))
	}
	// Body must be readable (we restored it before surfacing).
	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), "IDEMPOTENCY_IN_PROGRESS") {
		t.Errorf("body lost or unreadable on surfaced response: %q", string(got))
	}
}

func TestRetry_IdempotencyConflict_NotAutoRetried(t *testing.T) {
	// Plain IDEMPOTENCY_CONFLICT (different request body, same key) must
	// NOT auto-retry — the caller minted the wrong key. Surface immediately.
	body := `{"error":{"code":"IDEMPOTENCY_CONFLICT","message":"key reused with different body"}}`
	rec := &recordingRT{
		scripted: []scriptStep{{status: http.StatusConflict, body: body}},
	}
	rt := &retryRT{next: rec, sleep: func(time.Duration) {}}

	req := newRequest(t, http.MethodPost, "https://acme.captainbook.io/products", []byte(`{}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
	if len(rec.requests) != 1 {
		t.Errorf("plain IDEMPOTENCY_CONFLICT must not auto-retry; got %d attempts", len(rec.requests))
	}
	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), "IDEMPOTENCY_CONFLICT") {
		t.Errorf("body must be preserved on surfaced 409: %q", string(got))
	}
}
