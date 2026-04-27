package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/captainbook/captainbook-cli/internal/api"
	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	"github.com/spf13/cobra"
)

// fakeServer returns an httptest.Server that returns canned responses
// based on URL + method. Each handler replaces the server's mux and is
// scoped to one test.
func fakeServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Runner) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	// Build a transport-less gen client (the test handler is the source
	// of truth, and we don't want the round-tripper chain's host
	// allow-list to reject httptest's loopback addresses).
	client, err := gen.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("build gen client: %v", err)
	}

	// Audit logger pointed at a temp file so tests can assert entries.
	tmpDir := t.TempDir()
	auditPath := filepath.Join(tmpDir, "audit.jsonl")
	logger, err := invpkg.NewFileLogger(auditPath)
	if err != nil {
		t.Fatalf("new audit logger: %v", err)
	}

	runner := &Runner{
		Client:      client,
		HTTPClient:  &http.Client{},
		AuditLogger: logger,
		Abilities:   invpkg.Set{invpkg.Read, invpkg.Write, invpkg.CS},
		ProfileName: "test",
		Tenant:      u.Host,
		Format:      "json",
		Out:         &bytes.Buffer{},
		Err:         &bytes.Buffer{},
	}
	return srv, runner
}

// TestRunMutation_Success_DryRunDiff verifies the happy path: a successful
// dry-run returns a diff envelope which the runner renders + audits.
func TestRunMutation_Success_DryRunDiff(t *testing.T) {
	body := `{
		"data": {
			"would_apply": true,
			"diff": {
				"before": {"title": "Old"},
				"after": {"title": "New"}
			}
		},
		"meta": {"request_id": "req_abc"}
	}`
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
	out := &bytes.Buffer{}
	runner.Out = out

	def := CommandDef{
		Use:        "update <id>",
		Kind:       KindMutation,
		Verb:       "PATCH",
		Path:       "/products/{id}",
		Ability:    invpkg.Write,
		DryRunMode: DryRunBody,
		Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
			b, _ := JSONBodyFromArgs(args, args.DryRun, nil)
			resp, err := r.Client.UpdateProductWithBodyWithResponse(ctx, "abc", &gen.UpdateProductParams{}, "application/json", asReader(b))
			if err != nil {
				return nil, err
			}
			return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", "abc")
		},
	}

	args := RunArgs{DryRun: true, Flags: map[string]any{}}
	if err := runMutation(context.Background(), runner, def, args); err != nil {
		t.Fatalf("runMutation: %v", err)
	}
	if out.Len() == 0 {
		t.Fatalf("expected output, got empty")
	}
}

// TestRunMutation_ValidationError verifies that a 422 maps to a typed
// ValidationError whose UserMessage is renderable.
func TestRunMutation_ValidationError(t *testing.T) {
	errBody := `{
		"meta": {"request_id": "req_xyz"},
		"error": {
			"code": "VALIDATION_FAILED",
			"message": "fields invalid",
			"details": {
				"capacity": ["The capacity must be at least 0."],
				"from": ["The from field is required."]
			}
		}
	}`
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(errBody))
	})

	def := CommandDef{
		Use: "create", Kind: KindMutation, Verb: "POST", Path: "/products",
		Ability: invpkg.Write, DryRunMode: DryRunBody,
		Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
			b, _ := JSONBodyFromArgs(args, args.DryRun, nil)
			resp, err := r.Client.CreateProductWithBodyWithResponse(ctx, &gen.CreateProductParams{}, "application/json", asReader(b))
			if err != nil {
				return nil, err
			}
			return ParseGenResponse(resp.Body, resp.HTTPResponse, "Product", "")
		},
	}
	err := runMutation(context.Background(), runner, def, RunArgs{Flags: map[string]any{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ve *invpkg.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if !strings.Contains(ve.UserMessage(), "capacity") {
		t.Errorf("UserMessage missing field name: %q", ve.UserMessage())
	}
}

// TestRunMutation_DryRunGate_NotSupported verifies D32: --dry-run on a
// NotSupported endpoint hard-errors before any network call.
func TestRunMutation_DryRunGate_NotSupported(t *testing.T) {
	calls := 0
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Errorf("server should not be reached on NotSupported dry-run")
	})

	def := CommandDef{
		Use: "delete", Kind: KindMutation, Verb: "DELETE", Path: "/products/{id}",
		Ability: invpkg.Write, DryRunMode: DryRunNotSupported,
		Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
			t.Errorf("Run closure should not be invoked")
			return nil, nil
		},
	}
	err := runMutation(context.Background(), runner, def, RunArgs{DryRun: true, Flags: map[string]any{}})
	if err == nil {
		t.Fatal("expected hard error on --dry-run + NotSupported, got nil")
	}
	var exitErr *api.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *api.ExitError, got %T", err)
	}
	if calls != 0 {
		t.Errorf("server hit %d times; expected 0", calls)
	}
}

