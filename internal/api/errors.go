package api

import "fmt"

// Exit codes for different error types, enabling agents to distinguish failures.
const (
	ExitAuth       = 1
	ExitForbidden  = 2
	ExitValidation = 3
	ExitNetwork    = 4
	ExitJSONParse  = 5
	ExitConfig     = 6
	ExitServer     = 7
	ExitRateLimit  = 8
	ExitUnexpected = 9
)

type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Authentication failed: %s", e.Message)
	}
	return "Authentication failed: invalid or missing token"
}

type ForbiddenError struct {
	Message string
}

func (e *ForbiddenError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Access denied: %s", e.Message)
	}
	return "Access denied: requires view_reports permission"
}

type ValidationError struct {
	Message string
	Errors  map[string][]string
}

func (e *ValidationError) Error() string {
	if len(e.Errors) > 0 {
		msg := "Validation error:"
		for field, messages := range e.Errors {
			for _, m := range messages {
				msg += fmt.Sprintf("\n  %s: %s", field, m)
			}
		}
		return msg
	}
	if e.Message != "" {
		return fmt.Sprintf("Validation error: %s", e.Message)
	}
	return "Validation error"
}

type NetworkError struct {
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("Network error: %v", e.Err)
}

func (e *NetworkError) Unwrap() error {
	return e.Err
}

type TimeoutError struct {
	Duration string
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("Request timed out after %s", e.Duration)
}

type JSONParseError struct {
	Err error
}

func (e *JSONParseError) Error() string {
	return fmt.Sprintf("Failed to parse API response: %v", e.Err)
}

func (e *JSONParseError) Unwrap() error {
	return e.Err
}

type ServerError struct {
	StatusCode int
	Body       string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("Server error (%d). API may be unavailable.", e.StatusCode)
}

type RateLimitError struct {
	RetryAfter string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("Rate limited. Try again in %ss.", e.RetryAfter)
	}
	return "Rate limited. Try again later."
}

type UnexpectedStatusError struct {
	StatusCode int
	Body       string
}

func (e *UnexpectedStatusError) Error() string {
	return fmt.Sprintf("Unexpected API response: %d %s", e.StatusCode, e.Body)
}

// ExitCodeFor returns the exit code for a given error type.
func ExitCodeFor(err error) int {
	switch err.(type) {
	case *AuthError:
		return ExitAuth
	case *ForbiddenError:
		return ExitForbidden
	case *ValidationError:
		return ExitValidation
	case *NetworkError, *TimeoutError:
		return ExitNetwork
	case *JSONParseError:
		return ExitJSONParse
	case *ServerError:
		return ExitServer
	case *RateLimitError:
		return ExitRateLimit
	case *UnexpectedStatusError:
		return ExitUnexpected
	default:
		return 1
	}
}
