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
// The canonical type lives in abilities.go (same package). The registry
// constructor below builds it from the server's error envelope.
// -----------------------------------------------------------------------------

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

// UserMessage names both ways a key can conflict. Since spec 1.2.0 a key is
// bound to the method and path of its first use as well as its body, so the
// most common cause is no longer a changed body — it's one key reused across
// two operations (often the same command against two different ids, where
// the bodies are identical and only the path differs).
func (e *IdempotencyConflictError) UserMessage() string {
	return fmt.Sprintf(
		"idempotency key %s was already used for a different request — either a "+
			"different body, or a different endpoint/resource (a key is bound to "+
			"the operation it was first used on). Use one key per operation: mint "+
			"a new one (omit --idempotency-key) or supply a fresh UUIDv7.",
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
			"Retry the command with a fresh key (omit --idempotency-key to auto-mint).",
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
// 9b. AvailabilityHasConfirmedBookingError — AVAILABILITY_HAS_CONFIRMED_BOOKING, 409
//
// Returned by `DELETE /availabilities/{id}` and `POST
// /availabilities/bulk-delete` when one or more matched rows have a
// confirmed Booking attached. The two endpoints surface different
// `details` shapes:
//
//   - single delete: { availability_id: "<echoes path id>" }
//   - bulk delete:   { total_blocked: <count>,
//                      sample_availability_ids: [<up to 20 ids>] }
//
// We accept both into one struct so callers don't need to distinguish:
// AvailabilityID is set only on the single-delete path; TotalBlocked +
// SampleAvailabilityIDs are set only on the bulk-delete path.
// -----------------------------------------------------------------------------

type AvailabilityHasConfirmedBookingError struct {
	AvailabilityID        string
	TotalBlocked          int64
	SampleAvailabilityIDs []string
}

func (e *AvailabilityHasConfirmedBookingError) Error() string {
	if e.AvailabilityID != "" {
		return fmt.Sprintf("AVAILABILITY_HAS_CONFIRMED_BOOKING: availability=%s", e.AvailabilityID)
	}
	return fmt.Sprintf("AVAILABILITY_HAS_CONFIRMED_BOOKING: total_blocked=%d", e.TotalBlocked)
}

func (e *AvailabilityHasConfirmedBookingError) UserMessage() string {
	if e.AvailabilityID != "" {
		return fmt.Sprintf(
			"availability %s cannot be deleted: it has a confirmed booking. Cancel or move the booking first.",
			e.AvailabilityID,
		)
	}
	if len(e.SampleAvailabilityIDs) == 0 {
		return fmt.Sprintf(
			"%d availability rows in the matched range have confirmed bookings; entire bulk-delete rejected. Cancel/move the bookings or narrow the range.",
			e.TotalBlocked,
		)
	}
	return fmt.Sprintf(
		"%d availability rows in the matched range have confirmed bookings; entire bulk-delete rejected. Sample blocking ids (up to 20): %s. Cancel/move the bookings or narrow the range.",
		e.TotalBlocked, strings.Join(e.SampleAvailabilityIDs, ", "),
	)
}

// -----------------------------------------------------------------------------
// 9c. WorkflowNotEditableError — WORKFLOW_NOT_EDITABLE, 409
//
// Returned by every /workflows/{id}/trigger and /workflows/{id}/steps* write
// when workflow.status=ACTIVE. The shell-only PATCH /workflows/{id} is NOT
// gated (those fields don't affect the executor). Carries the workflow's
// current status when the server includes it in details so the user knows
// whether to deactivate or accept that the workflow is already running.
// -----------------------------------------------------------------------------

type WorkflowNotEditableError struct {
	Status string // e.g. "active" — empty when server omits it
	Hint   string
}

func (e *WorkflowNotEditableError) Error() string {
	if e.Status != "" {
		return fmt.Sprintf("WORKFLOW_NOT_EDITABLE: status=%s", e.Status)
	}
	return "WORKFLOW_NOT_EDITABLE"
}

func (e *WorkflowNotEditableError) UserMessage() string {
	var b strings.Builder
	b.WriteString("workflow is not editable")
	if e.Status != "" {
		b.WriteString(" (current status: ")
		b.WriteString(e.Status)
		b.WriteString(")")
	}
	b.WriteString(". Trigger and step writes require status ∈ {DRAFT, PAUSED}. Run `workflows deactivate <id>` first, or use shell PATCH (name/description/notify_on_fail/max_credits_per_run) which is allowed on ACTIVE.")
	if e.Hint != "" {
		b.WriteString("\n  hint: ")
		b.WriteString(e.Hint)
	}
	return b.String()
}

// -----------------------------------------------------------------------------
// 9d. WorkflowNotActivatableError — WORKFLOW_NOT_ACTIVATABLE, 422
//
// Returned by POST /workflows/{id}/activate when WorkflowActivationValidator
// finds problems. The validator does NOT short-circuit — it returns ALL
// failures in one response so the user can fix the workflow in one editing
// pass. Each entry in Errors carries one validator code (NO_TRIGGER,
// NO_STEPS, ORPHAN_PARENT_REF, INVALID_STEP_CONFIG, INVALID_STEP_TYPE,
// CREDIT_LIMIT_EXCEEDED). Step-level failures include StepID.
// -----------------------------------------------------------------------------

// WorkflowActivationFailure is one entry in WorkflowNotActivatableError.Errors.
// StepID is non-nil only for step-level failures (INVALID_STEP_CONFIG,
// INVALID_STEP_TYPE, ORPHAN_PARENT_REF); the workflow-level failures
// (NO_TRIGGER, NO_STEPS, CREDIT_LIMIT_EXCEEDED) leave it nil.
type WorkflowActivationFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	StepID  *int64 `json:"step_id,omitempty"`
}

type WorkflowNotActivatableError struct {
	Hint   string
	Errors []WorkflowActivationFailure
}

func (e *WorkflowNotActivatableError) Error() string {
	return fmt.Sprintf("WORKFLOW_NOT_ACTIVATABLE (%d failures)", len(e.Errors))
}

func (e *WorkflowNotActivatableError) UserMessage() string {
	if len(e.Errors) == 0 {
		msg := "workflow cannot be activated"
		if e.Hint != "" {
			msg += "\n  hint: " + e.Hint
		}
		return msg
	}
	var b strings.Builder
	b.WriteString("workflow cannot be activated:")
	for _, f := range e.Errors {
		b.WriteString("\n  - ")
		b.WriteString(f.Code)
		if f.StepID != nil {
			b.WriteString(fmt.Sprintf(" (step %d)", *f.StepID))
		}
		if f.Message != "" {
			b.WriteString(": ")
			b.WriteString(f.Message)
		}
	}
	if e.Hint != "" {
		b.WriteString("\n  hint: ")
		b.WriteString(e.Hint)
	}
	return b.String()
}

// -----------------------------------------------------------------------------
// 9e. BookingResourceStateStaleError — BOOKING_RESOURCE_STATE_STALE, 409
//
// Returned by POST /bookings/{id}/resources when the
// `expected_resource_state_token` in the request no longer matches the
// booking's current aggregate resource state — i.e. someone (back office,
// another agent, a workflow) reassigned resources between the caller's read
// and its write. This is the optimistic-concurrency guard, not a validation
// failure: the fix is always "re-read, re-decide, re-send", never "retry the
// same body".
//
// The server may echo the two tokens in details; both are best-effort, so
// UserMessage degrades gracefully when they're absent.
// -----------------------------------------------------------------------------

type BookingResourceStateStaleError struct {
	Expected string // token the caller sent
	Current  string // token the booking actually has now
}

func (e *BookingResourceStateStaleError) Error() string {
	return fmt.Sprintf("BOOKING_RESOURCE_STATE_STALE: expected=%s current=%s", e.Expected, e.Current)
}

func (e *BookingResourceStateStaleError) UserMessage() string {
	var b strings.Builder
	b.WriteString("booking resources changed since you read them; the write was rejected to avoid clobbering the newer state.")
	if e.Expected != "" && e.Current != "" {
		b.WriteString(fmt.Sprintf("\n  sent token: %s\n  current token: %s", e.Expected, e.Current))
	}
	b.WriteString("\n  Re-read with `bookings get <id>` (or `bookings list --include resources`), confirm the assignment still makes sense, then resend with the fresh --expected-resource-state-token. Do NOT retry the same body.")
	return b.String()
}

// -----------------------------------------------------------------------------
// 9f. BookingResourceConflictError — BOOKING_RESOURCE_CONFLICT, 409
//
// Returned by POST /bookings/{id}/resources when the requested selection is
// invalid for this booking: the resource is double-booked at the slot, isn't
// attached to the booking's ProductOption, is soft-deleted, or is being used
// in the wrong slot (an auxiliary passed as `main_resource_id`, say).
//
// Distinct from STATE_STALE: the caller's view of the world was current, the
// selection itself is just not allowed. Re-reading won't help — pick a
// different resource, which the /resources/available endpoints enumerate.
//
// The server does not short-circuit: it plans the whole write and reports
// every refused id at once in `details.rejections[]`, so one round trip tells
// you everything to fix. Each entry's `code` is `<FIELD>_RESOURCE_<PROBLEM>`,
// where FIELD is the flag the id was sent in (MAIN / EQUIPMENT / AUXILIARY)
// rather than what the resource turned out to be — so the code names both
// halves of a wrong-field mistake.
// -----------------------------------------------------------------------------

// BookingResourceRejection is one entry in BookingResourceConflictError.
// Code is one of <FIELD>_RESOURCE_NOT_ON_PRODUCT_OPTION,
// <FIELD>_RESOURCE_IS_{MAIN,EQUIPMENT,AUXILIARY}, MAIN_RESOURCE_NOT_AVAILABLE
// or AUXILIARY_RESOURCE_NOT_AVAILABLE. There is no equipment "not available":
// equipment is never rationed per booking, so it is either on the option or it
// is not.
type BookingResourceRejection struct {
	ResourceID string
	Code       string
	Message    string
}

// UnmarshalJSON tolerates `resource_id` arriving as a JSON string (what the
// server sends today — it casts the integer id) or as a bare number, so a
// change on that one field can't silently drop the id from the message.
func (r *BookingResourceRejection) UnmarshalJSON(data []byte) error {
	var raw struct {
		ResourceID json.RawMessage `json:"resource_id"`
		Code       string          `json:"code"`
		Message    string          `json:"message"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Code, r.Message = raw.Code, raw.Message
	r.ResourceID = RawJSONIDToString(raw.ResourceID)
	return nil
}

type BookingResourceConflictError struct {
	Rejections []BookingResourceRejection
	// Message is the envelope's top-level message, rendered only when the
	// server sent no rejections — otherwise it is the generic "not valid"
	// sentence and the per-id entries say strictly more.
	Message string
}

func (e *BookingResourceConflictError) Error() string {
	return fmt.Sprintf("BOOKING_RESOURCE_CONFLICT (%d rejections)", len(e.Rejections))
}

func (e *BookingResourceConflictError) UserMessage() string {
	var b strings.Builder
	b.WriteString("requested resource assignment is not allowed")
	if len(e.Rejections) == 0 {
		if e.Message != "" {
			b.WriteString(": ")
			b.WriteString(e.Message)
		}
	} else {
		b.WriteString(":")
		for _, r := range e.Rejections {
			b.WriteString("\n  - ")
			if r.ResourceID != "" {
				b.WriteString("resource " + r.ResourceID + ": ")
			}
			b.WriteString(r.Code)
			if r.Message != "" {
				b.WriteString("\n      " + r.Message)
			}
		}
	}
	b.WriteString("\n  List valid candidates with `bookings available-resources <id>` (main), " +
		"`bookings available-equipment-resources <id>` (equipment) or " +
		"`bookings available-auxiliary-resources <id>` (auxiliary) and pick from those.")
	return b.String()
}

// -----------------------------------------------------------------------------
// 9g. TicketReissueNotConfirmedError — TICKET_REISSUE_NOT_CONFIRMED, 422
//
// Returned by PATCH /products/{id} when the request moves `delivery_method` on
// a product that has bookings. Committing would delete every ticket on every
// booking departing in the next 10 years and issue replacements, so the QR
// codes those customers already hold stop scanning — and nothing notifies
// them. The refusal is the confirmation modal the Ticketing form shows.
//
// `error.details` carries the same four keys the 200 returns under
// `ticket_reissue`, so one renderer covers the preview and the refusal.
//
// Raised before the write, which means it releases its Idempotency-Key: the
// retry carrying --confirm-ticket-reissue may reuse the same one.
// -----------------------------------------------------------------------------

type TicketReissueNotConfirmedError struct {
	From             string
	To               string
	AffectedBookings int64
	// CustomersNotified mirrors the same key the 200 returns. It is always
	// false today, but the message is derived from it rather than hardcoded:
	// if the server ever starts notifying, "you have to resend" becomes a lie
	// told at the exact moment the operator is deciding whether to proceed.
	CustomersNotified bool
	Hint              string
}

func (e *TicketReissueNotConfirmedError) Error() string {
	return fmt.Sprintf("TICKET_REISSUE_NOT_CONFIRMED: %s->%s bookings=%d", e.From, e.To, e.AffectedBookings)
}

func (e *TicketReissueNotConfirmedError) UserMessage() string {
	var b strings.Builder
	b.WriteString("changing the ticket type reissues tickets")
	if e.From != "" && e.To != "" {
		b.WriteString(fmt.Sprintf(" (%s -> %s)", e.From, e.To))
	}
	if e.AffectedBookings > 0 {
		b.WriteString(fmt.Sprintf(" for %d existing booking(s), invalidating the tickets or vouchers those customers already received", e.AffectedBookings))
	}
	b.WriteString(".")
	if !e.CustomersNotified {
		b.WriteString("\n  Customers are NOT notified — you have to resend their tickets yourself.")
	}
	if e.Hint != "" {
		b.WriteString("\n  hint: " + e.Hint)
	}
	b.WriteString("\n  Preview the blast radius with --dry-run (never refused), then resend with --confirm-ticket-reissue to go ahead. The refusal released the idempotency key, so the retry may reuse it.")
	return b.String()
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
			haveStrs, _ := decodeStringSliceField(env.Error.Details, "have")
			have := make(Set, 0, len(haveStrs))
			for _, s := range haveStrs {
				have = append(have, Ability(s))
			}
			return &AbilityMissingError{Needed: Ability(needed), Have: have}
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

		"AVAILABILITY_HAS_CONFIRMED_BOOKING": func(status int, env errorEnvelope) error {
			availabilityID, _ := decodeStringField(env.Error.Details, "availability_id")
			totalBlocked, _ := decodeIntField(env.Error.Details, "total_blocked")
			sampleIDs, _ := decodeStringSliceField(env.Error.Details, "sample_availability_ids")
			return &AvailabilityHasConfirmedBookingError{
				AvailabilityID:        availabilityID,
				TotalBlocked:          totalBlocked,
				SampleAvailabilityIDs: sampleIDs,
			}
		},

		"WORKFLOW_NOT_EDITABLE": func(status int, env errorEnvelope) error {
			// Server sometimes echoes the current workflow status in details
			// so the user knows whether deactivate is enough or the workflow
			// is already mid-run; tolerate either key shape.
			st, _ := decodeStringField(env.Error.Details, "status")
			if st == "" {
				st, _ = decodeStringField(env.Error.Details, "workflow_status")
			}
			return &WorkflowNotEditableError{Status: st, Hint: env.Error.Hint}
		},

		"WORKFLOW_NOT_ACTIVATABLE": func(status int, env errorEnvelope) error {
			failures, _ := decodeActivationFailures(env.Error.Details, "errors")
			return &WorkflowNotActivatableError{Hint: env.Error.Hint, Errors: failures}
		},

		"BOOKING_RESOURCE_STATE_STALE": func(status int, env errorEnvelope) error {
			// Tolerate both the verbose key shape and the short one; the spec
			// doesn't pin `details` for this code (additionalProperties: true).
			expected, _ := decodeStringField(env.Error.Details, "expected_resource_state_token")
			if expected == "" {
				expected, _ = decodeStringField(env.Error.Details, "expected")
			}
			current, _ := decodeStringField(env.Error.Details, "current_resource_state_token")
			if current == "" {
				current, _ = decodeStringField(env.Error.Details, "current")
			}
			return &BookingResourceStateStaleError{Expected: expected, Current: current}
		},

		"BOOKING_RESOURCE_CONFLICT": func(status int, env errorEnvelope) error {
			rejections, _ := decodeResourceRejections(env.Error.Details, "rejections")
			return &BookingResourceConflictError{
				Rejections: rejections,
				Message:    env.Error.Message,
			}
		},

		"TICKET_REISSUE_NOT_CONFIRMED": func(status int, env errorEnvelope) error {
			from, _ := decodeStringField(env.Error.Details, "delivery_method_from")
			to, _ := decodeStringField(env.Error.Details, "delivery_method_to")
			affected, _ := decodeIntField(env.Error.Details, "affected_bookings")
			notified, _ := decodeBoolField(env.Error.Details, "customers_notified")
			return &TicketReissueNotConfirmedError{
				From:              from,
				To:                to,
				AffectedBookings:  affected,
				CustomersNotified: notified,
				Hint:              env.Error.Hint,
			}
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

// decodeBoolField returns the bool at key, or false if absent / wrong type.
// Absent reads as false, which is the safe default here: it keeps the
// "customers are NOT notified" warning on rather than silently dropping it.
func decodeBoolField(d map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := d[key]
	if !ok {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

// decodeResourceRejections returns []BookingResourceRejection at key, or nil
// if absent / wrong type. Used by the BOOKING_RESOURCE_CONFLICT registry
// constructor to extract `details.rejections[]`.
func decodeResourceRejections(d map[string]json.RawMessage, key string) ([]BookingResourceRejection, bool) {
	raw, ok := d[key]
	if !ok {
		return nil, false
	}
	var rs []BookingResourceRejection
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil, false
	}
	return rs, true
}

// RawJSONIDToString converts a raw JSON id value to its string form,
// preserving precision for large int64s that decoding through `any` would
// lossily round-trip via float64. Strings come back unquoted; numbers come
// back as their literal digits; null / empty / objects / arrays return "".
//
// Exported because both sides of the wire need it: response parsing in
// cmd/inventory pulls ids out of envelopes, and the BOOKING_RESOURCE_CONFLICT
// decoder below reads `rejections[].resource_id`, which the server sends as a
// quoted string but which must not silently vanish if it ever arrives bare.
// cmd/inventory imports this package, so this is the only direction the shared
// helper can live in.
func RawJSONIDToString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	// Strings: "abc" -> abc (let json.Unmarshal handle escape sequences).
	if s[0] == '"' {
		var unq string
		if err := json.Unmarshal(raw, &unq); err != nil {
			return ""
		}
		return unq
	}
	// Objects / arrays: not a usable scalar id.
	if s[0] == '{' || s[0] == '[' {
		return ""
	}
	// Numbers (or bare tokens like `true`/`false`, returned as their literal
	// form rather than silently dropped).
	return s
}

// decodeActivationFailures returns []WorkflowActivationFailure at key, or nil
// if absent / wrong type. Used by the WORKFLOW_NOT_ACTIVATABLE registry
// constructor to extract `details.errors[]`.
func decodeActivationFailures(d map[string]json.RawMessage, key string) ([]WorkflowActivationFailure, bool) {
	raw, ok := d[key]
	if !ok {
		return nil, false
	}
	var fs []WorkflowActivationFailure
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, false
	}
	return fs, true
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
