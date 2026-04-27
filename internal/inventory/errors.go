// Package inventory will host the cobra-driven Inventory CLI v1 surface.
//
// This file (errors.go) is Lane E of the parallelization plan and defines a
// parallel typed-error taxonomy for the Inventory CLI v1 API (D12). It is
// deliberately separate from internal/api/errors.go, which is the legacy
// stats-API taxonomy.
//
// Each typed error implements UserMessage() (D29) so the cobra error handler
// can render a crisp, user-facing string without leaking developer-oriented
// error formatting.
//
// ParseError converts an HTTP response (status + body) to a typed error. The
// mapping from the server's open-string `code` field to a Go type is
// hand-maintained in a registry (D34) — the spec calls `code` an open string
// with "examples", not an enum, so codegen can't help here. Adding an
// endpoint that introduces a new error code: define the typed error +
// UserMessage, add an entry to the registry in init(), and add a row to
// the table-driven test in errors_test.go.
//
// Error envelope (per spec components/schemas/ErrorEnvelope):
//
//	{
//	  "meta": { "request_id": "req_...", "api_version": "v1", ... },
//	  "error": {
//	    "code": "VALIDATION_FAILED",
//	    "message": "...",
//	    "hint": "...",
//	    "retriable": false,
//	    "details": { "<field>": ["msg", ...], ... }   // additionalProperties: true
//	  }
//	}
//
// Cross-lane coupling (D34 + Lane B):
//
//   - Lane B's abilities.go owns the canonical *AbilityMissingError type.
//   - Lane E (this file) is worktree-isolated from Lane B; we define a
//     placeholder AbilityMissingError here so this lane builds and tests in
//     isolation.
//   - MERGE RESOLUTION: when Lane B and Lane E land on the same branch,
//     delete the placeholder type below and switch the registry's
//     ABILITY_MISSING entry to construct *abilities.AbilityMissingError
//     (or, since abilities.go lives in this same `inventory` package on
//     the merged branch, just remove the local type and let the registry
//     reference the one from abilities.go directly). Tests in
//     errors_test.go assert the type via errors.As, so they keep passing
//     as long as the canonical type implements UserMessenger.
package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// UserMessenger is implemented by every typed inventory error. The cobra
// error handler type-asserts this interface to render the friendly string;
// callers that don't recognize the type fall back to err.Error().
type UserMessenger interface {
	error
	UserMessage() string
}

// errorEnvelope mirrors the spec's ErrorEnvelope schema for the fields we
// actually use. Other meta fields (api_version, generated_at, tenant_slug)
// are decoded but not retained — anything we may want later is recoverable
// from the raw body.
type errorEnvelope struct {
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
	Error struct {
		Code      string                     `json:"code"`
		Message   string                     `json:"message"`
		Hint      string                     `json:"hint,omitempty"`
		Retriable bool                       `json:"retriable"`
		Details   map[string]json.RawMessage `json:"details,omitempty"`
	} `json:"error"`
}

// -----------------------------------------------------------------------------
// 1. AuthError — UNAUTHENTICATED, 401
// -----------------------------------------------------------------------------

type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	if e.Message != "" {
		return "UNAUTHENTICATED: " + e.Message
	}
	return "UNAUTHENTICATED"
}

func (e *AuthError) UserMessage() string {
	return "token expired or revoked. Run `ceebee config use <profile>` to switch, or refresh your token."
}

// -----------------------------------------------------------------------------
// 2. AbilityMissingError — ABILITY_MISSING, 403
//
// PLACEHOLDER (Lane E isolation). On merge with Lane B this type goes away
// and the registry constructor returns Lane B's canonical type instead. See
// the package-level comment for the full merge-resolution recipe.
// -----------------------------------------------------------------------------

type AbilityMissingError struct {
	Needed string
	Have   []string
}

func (e *AbilityMissingError) Error() string {
	return fmt.Sprintf("ABILITY_MISSING: needed=%q have=%v", e.Needed, e.Have)
}

func (e *AbilityMissingError) UserMessage() string {
	return fmt.Sprintf(
		"this command requires `%s` but your token has %v",
		e.Needed, e.Have,
	)
}

// -----------------------------------------------------------------------------
// 3. NotFoundError — NOT_FOUND, 404
// -----------------------------------------------------------------------------

