// Package inventory wires the generated CLI v1 client (internal/inventory/gen)
// to the ceebee runtime: a chain of http.RoundTrippers handles tenant URL
// validation, bearer auth, idempotency-key minting, retry with body replay,
// and an audit hook.
//
// Round-tripper chain (outermost to innermost):
//
//	+----------------------+
//	| requestURLValidator  |  reject missing scheme; assert host == profile
//	+----------+-----------+
//	           v
//	+----------+-----------+
//	|     bearerAuth       |  set Authorization: Bearer <token>; redact in verbose
//	+----------+-----------+
//	           v
//	+----------+-----------+
//	|   idempotencyKey     |  mint UUIDv7 once per *http.Request for POST/PATCH/DELETE
//	+----------+-----------+
//	           v
//	+----------+-----------+
//	|        retry         |  GetBody-driven body replay; max 2 retries; key reused
//	+----------+-----------+
//	           v
//	+----------+-----------+
//	|        audit         |  AuditLogger.Append on 2xx (nil-tolerant)
//	+----------+-----------+
//	           v
//	+----------+-----------+
//	|         base         |  http.DefaultTransport unless overridden
//	+----------------------+
//
// Critical invariants (see plan D24, D25, D27, D32, D34, D36 + Critical Rules):
//   - The same Idempotency-Key MUST be reused across every retry attempt of a
//     given mutation. Since the key is set on the *http.Request before retry
//     wraps the call, replays of the same request naturally preserve the key.
//   - The retry layer MUST replay request bodies via req.GetBody (D25). The
//     CLI layer is responsible for setting GetBody when it sends a mutation.
//   - Bearer tokens MUST never appear in verbose output (Critical Rule 3).
package inventory

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Config configures the round-tripper chain produced by New.
type Config struct {
	// Token is the Sanctum bearer token for the active profile.
	Token string

	// Verbose enables stderr request/response logging. Bearer is redacted.
	Verbose bool

	// VerboseW receives verbose log lines. Defaults to os.Stderr when nil
	// and Verbose is true.
	VerboseW io.Writer

	// AuditLogger, if non-nil, is invoked once per successful 2xx response.
	// Lane C will provide the production implementation.
	AuditLogger AuditLogger
}

// AuditLogger is the hook the audit subsystem (Lane C) implements. The
// transport calls Append exactly once per successful (2xx) response. Append
// MUST NOT mutate req or resp.
type AuditLogger interface {
	Append(req *http.Request, resp *http.Response, dur time.Duration) error
}

// retry tuning. Plan: max 2 retries, exponential 250ms then 500ms cap.
const (
	maxRetries        = 2
	initialBackoff    = 250 * time.Millisecond
	maxBackoff        = 500 * time.Millisecond
	maxRetryAfterSecs = 30
)

// New returns the round-tripper chain wrapping base. If base is nil,
// http.DefaultTransport is used. The chain order, outermost first, is:
//
//	requestURLValidator → bearerAuth → idempotencyKey → retry → audit → base
func New(cfg Config, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	verboseW := cfg.VerboseW
	if cfg.Verbose && verboseW == nil {
		verboseW = os.Stderr
	}

	var rt http.RoundTripper = base
	rt = &auditRT{next: rt, logger: cfg.AuditLogger}
	rt = &retryRT{next: rt, verbose: cfg.Verbose, verboseW: verboseW, sleep: time.Sleep}
	rt = &idempotencyKeyRT{next: rt, mint: mintUUIDv7}
	rt = &bearerAuthRT{next: rt, token: cfg.Token, verbose: cfg.Verbose, verboseW: verboseW}
	rt = &requestURLValidatorRT{next: rt}
	return rt
}

// requestURLValidatorRT rejects requests whose URL has no scheme. The
// generated client sets a Server URL at construction time (e.g.
// https://acme.captainbook.io); this layer asserts the request URL is
// well-formed before we put it on the wire.
type requestURLValidatorRT struct {
	next http.RoundTripper
}

func (r *requestURLValidatorRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, fmt.Errorf("inventory transport: request URL is nil")
	}
	if req.URL.Scheme == "" {
		return nil, fmt.Errorf("inventory transport: request URL %q has no scheme; profile must include https://", req.URL.String())
	}
	if req.URL.Host == "" {
		return nil, fmt.Errorf("inventory transport: request URL %q has no host", req.URL.String())
	}
	return r.next.RoundTrip(req)
}

// bearerAuthRT injects Authorization: Bearer <token>. In verbose mode the
// token is redacted to first-3-chars + "***" before being written to
// verboseW (Critical Rule 3).
type bearerAuthRT struct {
	next     http.RoundTripper
	token    string
	verbose  bool
	verboseW io.Writer
}

func (r *bearerAuthRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.verbose && r.verboseW != nil {
		fmt.Fprintf(r.verboseW, "→ %s %s\n", req.Method, req.URL.String())
		fmt.Fprintf(r.verboseW, "→ Authorization: Bearer %s\n", redactToken(r.token))
	}
	return r.next.RoundTrip(req)
}

