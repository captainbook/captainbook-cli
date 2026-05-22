package inventory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func int64Ptr(n int64) *int64 { return &n }

// TestUserMessages walks every typed error in the taxonomy and asserts both
// the developer-facing Error() and user-facing UserMessage() outputs. Adding
// a new typed error: append a row here.
func TestUserMessages(t *testing.T) {
	cases := []struct {
		name        string
		err         UserMessenger
		wantError   string
		wantMessage string
	}{
		{
			name:        "AuthError",
			err:         &AuthError{Message: "expired"},
			wantError:   "UNAUTHENTICATED: expired",
			wantMessage: "token expired or revoked. Run `ceebee config use <profile>` to switch, or refresh your token.",
		},
		{
			name:        "AuthError_no_message",
			err:         &AuthError{},
			wantError:   "UNAUTHENTICATED",
			wantMessage: "token expired or revoked. Run `ceebee config use <profile>` to switch, or refresh your token.",
		},
		{
			name: "AbilityMissingError",
			err: &AbilityMissingError{
				Needed: Write,
				Have:   Set{Read},
			},
			wantError: `token missing required ability "cli:write" (have [cli:read])`,
			wantMessage: "This command requires the \"cli:write\" ability, but your token doesn't have it.\n" +
				"Granted abilities: [cli:read]\n" +
				"Ask an admin to issue a token with the missing ability, " +
				"or switch profiles with --profile <name>.",
		},
		{
			name:        "NotFoundError",
			err:         &NotFoundError{ResourceType: "product", ID: "prod_42"},
			wantError:   "NOT_FOUND: product prod_42",
			wantMessage: "product prod_42 not found",
		},
		{
			name: "ValidationError_multi_field",
			err: &ValidationError{FieldErrors: map[string][]string{
				"capacity": {"must be at least 0"},
				"from":     {"is required", "must be a date"},
			}},
			wantError: "VALIDATION_FAILED (2 fields)",
			wantMessage: "validation failed:\n" +
				"  - capacity: must be at least 0\n" +
				"  - from: is required\n" +
				"  - from: must be a date",
		},
		{
			name:        "ValidationError_empty",
			err:         &ValidationError{},
			wantError:   "VALIDATION_FAILED (0 fields)",
			wantMessage: "validation failed",
		},
		{
			name:        "IdempotencyConflictError",
			err:         &IdempotencyConflictError{Key: "01HXY"},
			wantError:   "IDEMPOTENCY_CONFLICT: key=01HXY",
			wantMessage: "idempotency key 01HXY was already used with a different request body. Mint a new key (omit --idempotency-key) or use a fresh UUIDv7.",
		},
		{
			name:        "IdempotencyInProgressError",
			err:         &IdempotencyInProgressError{Key: "01HXY"},
			wantError:   "IDEMPOTENCY_IN_PROGRESS: key=01HXY",
			wantMessage: "idempotency key 01HXY is currently being processed. Try again in a moment.",
		},
		{
			name:        "IdempotencyUnknownError",
			err:         &IdempotencyUnknownError{Key: "01HXY"},
			wantError:   "IDEMPOTENCY_UNKNOWN: key=01HXY",
			wantMessage: "idempotency key 01HXY expired (server prunes stale keys every 5 min). Retry the command with a fresh key (omit --idempotency-key to auto-mint).",
		},
		{
			name:        "DiscountNotApplicableError",
			err:         &DiscountNotApplicableError{DiscountID: "disc_7", Reason: "minimum spend not met"},
			wantError:   "DISCOUNT_NOT_APPLICABLE: discount=disc_7 reason=minimum spend not met",
			wantMessage: "discount disc_7 cannot be applied: minimum spend not met",
		},
		{
			name:        "ResourceInUseError",
			err:         &ResourceInUseError{ResourceType: "category", RelatedType: "product"},
			wantError:   "RESOURCE_IN_USE: category blocked by product",
			wantMessage: "category cannot be deleted: product still references it",
		},
		{
			name:        "AvailabilityHasConfirmedBookingError_single",
			err:         &AvailabilityHasConfirmedBookingError{AvailabilityID: "av_42"},
			wantError:   "AVAILABILITY_HAS_CONFIRMED_BOOKING: availability=av_42",
			wantMessage: "availability av_42 cannot be deleted: it has a confirmed booking. Cancel or move the booking first.",
		},
		{
			name:        "AvailabilityHasConfirmedBookingError_bulk_with_sample",
			err:         &AvailabilityHasConfirmedBookingError{TotalBlocked: 7, SampleAvailabilityIDs: []string{"av_1", "av_2", "av_3"}},
			wantError:   "AVAILABILITY_HAS_CONFIRMED_BOOKING: total_blocked=7",
			wantMessage: "7 availability rows in the matched range have confirmed bookings; entire bulk-delete rejected. Sample blocking ids (up to 20): av_1, av_2, av_3. Cancel/move the bookings or narrow the range.",
		},
		{
			name:        "AvailabilityHasConfirmedBookingError_bulk_no_sample",
			err:         &AvailabilityHasConfirmedBookingError{TotalBlocked: 3},
			wantError:   "AVAILABILITY_HAS_CONFIRMED_BOOKING: total_blocked=3",
			wantMessage: "3 availability rows in the matched range have confirmed bookings; entire bulk-delete rejected. Cancel/move the bookings or narrow the range.",
		},
		{
			name:        "WorkflowNotEditableError_with_status_and_hint",
			err:         &WorkflowNotEditableError{Status: "active", Hint: "Deactivate first."},
			wantError:   "WORKFLOW_NOT_EDITABLE: status=active",
			wantMessage: "workflow is not editable (current status: active). Trigger and step writes require status ∈ {DRAFT, PAUSED}. Run `workflows deactivate <id>` first, or use shell PATCH (name/description/notify_on_fail/max_credits_per_run) which is allowed on ACTIVE.\n  hint: Deactivate first.",
		},
		{
			name:        "WorkflowNotEditableError_bare",
			err:         &WorkflowNotEditableError{},
			wantError:   "WORKFLOW_NOT_EDITABLE",
			wantMessage: "workflow is not editable. Trigger and step writes require status ∈ {DRAFT, PAUSED}. Run `workflows deactivate <id>` first, or use shell PATCH (name/description/notify_on_fail/max_credits_per_run) which is allowed on ACTIVE.",
		},
		{
			name: "WorkflowNotActivatableError_with_failures",
			err: &WorkflowNotActivatableError{
				Hint: "Fix the validation errors below and try activating again.",
				Errors: []WorkflowActivationFailure{
					{Code: "NO_TRIGGER", Message: "Workflow has no trigger step."},
					{Code: "INVALID_STEP_CONFIG", Message: "Step config invalid.", StepID: int64Ptr(42)},
				},
			},
			wantError: "WORKFLOW_NOT_ACTIVATABLE (2 failures)",
			wantMessage: "workflow cannot be activated:\n" +
				"  - NO_TRIGGER: Workflow has no trigger step.\n" +
				"  - INVALID_STEP_CONFIG (step 42): Step config invalid.\n" +
				"  hint: Fix the validation errors below and try activating again.",
		},
		{
			name:        "WorkflowNotActivatableError_empty",
			err:         &WorkflowNotActivatableError{Hint: "see logs"},
			wantError:   "WORKFLOW_NOT_ACTIVATABLE (0 failures)",
			wantMessage: "workflow cannot be activated\n  hint: see logs",
		},
		{
			name:        "PayloadTooLargeError",
			err:         &PayloadTooLargeError{ActualBytes: 12 * 1024 * 1024, MaxBytes: 10 * 1024 * 1024},
			wantError:   "PAYLOAD_TOO_LARGE: actual=12582912 max=10485760",
			wantMessage: "file is 12 MB; plan max is 10 MB",
		},
		{
			name:        "UnsupportedMediaTypeError",
			err:         &UnsupportedMediaTypeError{Got: "image/bmp", Allowed: []string{"image/jpeg", "image/png"}},
			wantError:   "UNSUPPORTED_MEDIA_TYPE: got=image/bmp allowed=[image/jpeg image/png]",
			wantMessage: "media type image/bmp not allowed; expected one of: image/jpeg, image/png",
		},
		{
			name:        "RateLimitError",
			err:         &RateLimitError{RetryAfter: 30 * time.Second},
			wantError:   "RATE_LIMITED: retry_after=30s",
			wantMessage: "rate limited; retry after 30s",
		},
		{
			name:        "ServerError",
			err:         &ServerError{Status: 503, RequestID: "req_01HXY"},
			wantError:   "server error: status=503 request_id=req_01HXY",
			wantMessage: "server error (status 503, request_id: req_01HXY); contact support if this persists",
		},
		{
			name: "ResponseDriftError",
			err: &ResponseDriftError{
				Status:   200,
				Body:     []byte("???"),
				ParseErr: errors.New("invalid character '?'"),
			},
			wantError:   "response drift: status=200 parse_err=invalid character '?'",
			wantMessage: "server returned an unexpected response shape (status 200). The CLI may be out of date — try upgrading. Underlying parse error: invalid character '?'",
		},
		{
			name:        "RawAPIError_with_code",
			err:         &RawAPIError{Code: "BOOKING_ALREADY_CANCELLED", Status: 409, Message: "Booking is already cancelled."},
			wantError:   "BOOKING_ALREADY_CANCELLED (status 409): Booking is already cancelled.",
			wantMessage: "BOOKING_ALREADY_CANCELLED: Booking is already cancelled.",
		},
		{
			name:        "RawAPIError_no_code",
			err:         &RawAPIError{Status: 418, Message: "I'm a teapot"},
			wantError:   "api error (status 418): I'm a teapot",
			wantMessage: "I'm a teapot",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.wantError {
				t.Errorf("Error() mismatch:\n  got: %q\n want: %q", got, tc.wantError)
			}
			if got := tc.err.UserMessage(); got != tc.wantMessage {
				t.Errorf("UserMessage() mismatch:\n  got: %q\n want: %q", got, tc.wantMessage)
			}
		})
	}
}

