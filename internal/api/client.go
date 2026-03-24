package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	maxRetries     = 2
	basePath       = "/api/v1/statistics"
)

// Client is an HTTP client for the CaptainBook Statistics API.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Verbose    bool
	VerboseW   io.Writer // stderr for verbose output
}

// NewClient creates a new API client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: defaultTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // don't follow redirects
			},
		},
	}
}

// QueryParams holds the query parameters for a statistics request.
type QueryParams struct {
	From           string
	To             string
	Granularity    string
	BusinessUnitID int
	ProductID      int
	CompareFrom    string
	CompareTo      string
	Extra          map[string]string
}

// Do makes a GET request to the given endpoint path with the given query parameters.
// It returns the raw JSON response body, or a typed error.
func (c *Client) Do(ctx context.Context, endpoint *Endpoint, params *QueryParams) ([]byte, error) {
	reqURL, err := c.buildURL(endpoint, params)
	if err != nil {
		return nil, &NetworkError{Err: fmt.Errorf("building URL: %w", err)}
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := retryBackoff(lastErr, attempt)
			if c.Verbose && c.VerboseW != nil {
				fmt.Fprintf(c.VerboseW, "→ Retry %d/%d in %s\n", attempt, maxRetries, backoff)
			}
			select {
			case <-ctx.Done():
				return nil, &TimeoutError{Duration: defaultTimeout.String()}
			case <-time.After(backoff):
			}
		}

		body, err := c.doRequest(ctx, reqURL)
		if err == nil {
			return body, nil
		}

		// Only retry transient errors
		if isRetriable(err) {
			lastErr = err
			continue
		}
		return nil, err
	}
	return nil, lastErr
}

func (c *Client) doRequest(ctx context.Context, reqURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &NetworkError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	start := time.Now()

	if c.Verbose && c.VerboseW != nil {
		fmt.Fprintf(c.VerboseW, "→ GET %s\n", reqURL)
		fmt.Fprintf(c.VerboseW, "→ Authorization: Bearer %s\n", redactToken(c.Token))
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &TimeoutError{Duration: defaultTimeout.String()}
		}
		return nil, &NetworkError{Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB max
	if err != nil {
		return nil, &NetworkError{Err: fmt.Errorf("reading response: %w", err)}
	}

	elapsed := time.Since(start)
	if c.Verbose && c.VerboseW != nil {
		fmt.Fprintf(c.VerboseW, "← %s (%s, %s)\n", resp.Status, elapsed.Round(time.Millisecond), formatBytes(len(body)))
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusUnauthorized:
		var errResp ErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
			return nil, &AuthError{Message: errResp.Message}
		}
		return nil, &AuthError{}
	case http.StatusForbidden:
		var errResp ErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
			return nil, &ForbiddenError{Message: errResp.Message}
		}
		return nil, &ForbiddenError{}
	case http.StatusUnprocessableEntity:
		var valResp ValidationErrorResponse
		if json.Unmarshal(body, &valResp) == nil {
			return nil, &ValidationError{Message: valResp.Message, Errors: valResp.Errors}
		}
		return nil, &ValidationError{Message: "validation failed"}
	case http.StatusTooManyRequests:
		retryAfter := resp.Header.Get("Retry-After")
		return nil, &RateLimitError{RetryAfter: retryAfter}
	default:
		if resp.StatusCode >= 500 {
			return nil, &ServerError{StatusCode: resp.StatusCode, Body: string(body)}
		}
		return nil, &UnexpectedStatusError{StatusCode: resp.StatusCode, Body: truncate(string(body), 200)}
	}
}

func (c *Client) buildURL(endpoint *Endpoint, params *QueryParams) (string, error) {
	u, err := url.Parse(c.BaseURL + basePath + endpoint.Path)
	if err != nil {
		return "", err
	}

	q := u.Query()

	if params.From != "" {
		q.Set("from", params.From)
	}
	if params.To != "" {
		q.Set("to", params.To)
	}
	if params.Granularity != "" {
		q.Set("granularity", params.Granularity)
	}
	if params.BusinessUnitID > 0 && !endpoint.HasExcludedFlag("business_unit_id") {
		q.Set("business_unit_id", strconv.Itoa(params.BusinessUnitID))
	}
	if params.ProductID > 0 && !endpoint.HasExcludedFlag("product_id") {
		q.Set("product_id", strconv.Itoa(params.ProductID))
	}
	if params.CompareFrom != "" {
		q.Set("compare_from", params.CompareFrom)
	}
	if params.CompareTo != "" {
		q.Set("compare_to", params.CompareTo)
	}

	for k, v := range params.Extra {
		if v != "" {
			// Convert CLI flag names (kebab-case) to API param names (snake_case)
			apiKey := strings.ReplaceAll(k, "-", "_")
			q.Set(apiKey, v)
		}
	}

	u.RawQuery = q.Encode()
	return u.String(), nil
}

func isRetriable(err error) bool {
	switch err.(type) {
	case *RateLimitError, *ServerError, *TimeoutError:
		return true
	case *NetworkError:
		return true
	default:
		return false
	}
}

const maxRetryAfter = 60 // seconds

func retryBackoff(lastErr error, attempt int) time.Duration {
	if rl, ok := lastErr.(*RateLimitError); ok && rl.RetryAfter != "" {
		if secs, err := strconv.Atoi(rl.RetryAfter); err == nil && secs > 0 {
			if secs > maxRetryAfter {
				secs = maxRetryAfter
			}
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(1<<(attempt-1)) * time.Second
}

func redactToken(token string) string {
	if len(token) <= 6 {
		return "***"
	}
	return token[:3] + "***"
}

func formatBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
