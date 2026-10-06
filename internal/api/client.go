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
	// basePath is appended to BaseURL (which must already end in
	// /api/v1/cli — same convention as the inventory client). The
	// resulting URL is /api/v1/cli/statistics/<endpoint>.
	basePath = "/statistics"
)

// Client is an HTTP client for the CaptainBook Statistics API.
type Client struct {
	// sleep waits out a retry backoff. Nil means the real clock; it exists so
	// tests can assert the backoff SCHEDULE instead of spending it, which they
	// previously did at a cost of ~16s of wall time per run. Unexported on
	// purpose: no production caller needs it, so it is not part of the API.
	sleep func(ctx context.Context, d time.Duration) error

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
	From        string
	To          string
	Granularity string
	CompareFrom string
	CompareTo   string

	// Extra carries every per-metric filter and extra flag, keyed by the spec's
	// own parameter name. business_unit_id and product_id used to have dedicated
	// fields here; they travel through Extra like every other filter now, because
	// which filters a metric accepts is a per-metric question and the gating for
	// it lives in cmd/stats.go. Keeping two fields meant two code paths to the
	// same two query keys, only one of them gated.
	Extra map[string]string
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
			if err := c.waitBackoff(ctx, backoff); err != nil {
				return nil, err
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
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		// 400 is the one that actually fires. The statistics routes sit under
		// `api/*`, where every validation failure renders as 400, and the
		// vendored spec declares no 422 at all — so handling only 422 sent every
		// refusal to the `default` arm below, where it surfaced as exit 18
		// "Unexpected API response" with a 200-char-truncated body instead of
		// exit 12 with the field names. 422 stays accepted because it costs
		// nothing and the inventory lane does use it.
		var valResp ValidationErrorResponse
		// Informative() is load-bearing: unmarshalling into a struct succeeds for
		// ANY JSON object, so without it a 400 whose shape we do not model became
		// ValidationError{"", nil} and printed the bare string "Validation error",
		// throwing away the server's sentence. A plan-gate refusal
		// (`{"error":"Statistics are not included in your subscription plan."}`)
		// is exactly that shape, and it is the most actionable 400 this API sends.
		if json.Unmarshal(body, &valResp) == nil && valResp.Informative() {
			return nil, &ValidationError{Message: valResp.Reason(), Errors: valResp.FieldErrors()}
		}
		// Nothing we model decoded. The status still says validation failure, so
		// the TYPE stays ValidationError — that is what maps to exit 12, and a
		// proxy returning an HTML 422 is still a validation failure. What must not
		// happen is losing the body: carry it as the message instead of the
		// contentless "validation failed", which printed as the bare string
		// "Validation error" and threw the server's response away.
		return nil, &ValidationError{Message: truncate(string(body), 200)}
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

// waitBackoff blocks for d, or returns a TimeoutError if the context ends
// first. It is the only place the retry loop touches the clock.
func (c *Client) waitBackoff(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return &TimeoutError{Duration: defaultTimeout.String()}
	case <-time.After(d):
		return nil
	}
}

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