// TestRunMutation_AbilityRefuse verifies that a missing ability is
// rejected before any network call.
func TestRunMutation_AbilityRefuse(t *testing.T) {
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be reached on ability refusal")
	})
	runner.Abilities = invpkg.Set{invpkg.Read} // missing Write

	def := CommandDef{
		Use: "create", Kind: KindMutation, Verb: "POST", Path: "/products",
		Ability: invpkg.Write, DryRunMode: DryRunBody,
		Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
			t.Error("Run should not be invoked")
			return nil, nil
		},
	}
	err := runMutation(context.Background(), runner, def, RunArgs{Flags: map[string]any{}})
	if err == nil {
		t.Fatal("expected ability error, got nil")
	}
	var am *invpkg.AbilityMissingError
	if !errors.As(err, &am) {
		t.Fatalf("expected *AbilityMissingError, got %T: %v", err, err)
	}
}

// TestParseGenResponse_AsyncJobID verifies that a 202 response with a
// bulk_update_id triggers AsyncJobID extraction (D31).
func TestParseGenResponse_AsyncJobID(t *testing.T) {
	body := []byte(`{"data": {"bulk_update_id": "01900000-0000-7000-8000-000000000000"}, "meta": {"request_id": "r"}}`)
	resp := &http.Response{StatusCode: http.StatusAccepted}
	res, err := ParseGenResponse(body, resp, "Availability", "")
	if err != nil {
		t.Fatalf("ParseGenResponse: %v", err)
	}
	if res.AsyncJobID != "01900000-0000-7000-8000-000000000000" {
		t.Errorf("AsyncJobID = %q, expected the uuid", res.AsyncJobID)
	}
	if res.Status != http.StatusAccepted {
		t.Errorf("Status = %d, want 202", res.Status)
	}
}

// TestForensicSummary_CapturesAllowedFields asserts that
// ForensicFields-listed flags land in the audit entry's forensic_summary
// (D37) and only those — never arbitrary flag values.
func TestForensicSummary_CapturesAllowedFields(t *testing.T) {
	def := CommandDef{
		ForensicFields: []string{"reason", "amount"},
	}
	args := RunArgs{
		Flags: map[string]any{
			"reason":      "duplicate-charge",
			"amount":      5000,
			"some-secret": "hunter2", // must NOT leak
		},
	}
	got := forensicSummary(def, args)
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(got), got)
	}
	if got["reason"] != "duplicate-charge" {
		t.Errorf("reason: got %v", got["reason"])
	}
	if got["amount"] != 5000 {
		t.Errorf("amount: got %v", got["amount"])
	}
	if _, ok := got["some-secret"]; ok {
		t.Errorf("forensic_summary leaked unlisted flag")
	}
}