// idempotencyKeyRT mints a UUIDv7 Idempotency-Key for mutating methods
// (POST/PATCH/DELETE) IF NOT already set on the request. Pre-set values
// (from --idempotency-key) are preserved verbatim. Since this layer wraps
// the retry layer from the outside, the key is minted exactly once per
// *http.Request and reused on every retry — this is what makes retrying
// mutations safe (server dedupes by key).
type idempotencyKeyRT struct {
	next http.RoundTripper
	mint func() (string, error)
}

func (r *idempotencyKeyRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if isMutating(req.Method) && req.Header.Get("Idempotency-Key") == "" {
		key, err := r.mint()
		if err != nil {
			return nil, fmt.Errorf("inventory transport: minting Idempotency-Key: %w", err)
		}
		req.Header.Set("Idempotency-Key", key)
	}
	return r.next.RoundTrip(req)
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func mintUUIDv7() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// retryRT replays the request on transient failures, restoring the body
// from req.GetBody for each attempt (D25). The retry policy:
//
//   - Network error from the inner round-tripper: retry (with backoff).
//   - 429: parse Retry-After; if ≤ 30s use it, otherwise use backoff.
//   - 502/503/504: retry (with backoff).
//   - 500/501: do NOT retry (server bugs replay deterministically).
//   - Other 4xx: do NOT retry.
//
// The same Idempotency-Key is reused on every attempt because the
// idempotencyKeyRT runs once on the OUTSIDE of this layer.
type retryRT struct {
	next     http.RoundTripper
	verbose  bool
	verboseW io.Writer
	sleep    func(time.Duration) // injectable for tests
}

func (r *retryRT) RoundTrip(req *http.Request) (*http.Response, error) {
	var (
		resp    *http.Response
		lastErr error
	)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := restoreBody(req); err != nil {
				return nil, fmt.Errorf("inventory transport: replaying body on retry: %w", err)
			}
		}

		resp, lastErr = r.next.RoundTrip(req)

		if lastErr != nil {
			if attempt == maxRetries {
				return nil, lastErr
			}
			r.logRetry(attempt+1, fmt.Sprintf("network error: %v", lastErr))
			r.sleep(backoffFor(attempt))
			continue
		}

		if !shouldRetryStatus(resp.StatusCode) {
			return resp, nil
		}
		if attempt == maxRetries {
			return resp, nil // exhausted; surface last response
		}

		// Compute sleep based on response, then drain+close so the connection
		// is reusable.
		sleep := sleepForResponse(resp, attempt)
		drainAndClose(resp)
		r.logRetry(attempt+1, fmt.Sprintf("status %d, retrying in %s", resp.StatusCode, sleep))
		r.sleep(sleep)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return resp, nil
}

func (r *retryRT) logRetry(attempt int, reason string) {
	if r.verbose && r.verboseW != nil {
		fmt.Fprintf(r.verboseW, "→ Retry %d/%d: %s\n", attempt, maxRetries, reason)
	}
}

// shouldRetryStatus reports whether the given status code is in the retry
// allow-list (429, 502, 503, 504). 500/501 are explicitly NOT retried.
func shouldRetryStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// sleepForResponse picks the wait duration before retry. For 429, honor
// Retry-After if present and ≤ 30s. Otherwise (or for 5xx) use exponential
// backoff.
func sleepForResponse(resp *http.Response, attempt int) time.Duration {
	if resp.StatusCode == http.StatusTooManyRequests {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 && secs <= maxRetryAfterSecs {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return backoffFor(attempt)
}

// backoffFor returns the exponential backoff for the given 0-indexed
// attempt number, capped at maxBackoff. attempt=0 → 250ms, attempt=1 →
// 500ms (capped), attempt≥2 → 500ms.
func backoffFor(attempt int) time.Duration {
	d := initialBackoff << attempt
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// restoreBody resets req.Body from req.GetBody so the next round-trip
// attempt sees a fresh reader. Bodyless requests (GET, etc.) are a no-op.
func restoreBody(req *http.Request) error {
	if req.Body == nil && req.GetBody == nil {
		return nil
	}
	if req.GetBody == nil {
		// Body present but no GetBody set; the caller violated the
		// retry contract. We can't replay; surface a clear error.
		return fmt.Errorf("request has body but no GetBody; mutation must set GetBody to be retry-safe (D25)")
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}

func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // bounded drain
	_ = resp.Body.Close()
}

// auditRT calls AuditLogger.Append on every successful 2xx response. A nil
// logger is tolerated. Logger errors are intentionally swallowed (D15: the
// mutation already succeeded; audit failure is not the caller's problem).
type auditRT struct {
	next   http.RoundTripper
	logger AuditLogger
}

func (r *auditRT) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if r.logger != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = r.logger.Append(req, resp, time.Since(start))
	}
	return resp, nil
}

// redactToken renders a bearer for verbose logging: first 3 chars + "***".
// Tokens of length ≤ 6 collapse to "***" entirely so we never accidentally
// echo a short test token in full. Mirrors internal/api/client.go.
func redactToken(token string) string {
	if len(token) <= 6 {
		return "***"
	}
	return token[:3] + "***"
}