type NotFoundError struct {
	ResourceType string
	ID           string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("NOT_FOUND: %s %s", e.ResourceType, e.ID)
}

func (e *NotFoundError) UserMessage() string {
	return fmt.Sprintf("%s %s not found", e.ResourceType, e.ID)
}

// -----------------------------------------------------------------------------
// 4. ValidationError — VALIDATION_FAILED, 422 (also surfaces on 400)
//
// FieldErrors mirrors the spec's per-field details payload, e.g.:
//   { "capacity": ["The capacity must be at least 0."],
//     "from":     ["The from field is required."] }
// -----------------------------------------------------------------------------

type ValidationError struct {
	FieldErrors map[string][]string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("VALIDATION_FAILED (%d fields)", len(e.FieldErrors))
}

func (e *ValidationError) UserMessage() string {
	if len(e.FieldErrors) == 0 {
		return "validation failed"
	}
	// Sort field names so output is deterministic across map iterations.
	fields := make([]string, 0, len(e.FieldErrors))
	for f := range e.FieldErrors {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	var b strings.Builder
	b.WriteString("validation failed:")
	for _, f := range fields {
		for _, m := range e.FieldErrors[f] {
			b.WriteString("\n  - ")
			b.WriteString(f)
			b.WriteString(": ")
			b.WriteString(m)
		}
	}
	return b.String()
}

// -----------------------------------------------------------------------------
// 5. IdempotencyConflictError — IDEMPOTENCY_CONFLICT, 409
// -----------------------------------------------------------------------------

type IdempotencyConflictError struct {
	Key string
}

func (e *IdempotencyConflictError) Error() string {
	return fmt.Sprintf("IDEMPOTENCY_CONFLICT: key=%s", e.Key)
}

func (e *IdempotencyConflictError) UserMessage() string {
	return fmt.Sprintf(
		"idempotency key %s was already used with a different request body. "+
			"Mint a new key (omit --idempotency-key) or use a fresh UUIDv7.",
		e.Key,
	)
}

// -----------------------------------------------------------------------------
// 6. IdempotencyInProgressError — IDEMPOTENCY_IN_PROGRESS, 409
// -----------------------------------------------------------------------------

type IdempotencyInProgressError struct {
	Key string
}

func (e *IdempotencyInProgressError) Error() string {
	return fmt.Sprintf("IDEMPOTENCY_IN_PROGRESS: key=%s", e.Key)
}

func (e *IdempotencyInProgressError) UserMessage() string {
	return fmt.Sprintf(
		"idempotency key %s is currently being processed. Try again in a moment.",
		e.Key,
	)
}

// -----------------------------------------------------------------------------
// 7. IdempotencyUnknownError — IDEMPOTENCY_UNKNOWN, 409
// -----------------------------------------------------------------------------

type IdempotencyUnknownError struct {
	Key string
}

func (e *IdempotencyUnknownError) Error() string {
	return fmt.Sprintf("IDEMPOTENCY_UNKNOWN: key=%s", e.Key)
}

func (e *IdempotencyUnknownError) UserMessage() string {
	return fmt.Sprintf(
		"idempotency key %s expired (server prunes stale keys every 5 min). "+
			"The retry will mint a new key.",
		e.Key,
	)
}

// -----------------------------------------------------------------------------
// 8. DiscountNotApplicableError — DISCOUNT_NOT_APPLICABLE, 409
// -----------------------------------------------------------------------------

type DiscountNotApplicableError struct {
	DiscountID string
	Reason     string
}

func (e *DiscountNotApplicableError) Error() string {
	return fmt.Sprintf("DISCOUNT_NOT_APPLICABLE: discount=%s reason=%s", e.DiscountID, e.Reason)
}

func (e *DiscountNotApplicableError) UserMessage() string {
	return fmt.Sprintf("discount %s cannot be applied: %s", e.DiscountID, e.Reason)
}

// -----------------------------------------------------------------------------
// 9. ResourceInUseError — RESOURCE_IN_USE, 409
//
// The spec describes "Resource is still in use; detach references first"
// (e.g. deleting a category that still has products). The exact code string
// the server emits is hand-maintained here per D34; if/when it changes,
// update the registry mapping.
// -----------------------------------------------------------------------------

type ResourceInUseError struct {
	ResourceType string
	RelatedType  string
}

func (e *ResourceInUseError) Error() string {
	return fmt.Sprintf("RESOURCE_IN_USE: %s blocked by %s", e.ResourceType, e.RelatedType)
}

func (e *ResourceInUseError) UserMessage() string {
	return fmt.Sprintf(
		"%s cannot be deleted: %s still references it",
		e.ResourceType, e.RelatedType,
	)
}

// -----------------------------------------------------------------------------
// 10. PayloadTooLargeError — PAYLOAD_TOO_LARGE, 413 (multipart upload)
//
// Spec: 10 MiB cap by default; tenant plans may raise. ActualBytes/MaxBytes
// come from the server's details payload when present; if absent, the
// fields are 0 and UserMessage degrades to "0 MB; plan max is 0 MB".
// -----------------------------------------------------------------------------

type PayloadTooLargeError struct {
	ActualBytes int64
	MaxBytes    int64
}

func (e *PayloadTooLargeError) Error() string {
	return fmt.Sprintf("PAYLOAD_TOO_LARGE: actual=%d max=%d", e.ActualBytes, e.MaxBytes)
}

func (e *PayloadTooLargeError) UserMessage() string {
	const mib = int64(1024 * 1024)
	return fmt.Sprintf(
		"file is %d MB; plan max is %d MB",
		e.ActualBytes/mib, e.MaxBytes/mib,
	)
}

// -----------------------------------------------------------------------------
// 11. UnsupportedMediaTypeError — UNSUPPORTED_MEDIA_TYPE, 415
// -----------------------------------------------------------------------------

type UnsupportedMediaTypeError struct {
	Got     string
	Allowed []string
}

func (e *UnsupportedMediaTypeError) Error() string {
	return fmt.Sprintf("UNSUPPORTED_MEDIA_TYPE: got=%s allowed=%v", e.Got, e.Allowed)
}

func (e *UnsupportedMediaTypeError) UserMessage() string {
	return fmt.Sprintf(
		"media type %s not allowed; expected one of: %s",
		e.Got, strings.Join(e.Allowed, ", "),
	)
}

// -----------------------------------------------------------------------------
// 12. RateLimitError — RATE_LIMITED, 429
//
// RetryAfter is canonically sourced from the Retry-After response header
// (decoded by ParseRetryAfter and folded in via WithRetryAfter); the body
// may also carry retry_after_seconds, which the registry constructor
// honors as a fallback.
// -----------------------------------------------------------------------------

type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("RATE_LIMITED: retry_after=%s", e.RetryAfter)
}

