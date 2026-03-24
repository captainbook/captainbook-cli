package api

import "encoding/json"

// StatisticsResponse is the common envelope returned by all statistics endpoints.
type StatisticsResponse struct {
	Meta       Meta                `json:"meta"`
	Data       json.RawMessage     `json:"data"`
	Series     json.RawMessage     `json:"series,omitempty"`
	Comparison *ComparisonResponse `json:"comparison"`
}

type Meta struct {
	Period      Period          `json:"period"`
	Granularity string          `json:"granularity"`
	Currency    string          `json:"currency"`
	Filters     json.RawMessage `json:"filters,omitempty"`
	GeneratedAt string          `json:"generated_at"`
}

type Period struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type ComparisonResponse struct {
	Period Period              `json:"period"`
	Data   json.RawMessage     `json:"data"`
	Series json.RawMessage     `json:"series,omitempty"`
	Deltas map[string]Delta    `json:"deltas"`
}

type Delta struct {
	Absolute   float64  `json:"absolute"`
	Percentage *float64 `json:"percentage"`
}

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
