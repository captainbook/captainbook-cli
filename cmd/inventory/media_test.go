package inventory

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// mediaDefFor returns the single media CommandDef with the given Use.
func mediaDefFor(t *testing.T, use string) CommandDef {
	t.Helper()
	var found []CommandDef
	for _, d := range mediaDefs() {
		if d.Use == use {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one %q CommandDef, got %d", use, len(found))
	}
	return found[0]
}

// TestMediaList_SinceReachesQueryString covers the flag this sync ADDED, and
// pins the two things about it that are easy to get wrong.
//
// First, it has to reach the wire: `media list` is the odd one out among the
// list commands (it takes a positional product id and threads params as the
// SECOND argument to the gen call), so a param built into a struct that is
// then not passed is a plausible mistake — and the symptom is a full,
// unfiltered attachment list that looks like a legitimate answer.
//
// Second, `since` here bounds media_updated_at, not updated_at:
// product_attachments carries no Laravel timestamps, so the endpoint has a
// different Since type in the generated client than every other list. The
// value must still travel as an instant.
func TestMediaList_SinceReachesQueryString(t *testing.T) {
	def := mediaDefFor(t, "list <product-id>")

	var gotPath string
	var gotQuery url.Values
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"request_id":"req_1"}}`))
	})

	args := RunArgs{
		PathArgs: []string{"prod_7"},
		Flags:    map[string]any{"limit": 10, "since": "2026-08-01T00:00:00Z"},
	}
	if _, err := def.Run(context.Background(), runner, args); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotPath != "/products/prod_7/media" {
		t.Errorf("path = %q; want /products/prod_7/media", gotPath)
	}
	if got := gotQuery.Get("limit"); got != "10" {
		t.Errorf("limit = %q; want 10 (full query: %v)", got, gotQuery)
	}
	raw := gotQuery.Get("since")
	if raw == "" {
		t.Fatalf("since missing from the query (%v); an ignored filter returns the "+
			"whole attachment list, which reads as a valid answer", gotQuery)
	}
	got, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("since = %q, which is not RFC3339: %v", raw, err)
	}
	want, _ := time.Parse(time.RFC3339, "2026-08-01T00:00:00Z")
	if !got.Equal(want) {
		t.Errorf("since = %q (%v); want the same instant as %v", raw, got, want)
	}
}

// TestMediaList_RejectsInvalidSince covers the parse-error branch. The flag
// is documented as bounding media_updated_at "surfaced as the item's
// created_at", so a bare date is the natural first attempt; it has to fail
// with a message that names the flag rather than being sent as an empty or
// zero timestamp.
func TestMediaList_RejectsInvalidSince(t *testing.T) {
	def := mediaDefFor(t, "list <product-id>")

	called := false
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	})

	args := RunArgs{
		PathArgs: []string{"prod_7"},
		Flags:    map[string]any{"since": "2026-08-01"},
	}
	_, err := def.Run(context.Background(), runner, args)
	if err == nil {
		t.Fatal("expected --since 2026-08-01 to be rejected (no time component)")
	}
	if !strings.Contains(err.Error(), "--since") || !strings.Contains(err.Error(), "RFC3339") {
		t.Errorf("error = %q; want it to name --since and the expected format", err)
	}
	if called {
		t.Error("a malformed timestamp must fail before the request is sent")
	}
}