func (e *RateLimitError) UserMessage() string {
	return fmt.Sprintf("rate limited; retry after %s", e.RetryAfter)
}

// -----------------------------------------------------------------------------
// 13. ServerError — 5xx without (or with unknown) code
// -----------------------------------------------------------------------------

type ServerError struct {
	Status    int
	RequestID string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("server error: status=%d request_id=%s", e.Status, e.RequestID)
}

func (e *ServerError) UserMessage() string {
	return fmt.Sprintf(
		"server error (status %d, request_id: %s); contact support if this persists",
		e.Status, e.RequestID,
	)
}

// -----------------------------------------------------------------------------
// 14. ResponseDriftError — parse failure on a success response (codegen drift)
//
// The transport layer (Lane A) calls this when a 2xx body fails to unmarshal
// into the codegen-emitted typed response. It signals "the server's response
// shape has drifted from the spec the CLI was built against" — almost always
// resolved by upgrading the CLI.
// -----------------------------------------------------------------------------

type ResponseDriftError struct {
	Status   int
	Body     []byte
	ParseErr error
}

func (e *ResponseDriftError) Error() string {
	return fmt.Sprintf("response drift: status=%d parse_err=%v", e.Status, e.ParseErr)
}

func (e *ResponseDriftError) Unwrap() error {
	return e.ParseErr
}

func (e *ResponseDriftError) UserMessage() string {
	return fmt.Sprintf(
		"server returned an unexpected response shape (status %d). "+
			"The CLI may be out of date — try upgrading. Underlying parse error: %v",
		e.Status, e.ParseErr,
	)
}

// -----------------------------------------------------------------------------
// 15. RawAPIError — fallback when code is set but unknown to our registry
//
// We never want to lose the server's message just because we haven't taught
// the CLI a new code yet. RawAPIError preserves both the code and the
// human-readable message; UserMessage just passes them through.
// -----------------------------------------------------------------------------

