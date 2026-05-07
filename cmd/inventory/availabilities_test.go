package inventory

import (
	"context"
	"io"
	"net/http"
	"testing"
)

// TestDryRunBodyEditor_AttachesBodyAndHeaders verifies that the editor used
// by `availabilities delete <id> --dry-run` actually attaches the body to
// the gen-built DELETE request. The OpenAPI spec doesn't declare a request
// body on the DELETE, so codegen produces no *WithBody variant — without
// this editor, dry-run would silently send an empty DELETE and the server
// would treat it as a real soft-delete.
//
// The editor MUST also set GetBody so the transport's retry layer (D25)
// can replay the body on a 5xx; otherwise a single 5xx would corrupt the
// retried request.
func TestDryRunBodyEditor_AttachesBodyAndHeaders(t *testing.T) {
	body := []byte(`{"dry_run":true}`)
	editor := dryRunBodyEditor(body)

	req, err := http.NewRequest("DELETE", "https://example.test/availabilities/av_1", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := editor(context.Background(), req); err != nil {
		t.Fatalf("editor returned error: %v", err)
	}

	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", got)
	}
	if req.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d; want %d", req.ContentLength, len(body))
	}

	// Body is consumed once; assert contents.
	read, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(read) != string(body) {
		t.Errorf("body = %q; want %q", read, body)
	}

	// GetBody MUST return a fresh, readable copy so retries replay
	// correctly. Without this the transport's retryRT corrupts retries.
	if req.GetBody == nil {
		t.Fatal("GetBody is nil; retries would send empty body on replay")
	}
	rc, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody returned error: %v", err)
	}
	replay, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read replayed body: %v", err)
	}
	if string(replay) != string(body) {
		t.Errorf("replayed body = %q; want %q", replay, body)
	}
}