// TestValidationErrorDeterministicOrder asserts that field names sort
// alphabetically (so output is stable across map-iteration orders) and
// per-field messages preserve their slice order.
func TestValidationErrorDeterministicOrder(t *testing.T) {
	ve := &ValidationError{FieldErrors: map[string][]string{
		"zeta":  {"z1", "z2"},
		"alpha": {"a1"},
		"mu":    {"m1"},
	}}
	want := "validation failed:\n" +
		"  - alpha: a1\n" +
		"  - mu: m1\n" +
		"  - zeta: z1\n" +
		"  - zeta: z2"
	for i := 0; i < 5; i++ {
		if got := ve.UserMessage(); got != want {
			t.Fatalf("iteration %d: order non-deterministic\n got:\n%s\nwant:\n%s", i, got, want)
		}
	}
}

// TestParseError covers the realistic-body branches of the registry plus
// the three fallbacks (ServerError, ResponseDriftError, RawAPIError). Every
// returned error is also asserted to implement UserMessenger so we can't
// accidentally land a typed error that the cobra rendering path can't use.
func TestParseError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		{
			name:   "UNAUTHENTICATED maps to AuthError",
			status: 401,
			body: `{
				"meta": {"request_id":"req_X"},
				"error": {"code":"UNAUTHENTICATED","message":"Missing or invalid bearer token.","retriable":false}
			}`,
			check: func(t *testing.T, err error) {
				var ae *AuthError
				if !errors.As(err, &ae) {
					t.Fatalf("want *AuthError, got %T: %v", err, err)
				}
				if ae.Message != "Missing or invalid bearer token." {
					t.Errorf("wrong Message: %q", ae.Message)
				}
			},
		},
		{
			name:   "ABILITY_MISSING maps to *AbilityMissingError",
			status: 403,
			body: `{
				"meta": {"request_id":"req_Y"},
				"error": {
					"code":"ABILITY_MISSING",
					"message":"Token lacks required ability.",
					"retriable":false,
					"details": {"needed":"cli:write","have":["cli:read"]}
				}
			}`,
			check: func(t *testing.T, err error) {
				var am *AbilityMissingError
				if !errors.As(err, &am) {
					t.Fatalf("want *AbilityMissingError, got %T: %v", err, err)
				}
				if am.Needed != Write {
					t.Errorf("wrong Needed: %q", am.Needed)
				}
				if len(am.Have) != 1 || am.Have[0] != Read {
					t.Errorf("wrong Have: %v", am.Have)
				}
			},
		},
		{
			name:   "NOT_FOUND maps to NotFoundError",
			status: 404,
			body: `{
				"meta":{"request_id":"req_Z"},
				"error":{"code":"NOT_FOUND","message":"product not found","retriable":false,
				         "details":{"resource_type":"product","id":"prod_42"}}
			}`,
			check: func(t *testing.T, err error) {
				var nf *NotFoundError
				if !errors.As(err, &nf) {
					t.Fatalf("want *NotFoundError, got %T", err)
				}
				if nf.ResourceType != "product" || nf.ID != "prod_42" {
					t.Errorf("wrong fields: %+v", nf)
				}
			},
		},
		{
			name:   "VALIDATION_FAILED with flat details (spec example shape)",
			status: 422,
			body: `{
				"meta":{"request_id":"req_V"},
				"error":{
					"code":"VALIDATION_FAILED",
					"message":"The request body has invalid fields.",
					"retriable":false,
					"details":{
						"capacity":["The capacity must be at least 0."],
						"from":["The from field is required."]
					}
				}
			}`,
			check: func(t *testing.T, err error) {
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("want *ValidationError, got %T", err)
				}
				if len(ve.FieldErrors) != 2 {
					t.Fatalf("want 2 field errors, got %d: %+v", len(ve.FieldErrors), ve.FieldErrors)
				}
				if ve.FieldErrors["capacity"][0] != "The capacity must be at least 0." {
					t.Errorf("wrong capacity msg: %v", ve.FieldErrors["capacity"])
				}
			},
		},
		{
			name:   "VALIDATION_FAILED with nested field_errors detail",
			status: 422,
			body: `{
				"meta":{"request_id":"req_V"},
				"error":{
					"code":"VALIDATION_FAILED",
					"message":"bad",
					"retriable":false,
					"details":{"field_errors":{"name":["required"]}}
				}
			}`,
			check: func(t *testing.T, err error) {
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("want *ValidationError, got %T", err)
				}
				if got := ve.FieldErrors["name"]; len(got) != 1 || got[0] != "required" {
					t.Errorf("nested field_errors not extracted: %+v", ve.FieldErrors)
				}
			},
		},
		{
			name:   "IDEMPOTENCY_CONFLICT carries key",
			status: 409,
			body: `{
				"meta":{"request_id":"req_I"},
				"error":{"code":"IDEMPOTENCY_CONFLICT","message":"reused","retriable":false,
				         "details":{"key":"01HXY"}}
			}`,
			check: func(t *testing.T, err error) {
				var e *IdempotencyConflictError
				if !errors.As(err, &e) {
					t.Fatalf("want *IdempotencyConflictError, got %T", err)
				}
				if e.Key != "01HXY" {
					t.Errorf("wrong key: %q", e.Key)
				}
			},
		},
		{
			name:   "IDEMPOTENCY_IN_PROGRESS carries key",
			status: 409,
			body: `{"meta":{},"error":{"code":"IDEMPOTENCY_IN_PROGRESS","message":"x","retriable":true,
			         "details":{"key":"01HXY"}}}`,
			check: func(t *testing.T, err error) {
				var e *IdempotencyInProgressError
				if !errors.As(err, &e) {
					t.Fatalf("want *IdempotencyInProgressError, got %T", err)
				}
				if e.Key != "01HXY" {
					t.Errorf("wrong key: %q", e.Key)
				}
			},
		},
		{
			name:   "IDEMPOTENCY_UNKNOWN carries key",
			status: 409,
			body: `{"meta":{},"error":{"code":"IDEMPOTENCY_UNKNOWN","message":"x","retriable":false,
			         "details":{"key":"01HXY"}}}`,
			check: func(t *testing.T, err error) {
				var e *IdempotencyUnknownError
				if !errors.As(err, &e) {
					t.Fatalf("want *IdempotencyUnknownError, got %T", err)
				}
			},
		},
		{
			name:   "DISCOUNT_NOT_APPLICABLE carries discount_id and reason",
			status: 409,
			body: `{"meta":{},"error":{
				"code":"DISCOUNT_NOT_APPLICABLE",
				"message":"discount cannot be applied",
				"retriable":false,
				"details":{"discount_id":"disc_7","reason":"minimum spend not met"}
			}}`,
			check: func(t *testing.T, err error) {
				var e *DiscountNotApplicableError
				if !errors.As(err, &e) {
					t.Fatalf("want *DiscountNotApplicableError, got %T", err)
				}
				if e.DiscountID != "disc_7" || e.Reason != "minimum spend not met" {
					t.Errorf("wrong fields: %+v", e)
				}
			},
		},
		{
			name:   "DISCOUNT_NOT_APPLICABLE falls back to message when reason missing",
			status: 409,
			body: `{"meta":{},"error":{
				"code":"DISCOUNT_NOT_APPLICABLE",
				"message":"already redeemed",
				"retriable":false,
				"details":{"discount_id":"disc_7"}
			}}`,
			check: func(t *testing.T, err error) {
				var e *DiscountNotApplicableError
				if !errors.As(err, &e) {
					t.Fatalf("want *DiscountNotApplicableError, got %T", err)
				}
				if e.Reason != "already redeemed" {
					t.Errorf("expected fallback to message, got %q", e.Reason)
				}
			},
		},
		{
			name:   "RESOURCE_IN_USE",
			status: 409,
			body: `{"meta":{},"error":{
				"code":"RESOURCE_IN_USE","message":"category in use","retriable":false,
				"details":{"resource_type":"category","related_type":"product"}
			}}`,
			check: func(t *testing.T, err error) {
				var e *ResourceInUseError
				if !errors.As(err, &e) {
					t.Fatalf("want *ResourceInUseError, got %T", err)
				}
				if e.ResourceType != "category" || e.RelatedType != "product" {
					t.Errorf("wrong fields: %+v", e)
				}
			},
		},
		{
			name:   "AVAILABILITY_HAS_CONFIRMED_BOOKING (single delete shape)",
			status: 409,
			body: `{"meta":{},"error":{
				"code":"AVAILABILITY_HAS_CONFIRMED_BOOKING","message":"row has confirmed booking","retriable":false,
				"details":{"availability_id":"av_42"}
			}}`,
			check: func(t *testing.T, err error) {
				var e *AvailabilityHasConfirmedBookingError
				if !errors.As(err, &e) {
					t.Fatalf("want *AvailabilityHasConfirmedBookingError, got %T", err)
				}
				if e.AvailabilityID != "av_42" {
					t.Errorf("wrong AvailabilityID: %q", e.AvailabilityID)
				}
				if e.TotalBlocked != 0 || len(e.SampleAvailabilityIDs) != 0 {
					t.Errorf("bulk fields should be unset on single-delete shape: %+v", e)
				}
			},
		},
		{
			name:   "AVAILABILITY_HAS_CONFIRMED_BOOKING (bulk delete shape)",
			status: 409,
			body: `{"meta":{},"error":{
				"code":"AVAILABILITY_HAS_CONFIRMED_BOOKING","message":"7 rows have confirmed bookings","retriable":false,
				"details":{"total_blocked":7,"sample_availability_ids":["av_1","av_2","av_3"]}
			}}`,
			check: func(t *testing.T, err error) {
				var e *AvailabilityHasConfirmedBookingError
				if !errors.As(err, &e) {
					t.Fatalf("want *AvailabilityHasConfirmedBookingError, got %T", err)
				}
				if e.AvailabilityID != "" {
					t.Errorf("single-delete field should be unset on bulk shape: %q", e.AvailabilityID)
				}
				if e.TotalBlocked != 7 {
					t.Errorf("wrong TotalBlocked: %d", e.TotalBlocked)
				}
				if len(e.SampleAvailabilityIDs) != 3 {
					t.Errorf("wrong sample ids: %v", e.SampleAvailabilityIDs)
				}
			},
		},
		{
			name:   "WORKFLOW_NOT_EDITABLE carries status + hint",
			status: 409,
			body: `{"meta":{},"error":{
				"code":"WORKFLOW_NOT_EDITABLE","message":"not editable","hint":"Deactivate first.","retriable":false,
				"details":{"status":"active"}
			}}`,
			check: func(t *testing.T, err error) {
				var e *WorkflowNotEditableError
				if !errors.As(err, &e) {
					t.Fatalf("want *WorkflowNotEditableError, got %T", err)
				}
				if e.Status != "active" {
					t.Errorf("wrong Status: %q", e.Status)
				}
				if e.Hint != "Deactivate first." {
					t.Errorf("wrong Hint: %q", e.Hint)
				}
			},
		},
		{
			name:   "WORKFLOW_NOT_EDITABLE tolerates alternate workflow_status key",
			status: 409,
			body: `{"meta":{},"error":{
				"code":"WORKFLOW_NOT_EDITABLE","message":"x","retriable":false,
				"details":{"workflow_status":"active"}
			}}`,
			check: func(t *testing.T, err error) {
				var e *WorkflowNotEditableError
				if !errors.As(err, &e) {
					t.Fatalf("want *WorkflowNotEditableError, got %T", err)
				}
				if e.Status != "active" {
					t.Errorf("wrong Status from workflow_status fallback: %q", e.Status)
				}
			},
		},
		{
			name:   "WORKFLOW_NOT_ACTIVATABLE extracts errors[] with step_id",
			status: 422,
			body: `{"meta":{},"error":{
				"code":"WORKFLOW_NOT_ACTIVATABLE",
				"message":"Workflow cannot be activated.",
				"hint":"Fix the validation errors below and try activating again.",
				"retriable":false,
				"details":{"errors":[
					{"code":"NO_TRIGGER","message":"Workflow has no trigger step."},
					{"code":"INVALID_STEP_CONFIG","message":"Step config invalid.","step_id":42}
				]}
			}}`,
			check: func(t *testing.T, err error) {
				var e *WorkflowNotActivatableError
				if !errors.As(err, &e) {
					t.Fatalf("want *WorkflowNotActivatableError, got %T", err)
				}
				if len(e.Errors) != 2 {
					t.Fatalf("want 2 failures, got %d: %+v", len(e.Errors), e.Errors)
				}
				if e.Errors[0].Code != "NO_TRIGGER" || e.Errors[0].StepID != nil {
					t.Errorf("first failure mis-decoded: %+v", e.Errors[0])
				}
				if e.Errors[1].Code != "INVALID_STEP_CONFIG" || e.Errors[1].StepID == nil || *e.Errors[1].StepID != 42 {
					t.Errorf("second failure mis-decoded: %+v", e.Errors[1])
				}
				if e.Hint == "" {
					t.Error("Hint dropped")
				}
			},
		},
		{
			name:   "PAYLOAD_TOO_LARGE parses byte counts",
			status: 413,
			body: `{"meta":{},"error":{
				"code":"PAYLOAD_TOO_LARGE","message":"big","retriable":false,
				"details":{"actual_bytes":12582912,"max_bytes":10485760}
			}}`,
			check: func(t *testing.T, err error) {
				var e *PayloadTooLargeError
				if !errors.As(err, &e) {
					t.Fatalf("want *PayloadTooLargeError, got %T", err)
				}
				if e.ActualBytes != 12582912 || e.MaxBytes != 10485760 {
					t.Errorf("wrong byte fields: %+v", e)
				}
			},
		},
		{
			name:   "UNSUPPORTED_MEDIA_TYPE",
			status: 415,
			body: `{"meta":{},"error":{
				"code":"UNSUPPORTED_MEDIA_TYPE","message":"nope","retriable":false,
				"details":{"got":"image/bmp","allowed":["image/jpeg","image/png"]}
			}}`,
			check: func(t *testing.T, err error) {
				var e *UnsupportedMediaTypeError
				if !errors.As(err, &e) {
					t.Fatalf("want *UnsupportedMediaTypeError, got %T", err)
				}
				if e.Got != "image/bmp" || len(e.Allowed) != 2 {
					t.Errorf("wrong fields: %+v", e)
				}
			},
		},
		{
			name:   "RATE_LIMITED parses retry_after_seconds from body",
			status: 429,
			body: `{"meta":{},"error":{
				"code":"RATE_LIMITED","message":"slow down","retriable":true,
				"details":{"retry_after_seconds":42}
			}}`,
			check: func(t *testing.T, err error) {
				var e *RateLimitError
				if !errors.As(err, &e) {
					t.Fatalf("want *RateLimitError, got %T", err)
				}
				if e.RetryAfter != 42*time.Second {
					t.Errorf("wrong retry-after: %v", e.RetryAfter)
				}
			},
		},
		{
			name:   "INTERNAL_ERROR maps to ServerError",
			status: 500,
			body:   `{"meta":{"request_id":"req_S"},"error":{"code":"INTERNAL_ERROR","message":"boom","retriable":false}}`,
			check: func(t *testing.T, err error) {
				var e *ServerError
				if !errors.As(err, &e) {
					t.Fatalf("want *ServerError, got %T", err)
				}
				if e.Status != 500 || e.RequestID != "req_S" {
					t.Errorf("wrong fields: %+v", e)
				}
			},
		},
		{
			name:   "5xx with no recognized code maps to ServerError",
			status: 502,
			body:   `{"meta":{"request_id":"req_5"},"error":{"code":"BAD_GATEWAY","message":"upstream","retriable":true}}`,
			check: func(t *testing.T, err error) {
				var e *ServerError
				if !errors.As(err, &e) {
					t.Fatalf("want *ServerError, got %T: %v", err, err)
				}
				if e.RequestID != "req_5" {
					t.Errorf("expected request_id propagated, got %q", e.RequestID)
				}
			},
		},
		{
			name:   "5xx with unparseable body falls back to ServerError",
			status: 503,
			body:   `<html>oops</html>`,
			check: func(t *testing.T, err error) {
				var e *ServerError
				if !errors.As(err, &e) {
					t.Fatalf("want *ServerError, got %T: %v", err, err)
				}
				if e.Status != 503 {
					t.Errorf("wrong status: %d", e.Status)
				}
			},
		},
		{
			name:   "4xx with unknown code falls back to RawAPIError",
			status: 409,
			body:   `{"meta":{},"error":{"code":"BOOKING_ALREADY_CANCELLED","message":"already cancelled","retriable":false}}`,
			check: func(t *testing.T, err error) {
				var e *RawAPIError
				if !errors.As(err, &e) {
					t.Fatalf("want *RawAPIError, got %T: %v", err, err)
				}
				if e.Code != "BOOKING_ALREADY_CANCELLED" || e.Message != "already cancelled" {
					t.Errorf("wrong fields: %+v", e)
				}
			},
		},
		{
			name:   "4xx with unparseable body falls back to RawAPIError carrying body",
			status: 400,
			body:   `not json`,
			check: func(t *testing.T, err error) {
				var e *RawAPIError
				if !errors.As(err, &e) {
					t.Fatalf("want *RawAPIError, got %T: %v", err, err)
				}
				if !strings.Contains(e.Message, "not json") {
					t.Errorf("expected raw body in message, got %q", e.Message)
				}
			},
		},
		{
			name:   "2xx triggers ResponseDriftError (caller misuse)",
			status: 200,
			body:   `{"meta":{},"data":{}}`,
			check: func(t *testing.T, err error) {
				var e *ResponseDriftError
				if !errors.As(err, &e) {
					t.Fatalf("want *ResponseDriftError, got %T: %v", err, err)
				}
				if e.Status != 200 {
					t.Errorf("wrong status: %d", e.Status)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ParseError(tc.status, []byte(tc.body))
			if err == nil {
				t.Fatal("want error, got nil")
			}
			tc.check(t, err)
			// Every typed error in the inventory taxonomy must implement
			// UserMessenger so the cobra error handler can render it.
			if _, ok := err.(UserMessenger); !ok {
				t.Errorf("returned error %T does not implement UserMessenger", err)
			}
		})
	}
}