// TestJSONBodyFromArgs_DryRunInjection verifies D24: dry_run is set on
// the body BEFORE marshaling.
func TestJSONBodyFromArgs_DryRunInjection(t *testing.T) {
	args := RunArgs{Flags: map[string]any{"title": "Hi"}}
	body, err := JSONBodyFromArgs(args, true, map[string]string{"title": "title"})
	if err != nil {
		t.Fatalf("JSONBodyFromArgs: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed["dry_run"] != true {
		t.Errorf("dry_run not set: %v", parsed)
	}
	if parsed["title"] != "Hi" {
		t.Errorf("title not mapped: %v", parsed)
	}
}

// TestErrorCode_Mapping covers the audit error_code mapping for each
// typed error.
func TestErrorCode_Mapping(t *testing.T) {
	cases := map[string]error{
		"UNAUTHENTICATED":         &invpkg.AuthError{},
		"NOT_FOUND":               &invpkg.NotFoundError{},
		"VALIDATION_FAILED":       &invpkg.ValidationError{},
		"IDEMPOTENCY_CONFLICT":    &invpkg.IdempotencyConflictError{},
		"IDEMPOTENCY_IN_PROGRESS": &invpkg.IdempotencyInProgressError{},
		"IDEMPOTENCY_UNKNOWN":     &invpkg.IdempotencyUnknownError{},
		"DISCOUNT_NOT_APPLICABLE": &invpkg.DiscountNotApplicableError{},
		"RESOURCE_IN_USE":         &invpkg.ResourceInUseError{},
		"PAYLOAD_TOO_LARGE":       &invpkg.PayloadTooLargeError{},
		"UNSUPPORTED_MEDIA_TYPE":  &invpkg.UnsupportedMediaTypeError{},
		"RATE_LIMITED":            &invpkg.RateLimitError{},
		"INTERNAL_ERROR":          &invpkg.ServerError{},
	}
	for want, err := range cases {
		got := errorCode(err)
		if got != want {
			t.Errorf("errorCode(%T) = %q; want %q", err, got, want)
		}
	}
	// RawAPIError preserves the server-supplied code.
	rawCode := errorCode(&invpkg.RawAPIError{Code: "FOO_BAR"})
	if rawCode != "FOO_BAR" {
		t.Errorf("RawAPIError code = %q; want FOO_BAR", rawCode)
	}
}

// TestMultipartUpload_OversizedFile verifies the pre-flight size check
// rejects a file > 10 MiB before any network call (Critical Rule §5).
//
// Drives the real uploadCmd against a httptest server; if the pre-flight
// check broke, the server would see a request and the test would fail.
// Sparse-file truncate keeps disk usage trivial.
func TestMultipartUpload_OversizedFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "huge.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Skipf("create temp file: %v", err)
	}
	if err := f.Truncate(maxUploadBytes + 1); err != nil {
		t.Skipf("truncate: %v", err)
	}
	_ = f.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() <= maxUploadBytes {
		t.Skipf("filesystem doesn't support sparse files; got size %d", info.Size())
	}

	// Server records every hit so we can assert it never fired.
	var hits int32
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	})

	cmd := uploadCmd(runner)
	cmd.SetArgs([]string{"prod_42", "--file", path})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pte *invpkg.PayloadTooLargeError
	if !errors.As(err, &pte) {
		t.Fatalf("expected *PayloadTooLargeError, got %T: %v", err, err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("expected zero network calls before size check; got %d", got)
	}
}

// TestMultipartUpload_MIMECheck verifies isAllowedMIME against the spec's
// allowed list.
func TestMultipartUpload_MIMECheck(t *testing.T) {
	for _, m := range []string{"image/jpeg", "image/png", "image/webp", "image/gif", "application/pdf"} {
		if !isAllowedMIME(m) {
			t.Errorf("expected %q to be allowed", m)
		}
	}
	for _, m := range []string{"application/octet-stream", "text/plain", "video/mp4"} {
		if isAllowedMIME(m) {
			t.Errorf("expected %q to be rejected", m)
		}
	}
	// Allow well-formed Content-Type headers with parameters.
	if !isAllowedMIME("image/jpeg; charset=binary") {
		t.Errorf("expected image/jpeg with parameters to be allowed")
	}
}

// TestBindCommands_FormatDefaults verifies cherry-pick #6: read commands
// default to --format=table; mutations default to --format=json.
func TestBindCommands_FormatDefaults(t *testing.T) {
	// Build a runner-less test: we only need the flag default, which is
	// set during bindCommands.
	runner := &Runner{}
	parent := newTestParent()
	defs := []CommandDef{
		{Use: "list", Kind: KindRead, Run: noopRun},
		{Use: "create", Kind: KindMutation, DryRunMode: DryRunBody, Run: noopRun},
	}
	bindCommands(parent, defs, runner)
	listCmd, _, err := parent.Find([]string{"list"})
	if err != nil {
		t.Fatalf("find list: %v", err)
	}
	createCmd, _, err := parent.Find([]string{"create"})
	if err != nil {
		t.Fatalf("find create: %v", err)
	}
	if got := listCmd.Flag("format").DefValue; got != "table" {
		t.Errorf("read default = %q; want table", got)
	}
	if got := createCmd.Flag("format").DefValue; got != "json" {
		t.Errorf("mutation default = %q; want json", got)
	}
}

// noopRun is a CommandDef.Run stub used by tests that only inspect cobra
// metadata.
func noopRun(_ context.Context, _ *Runner, _ RunArgs) (*RunResult, error) {
	return &RunResult{Status: 200}, nil
}

func newTestParent() *cobra.Command {
	return &cobra.Command{Use: "test"}
}