type RawAPIError struct {
	Code    string
	Status  int
	Message string
}

func (e *RawAPIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("api error (status %d): %s", e.Status, e.Message)
	}
	return fmt.Sprintf("%s (status %d): %s", e.Code, e.Status, e.Message)
}

func (e *RawAPIError) UserMessage() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// -----------------------------------------------------------------------------
// Registry: code → constructor (D34, hand-maintained).
//
// The constructor receives the parsed envelope and the HTTP status so it can
// pull whatever it needs out of details. Constructors must be defensive:
// the spec says details is `additionalProperties: true`, which means the
// shape varies by endpoint. Missing/typo'd keys yield zero values, never
// panics.
// -----------------------------------------------------------------------------

var registry map[string]func(status int, env errorEnvelope) error

func init() {
	registry = map[string]func(status int, env errorEnvelope) error{
		"UNAUTHENTICATED": func(status int, env errorEnvelope) error {
			return &AuthError{Message: env.Error.Message}
		},

		"ABILITY_MISSING": func(status int, env errorEnvelope) error {
			needed, _ := decodeStringField(env.Error.Details, "needed")
			have, _ := decodeStringSliceField(env.Error.Details, "have")
			return &AbilityMissingError{Needed: needed, Have: have}
		},

		"NOT_FOUND": func(status int, env errorEnvelope) error {
			rt, _ := decodeStringField(env.Error.Details, "resource_type")
			id, _ := decodeStringField(env.Error.Details, "id")
			return &NotFoundError{ResourceType: rt, ID: id}
		},

		"VALIDATION_FAILED": func(status int, env errorEnvelope) error {
			// The spec's example shows details directly carrying field-name
			// keys (capacity, from). But real responses sometimes nest under
			// `field_errors`. Try both, in priority order.
			if nested, ok := env.Error.Details["field_errors"]; ok {
				var fe map[string][]string
				if err := json.Unmarshal(nested, &fe); err == nil && fe != nil {
					return &ValidationError{FieldErrors: fe}
				}
			}
			fe := map[string][]string{}
			for k, raw := range env.Error.Details {
				var msgs []string
				if err := json.Unmarshal(raw, &msgs); err == nil {
					fe[k] = msgs
				}
			}
			return &ValidationError{FieldErrors: fe}
		},

		"IDEMPOTENCY_CONFLICT": func(status int, env errorEnvelope) error {
			key, _ := decodeStringField(env.Error.Details, "key")
			return &IdempotencyConflictError{Key: key}
		},

		"IDEMPOTENCY_IN_PROGRESS": func(status int, env errorEnvelope) error {
			key, _ := decodeStringField(env.Error.Details, "key")
			return &IdempotencyInProgressError{Key: key}
		},

		"IDEMPOTENCY_UNKNOWN": func(status int, env errorEnvelope) error {
			key, _ := decodeStringField(env.Error.Details, "key")
			return &IdempotencyUnknownError{Key: key}
		},

		"DISCOUNT_NOT_APPLICABLE": func(status int, env errorEnvelope) error {
			discountID, _ := decodeStringField(env.Error.Details, "discount_id")
			reason, _ := decodeStringField(env.Error.Details, "reason")
			if reason == "" {
				// Server may put the reason in the top-level message rather
				// than a structured detail; fall back so UserMessage stays
				// readable.
				reason = env.Error.Message
			}
			return &DiscountNotApplicableError{DiscountID: discountID, Reason: reason}
		},

		"RESOURCE_IN_USE": func(status int, env errorEnvelope) error {
			rt, _ := decodeStringField(env.Error.Details, "resource_type")
			rel, _ := decodeStringField(env.Error.Details, "related_type")
			return &ResourceInUseError{ResourceType: rt, RelatedType: rel}
		},

		"PAYLOAD_TOO_LARGE": func(status int, env errorEnvelope) error {
			actual, _ := decodeIntField(env.Error.Details, "actual_bytes")
			maxBytes, _ := decodeIntField(env.Error.Details, "max_bytes")
			return &PayloadTooLargeError{ActualBytes: actual, MaxBytes: maxBytes}
		},

		"UNSUPPORTED_MEDIA_TYPE": func(status int, env errorEnvelope) error {
			got, _ := decodeStringField(env.Error.Details, "got")
			allowed, _ := decodeStringSliceField(env.Error.Details, "allowed")
			return &UnsupportedMediaTypeError{Got: got, Allowed: allowed}
		},

		"RATE_LIMITED": func(status int, env errorEnvelope) error {
			// retry_after_seconds is the body-side convention; the canonical
			// source is the Retry-After header, which the transport layer
			// stitches in via WithRetryAfter after ParseError returns.
			secs, _ := decodeIntField(env.Error.Details, "retry_after_seconds")
			return &RateLimitError{RetryAfter: time.Duration(secs) * time.Second}
		},

		// INTERNAL_ERROR is mentioned in the spec's example list. We map it
		// to ServerError so callers get RequestID + status without a
		// special-case branch.
		"INTERNAL_ERROR": func(status int, env errorEnvelope) error {
			return &ServerError{Status: status, RequestID: env.Meta.RequestID}
		},
	}
}

