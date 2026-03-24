package api

// ValidationErrorResponse matches the API's 422 response shape.
type ValidationErrorResponse struct {
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
}

// ErrorResponse matches the API's generic error response shape.
type ErrorResponse struct {
	Success *bool  `json:"success,omitempty"`
	Message string `json:"message"`
}