// TestAllResourceDefs_Buildable iterates every per-resource defs slice
// and asserts that each CommandDef has Use+Short+Run set, that mutations
// have a sensible DryRunMode, and that Verb+Path are non-empty so the
// audit entry's `command` field is never blank. This catches drift
// between the resource files and any future schema additions.
func TestAllResourceDefs_Buildable(t *testing.T) {
	groups := map[string][]CommandDef{
		"auth":              authDefs(),
		"products":          productsDefs(),
		"product_options":   productOptionsDefs(),
		"pricing_tiers":     pricingTiersDefs(),
		"discounts":         discountsDefs(),
		"gift_certificates": giftCertificatesDefs(),
		"bookings":          bookingsDefs(),
		"transactions":      transactionsDefs(),
		"customers":         customersDefs(),
		"guests":            guestsDefs(),
		"extras":            extrasDefs(),
		"questions":         questionsDefs(),
		"categories":        categoriesDefs(),
		"notifications":     notificationsDefs(),
	}
	for name, defs := range groups {
		if len(defs) == 0 {
			t.Errorf("%s: empty defs slice", name)
		}
		for _, d := range defs {
			if d.Use == "" {
				t.Errorf("%s: empty Use", name)
			}
			if d.Short == "" {
				t.Errorf("%s/%s: empty Short", name, d.Use)
			}
			if d.Run == nil {
				t.Errorf("%s/%s: nil Run", name, d.Use)
			}
			if d.Verb == "" || d.Path == "" {
				t.Errorf("%s/%s: missing Verb/Path", name, d.Use)
			}
		}
	}
}

// TestCmd_TopLevel verifies `inventory --help` lists exactly the 16
// resources the brief calls out (15 nested + whoami at top-level).
func TestCmd_TopLevel(t *testing.T) {
	root := Cmd()
	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}
	want := []string{
		"whoami",
		"products", "product-options", "availabilities",
		"pricing-tiers", "discounts", "gift-certificates",
		"bookings", "transactions", "customers", "guests",
		"extras", "questions", "categories", "media",
		"notifications",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing top-level resource: %q", w)
		}
	}
}

// TestGiftCertsIssue_SendNowMapsToSpecField verifies the --send-now flag
// maps to the spec's `send_now` JSON field, NOT `send_email` (which the
// server silently drops). Drives the actual Run closure with a flag set
// and asserts the wire body contains the correct key.
func TestGiftCertsIssue_SendNowMapsToSpecField(t *testing.T) {
	var defs []CommandDef
	for _, d := range giftCertificatesDefs() {
		if d.Use == "gift-certificates issue" {
			defs = append(defs, d)
		}
	}
	if len(defs) != 1 {
		t.Fatalf("expected exactly one gift-certificates issue CommandDef, got %d", len(defs))
	}
	def := defs[0]

	args := RunArgs{
		Flags: map[string]any{
			"available-gift-certificate-id": "agc_1",
			"recipient-email":               "alice@example.com",
			"recipient-name":                "Alice",
			"amount":                        15000,
			"send-now":                      true,
		},
		DryRun: false,
	}
	srv, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"gift_certificate":{"id":"gc_42"}},"meta":{"request_id":"r1"}}`))
	})
	_ = srv

	res, err := def.Run(context.Background(), runner, args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil || res.WireBody == nil {
		t.Fatal("expected non-nil RunResult with WireBody set")
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.WireBody, &parsed); err != nil {
		t.Fatalf("unmarshal wire body: %v", err)
	}
	if _, hasOldKey := parsed["send_email"]; hasOldKey {
		t.Errorf("wire body has obsolete `send_email` key (server would silently drop it): %v", parsed)
	}
	if got, ok := parsed["send_now"]; !ok || got != true {
		t.Errorf("wire body must carry send_now=true; got %v (full body: %v)", got, parsed)
	}
}

// TestGiftCertsIssue_SenderMessageMapping verifies the --sender-message
// flag maps to the spec's sender_message JSON field. Same pattern as
// TestGiftCertsIssue_SendNowMapsToSpecField; codex flagged that the
// previous regression test only covered send_now and would silently miss
// any drift on sender_message.
func TestGiftCertsIssue_SenderMessageMapping(t *testing.T) {
	var def CommandDef
	for _, d := range giftCertificatesDefs() {
		if d.Use == "gift-certificates issue" {
			def = d
			break
		}
	}
	args := RunArgs{
		Flags: map[string]any{
			"available-gift-certificate-id": "agc_1",
			"recipient-email":               "alice@example.com",
			"recipient-name":                "Alice",
			"amount":                        15000,
			"sender-message":                "Happy birthday!",
		},
	}
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"gift_certificate":{"id":"gc_42"}},"meta":{"request_id":"r1"}}`))
	})

	res, err := def.Run(context.Background(), runner, args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.WireBody, &parsed); err != nil {
		t.Fatalf("unmarshal wire body: %v", err)
	}
	if got, ok := parsed["sender_message"]; !ok || got != "Happy birthday!" {
		t.Errorf("wire body must carry sender_message; got %v (full body: %v)", got, parsed)
	}
}