// decodeStringField returns the string at key, or "" if absent or non-string.
func decodeStringField(d map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := d[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// decodeIntField returns the int64 at key, or 0 if absent or non-numeric.
func decodeIntField(d map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := d[key]
	if !ok {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

// decodeStringSliceField returns []string at key, or nil if absent / wrong type.
func decodeStringSliceField(d map[string]json.RawMessage, key string) ([]string, bool) {
	raw, ok := d[key]
	if !ok {
		return nil, false
	}
	var s []string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	return s, true
}

// ParseError converts an HTTP error response (status + body) to a typed
// error from the inventory taxonomy.
//
// Decision matrix:
//
//	2xx                                            → ResponseDriftError
//	                                                 (caller misuse: ParseError
//	                                                 should only be invoked
//	                                                 on non-success)
//	4xx/5xx + valid envelope + known code         → registered constructor
//	4xx/5xx + valid envelope + unknown code on 5xx → ServerError
//	4xx/5xx + valid envelope + unknown code on 4xx → RawAPIError (preserves
//	                                                 code + message)
//	4xx     + unparseable body                    → RawAPIError (raw body
//	                                                 as message)
//	5xx     + unparseable body                    → ServerError (no
//	                                                 request_id available)
func ParseError(status int, body []byte) error {
	// Defensive: callers shouldn't invoke us on 2xx, but if they do we want
	// to surface that as response drift rather than silently returning nil.
	if status >= 200 && status < 300 {
		return &ResponseDriftError{
			Status:   status,
			Body:     body,
			ParseErr: errors.New("ParseError invoked on a 2xx response"),
		}
	}

	var env errorEnvelope
	parseErr := json.Unmarshal(body, &env)

	if parseErr != nil {
		if status >= 500 {
			return &ServerError{Status: status, RequestID: ""}
		}
		// 4xx with junk body — preserve raw body as the message so the user
		// sees something rather than a generic "api error".
		return &RawAPIError{
			Status:  status,
			Message: strings.TrimSpace(string(body)),
		}
	}

	if ctor, ok := registry[env.Error.Code]; ok {
		return ctor(status, env)
	}

	if status >= 500 {
		return &ServerError{Status: status, RequestID: env.Meta.RequestID}
	}

	// Unknown 4xx code — pass through with whatever the server sent.
	return &RawAPIError{
		Code:    env.Error.Code,
		Status:  status,
		Message: env.Error.Message,
	}
}

// WithRetryAfter sets the RetryAfter on a *RateLimitError if err wraps one.
// The transport layer calls this after ParseError to fold the Retry-After
// header in. If err is not a RateLimitError, WithRetryAfter is a no-op
// returning err unchanged so callers can chain it unconditionally.
//
// A zero duration is treated as "no header data available" and leaves any
// body-derived RetryAfter intact.
func WithRetryAfter(err error, retryAfter time.Duration) error {
	var rl *RateLimitError
	if errors.As(err, &rl) {
		if retryAfter > 0 {
			rl.RetryAfter = retryAfter
		}
	}
	return err
}

// ParseRetryAfter decodes a Retry-After HTTP header value into a duration.
// Per RFC 7231 the value can be either delta-seconds or an HTTP-date; the
// inventory API emits seconds, but we accept HTTP-date too as a courtesy.
// Returns 0 if the header is empty or unparseable.
func ParseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := time.Parse(time.RFC1123, header); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