// TestParseRetryAfter covers both the delta-seconds and HTTP-date paths,
// plus the empty/garbage cases that should yield 0.
func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   time.Duration
		fuzzy  bool // for HTTP-date case where exact compare is brittle
	}{
		{name: "empty", header: "", want: 0},
		{name: "whitespace", header: "   ", want: 0},
		{name: "junk", header: "not a number", want: 0},
		{name: "seconds", header: "42", want: 42 * time.Second},
		{name: "zero seconds", header: "0", want: 0},
		{name: "negative seconds rejected", header: "-5", want: 0},
		{
			name:   "http date in the future",
			header: time.Now().Add(2 * time.Hour).UTC().Format(time.RFC1123),
			fuzzy:  true,
		},
		{
			name:   "http date in the past returns zero",
			header: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC1123),
			want:   0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseRetryAfter(tc.header)
			if tc.fuzzy {
				// We expect somewhere in the (1h, 3h) window to absorb the
				// RFC1123 second-resolution truncation and a tiny clock skew.
				if got < time.Hour || got > 3*time.Hour {
					t.Errorf("HTTP-date got %v, want roughly 2h", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWithRetryAfter ensures the helper folds Retry-After header data into
// a *RateLimitError without affecting other error types.
func TestWithRetryAfter(t *testing.T) {
	t.Run("sets retry on RateLimitError", func(t *testing.T) {
		base := &RateLimitError{}
		out := WithRetryAfter(base, 30*time.Second)
		var rl *RateLimitError
		if !errors.As(out, &rl) {
			t.Fatalf("expected *RateLimitError, got %T", out)
		}
		if rl.RetryAfter != 30*time.Second {
			t.Errorf("retry not applied: %v", rl.RetryAfter)
		}
	})
	t.Run("preserves existing retry when header is zero", func(t *testing.T) {
		base := &RateLimitError{RetryAfter: 5 * time.Second}
		out := WithRetryAfter(base, 0)
		var rl *RateLimitError
		_ = errors.As(out, &rl)
		if rl.RetryAfter != 5*time.Second {
			t.Errorf("body-side retry was clobbered: %v", rl.RetryAfter)
		}
	})
	t.Run("noop on other types", func(t *testing.T) {
		base := &AuthError{Message: "x"}
		out := WithRetryAfter(base, 10*time.Second)
		if out != base {
			t.Errorf("expected unchanged error, got %v", out)
		}
	})
}