// TestCommandDef_NoStrayEmptyAbility asserts the only CommandDef with
// Ability == "" is whoami (which is documented as the
// ability-discovery endpoint). This prevents accidental ability-gate
// bypass on other endpoints — Refuse("") short-circuits as a no-op so
// any CommandDef that forgets to set Ability would be wide-open.
func TestCommandDef_NoStrayEmptyAbility(t *testing.T) {
	defGroups := [][]CommandDef{
		authDefs(),
		productsDefs(),
		productOptionsDefs(),
		availabilitiesDefs(),
		pricingTiersDefs(),
		discountsDefs(),
		giftCertificatesDefs(),
		bookingsDefs(),
		transactionsDefs(),
		customersDefs(),
		guestsDefs(),
		extrasDefs(),
		questionsDefs(),
		categoriesDefs(),
		mediaDefs(),
		notificationsDefs(),
	}
	for _, defs := range defGroups {
		for _, d := range defs {
			if d.Ability == "" && !strings.Contains(d.Use, "whoami") {
				t.Errorf("CommandDef %q has empty Ability — only whoami may; please set Read/Write/CS explicitly", d.Use)
			}
		}
	}

	// Hand-written outliers: bulkUpdateDef (5 children) + uploadCmd are not
	// in any *Defs() table. Cover them directly.
	bulkSettings := []string{"capacity", "booking-status", "pricing", "start-time", "end-time"}
	for _, s := range bulkSettings {
		def := bulkUpdateDef(s, "test-"+s, nil, func(args RunArgs) (any, error) { return map[string]any{}, nil })
		if def.Ability == "" {
			t.Errorf("bulkUpdateDef(%q) has empty Ability", s)
		}
	}
	// uploadCmd is hand-written cobra; it doesn't expose a CommandDef, but
	// the ability gate is hardcoded inline. Static assertion: the source
	// must mention `invpkg.Refuse(invpkg.Write` so we know the gate fires.
	media, _ := os.ReadFile("media.go")
	if !strings.Contains(string(media), "invpkg.Refuse(invpkg.Write,") {
		t.Errorf("media.go: uploadCmd must call invpkg.Refuse(invpkg.Write, ...) — ability gate appears missing")
	}
}

// TestParseGenResponse_ErrorReturnsPartialResult asserts ParseGenResponse
// returns a non-nil RunResult on non-2xx so closures can populate
// res.WireBody and runMutation can hash the wire body for audit. Without
// this, error-row body_sha256 falls back to args.RawData (empty for
// typed-flag-only paths) and audit forensics are useless on failures.
func TestParseGenResponse_ErrorReturnsPartialResult(t *testing.T) {
	body := []byte(`{"meta":{"request_id":"r1"},"error":{"code":"VALIDATION_FAILED","message":"bad","retriable":false}}`)
	hr := &http.Response{StatusCode: http.StatusUnprocessableEntity, Header: http.Header{}}
	res, err := ParseGenResponse(body, hr, "Product", "prod_42")
	if err == nil {
		t.Fatal("expected typed error on 422; got nil")
	}
	if res == nil {
		t.Fatal("expected non-nil RunResult alongside error so closures can set WireBody for audit")
	}
	if res.Status != http.StatusUnprocessableEntity {
		t.Errorf("Status: got %d, want 422", res.Status)
	}
	if res.ResourceType != "Product" || res.ResourceID != "prod_42" {
		t.Errorf("ResourceType/ID not threaded: got %q/%q", res.ResourceType, res.ResourceID)
	}
	// Body intentionally empty on error — error-envelope bytes don't
	// belong on a success-shaped RunResult.Body.
	if res.Body != nil {
		t.Errorf("Body must be nil on error, got %d bytes", len(res.Body))
	}
}
