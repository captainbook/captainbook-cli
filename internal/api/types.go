package api

import "encoding/json"

// ValidationErrorResponse matches the statistics API's validation-failure body.
//
// Two things about it are easy to get wrong, and both were wrong here until the
// spec was read rather than assumed:
//
//   - The status is 400, NOT 422. These routes sit under `api/*`, where the
//     application renders every validation failure as 400. The vendored
//     statistics spec contains no 422 anywhere.
//   - The per-field messages are under `details`, not `errors`. Laravel's
//     default key is `errors`, which is why `errors` looks right; this surface
//     does not use it, so decoding `errors` silently yields an empty map and the
//     user sees a bare message with no field names.
//
// Both matter most for the case the per-metric filter design is built on: a
// filter a metric does not implement is REFUSED, and the refusal names the
// endpoints that do apply it. That sentence arrives in `message`, and the
// offending parameter in `details`.
type ValidationErrorResponse struct {
	Message string `json:"message"`
	// Details is the statistics lane's per-field map.
	Details map[string][]string `json:"details"`
	// Errors is the conventional Laravel spelling. Kept so a surface that does
	// use it still populates ValidationError.Errors; Details wins when both are
	// present. Decoding both costs nothing and absorbs the difference.
	Errors map[string][]string `json:"errors"`
	// Error is the FLAT shape some admission gates answer with —
	// `{"error":"Statistics are not included in your subscription plan."}`. It
	// carries the whole reason in one string and no per-field map. Decoded as
	// json.RawMessage because the same key is an OBJECT on other surfaces, and a
	// typed string there would fail the unmarshal for the whole body.
	Error json.RawMessage `json:"error"`
}

// FieldErrors returns the per-field messages from whichever key carried them.
func (v ValidationErrorResponse) FieldErrors() map[string][]string {
	if len(v.Details) > 0 {
		return v.Details
	}
	return v.Errors
}

// Reason returns the human sentence to show, preferring `message` and falling
// back to a FLAT string `error`.
func (v ValidationErrorResponse) Reason() string {
	if v.Message != "" {
		return v.Message
	}
	var flat string
	if len(v.Error) > 0 && json.Unmarshal(v.Error, &flat) == nil {
		return flat
	}
	return ""
}

// Informative reports whether this body actually says anything. It exists because
// json.Unmarshal into a struct SUCCEEDS for any JSON object, so a 400 carrying a
// shape we do not model decodes without error into an entirely empty struct — and
// returning a ValidationError for that prints the bare words "Validation error"
// with the server's reason discarded. Worse than the untyped fallback it replaced,
// which at least echoed the body.
func (v ValidationErrorResponse) Informative() bool {
	return v.Reason() != "" || len(v.FieldErrors()) > 0
}

// ErrorResponse matches the API's generic error response shape.
type ErrorResponse struct {
	Success *bool  `json:"success,omitempty"`
	Message string `json:"message"`
}
