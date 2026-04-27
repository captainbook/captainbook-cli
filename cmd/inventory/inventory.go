// Package inventory wires the generated CLI v1 client into per-resource
// cobra commands. It is the converge step (Lane H of the parallelization
// plan) that consumes Lane A (transport), Lane B (abilities), Lane C
// (audit), Lane D (diff), and Lane E (errors).
//
// This file owns the shared infrastructure:
//
//   - CommandDef: declarative description of one cobra command.
//   - Runner: per-invocation runtime context (gen client, audit, abilities).
//   - runRead / runMutation: orchestration helpers shared by every resource.
//   - bindCommands: walks []CommandDef and produces cobra commands.
//   - Cmd(): the parent `inventory` command, wired into root in cmd/root.go.
//
// runMutation orchestration (D24, D32, D37):
//
//	+-------------------------------+
//	|  ability preflight (Refuse)   |  401-equivalent before network
//	+-------------------------------+
//	              v
//	+-------------------------------+
//	|  --dry-run gate (D32)         |  hard error on NotSupported endpoints
//	+-------------------------------+
//	              v
//	+-------------------------------+
//	|  CommandDef.Run               |  closure builds typed *Request,
//	|  → typed gen client method    |  sets body.DryRun = ptr(true) (D24),
//	|  → ParseGenResponse           |  invokes gen.UpdateProductWithResponse
//	+-------------------------------+
//	              v
//	+-------------------------------+
//	|  audit.Append (D37)           |  forensic_summary built from
//	|  forensic_summary, hash, ...  |  CommandDef.ForensicFields
//	+-------------------------------+
//	              v
//	+-------------------------------+
//	|  renderResult                 |  table → diff renderer; json → raw
//	+-------------------------------+
//
// The 16 resource files (auth.go, products.go, …) declare per-resource
// []CommandDef tables; everything below is plumbing.
package inventory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/captainbook/captainbook-cli/internal/api"
	"github.com/captainbook/captainbook-cli/internal/config"
	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	"github.com/captainbook/captainbook-cli/internal/output"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// DryRunMode declares whether/how a command supports --dry-run (D32).
type DryRunMode int

const (
	// DryRunNotSupported means this endpoint cannot be dry-run. --dry-run
	// is a hard error before any network call.
	DryRunNotSupported DryRunMode = iota
	// DryRunBody means dry_run is set as a field on the JSON request body.
	DryRunBody
	// DryRunQuery means dry_run is sent as a query-string parameter.
	DryRunQuery
)

// CommandKind distinguishes read vs mutation commands. It drives the
// per-command-kind format default (cherry-pick #6: reads default to table,
// mutations to json) and selects the orchestration helper.
type CommandKind int

const (
	// KindRead is GET-only. No audit, no dry-run, default --format=table.
	KindRead CommandKind = iota
	// KindMutation is any non-GET. Audited, may be dry-run, default --format=json.
	KindMutation
)

// CommandDef declares one cobra command.
//
// Each resource file (e.g. products.go) builds a []CommandDef and passes it
// to bindCommands(parent, defs, runner). bindCommands walks the slice and
// produces cobra commands wired to runRead/runMutation.
type CommandDef struct {
	// Cobra metadata.
	Use     string // "list", "create", "update <id>", …
	Short   string
	Long    string
	Example string

	// Kind selects KindRead or KindMutation. Drives the format default and
	// whether runMutation (with audit + ability gate) or runRead is used.
	Kind CommandKind

	// Verb / Path describe the HTTP method + spec path. Used for audit.
	Verb string
	Path string

	// Ability is the token capability the runner enforces via
	// abilities.Refuse before invoking Run. Read commands typically use
	// invpkg.Read; mutations use invpkg.Write or invpkg.CS for refund/comp.
	Ability invpkg.Ability

	// DryRunMode: per-endpoint dry-run support (D32).
	DryRunMode DryRunMode

	// ForensicFields is the (kebab-case) flag-name allow-list captured into
	// the audit entry's forensic_summary (D37). For most commands the
	// flag-name list is identical to the API field-name list.
	ForensicFields []string

	// Run is the per-command function. It receives the orchestration
	// context (Runner) plus parsed flags + path args, and is expected to
	// invoke the generated client method, return the parsed response, and
	// surface a typed error from internal/inventory.ParseError when the
	// HTTP call fails.
	//
	// Run is called AFTER ability preflight + dry-run gate, so by the time
	// it runs all upstream checks have passed. For mutations, Run must
	// honor args.DryRun (the helper already validated it's allowed).
	Run func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error)

	// Flags is per-command flag declarations. bindCommands installs each
	// FlagDef onto the cobra command before RunE fires. Common flags
	// (--dry-run, --format, --idempotency-key, --data) are added
	// automatically based on Kind / DryRunMode.
	Flags []FlagDef

	// PositionalArgs declares required positional arguments (e.g.
	// ["id"] for `update <id>`). bindCommands sets cobra's Args validator
	// accordingly and surfaces values via RunArgs.PathArgs.
	PositionalArgs []string
}

// FlagDef declares one flag bound to a CommandDef.
//
// Type is one of "string", "int", "bool", "stringSlice".
type FlagDef struct {
	Name        string
	Short       string
	Default     any
	Required    bool
	Description string
	Type        string
}

// RunArgs is the parsed input to a CommandDef.Run closure.
type RunArgs struct {
	// DryRun is the resolved value of --dry-run. Already validated against
	// CommandDef.DryRunMode by the helper, so closures can treat it as
	// authoritative.
	DryRun bool

	// IdempotencyKey is the resolved value of --idempotency-key (empty if
	// not set; the transport layer mints a UUIDv7 in that case). Closures
	// rarely need to read this — the transport handles it.
	IdempotencyKey string

	// Flags is the parsed flag values keyed by FlagDef.Name (kebab-case).
	// Missing flags are omitted from the map. Use FlagString / FlagInt /
	// FlagBool / FlagSlice for type-safe access.
	Flags map[string]any

	// PathArgs holds positional arguments passed by the user.
	PathArgs []string

	// RawData is the optional --data JSON blob (or @file). When non-nil,
	// it should be the canonical JSON request body. Resource closures use
	// this as the high-leverage path: declare a typed *Request struct,
	// json.Unmarshal RawData into it, optionally override individual
	// fields from Flags, then pass to the generated client method.
	RawData []byte
}

// FlagString returns args.Flags[name] as a string (or "" if absent).
func (a RunArgs) FlagString(name string) string {
	if v, ok := a.Flags[name]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// FlagInt returns args.Flags[name] as an int (or 0 if absent).
func (a RunArgs) FlagInt(name string) int {
	if v, ok := a.Flags[name]; ok {
		if i, ok := v.(int); ok {
			return i
		}
	}
	return 0
}

// FlagBool returns args.Flags[name] as a bool (or false if absent).
func (a RunArgs) FlagBool(name string) bool {
	if v, ok := a.Flags[name]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// FlagSlice returns args.Flags[name] as a []string (or nil if absent).
func (a RunArgs) FlagSlice(name string) []string {
	if v, ok := a.Flags[name]; ok {
		if s, ok := v.([]string); ok {
			return s
		}
	}
	return nil
}

// FlagSet returns true if the named flag was provided on the command line.
func (a RunArgs) FlagSet(name string) bool {
	_, ok := a.Flags[name]
	return ok
}

// RunResult is what a CommandDef.Run closure returns.
type RunResult struct {
	// Status is the HTTP status (200, 202, 422, etc.). Used for audit and
	// for signaling 202 async-accepted on bulk-update.
	Status int

	// Body is the raw response bytes. The runner pretty-prints them in
	// JSON mode and feeds them to output.Format in table/csv modes.
	Body []byte

	// DiffEnv is non-nil when the response carried a MutationResult diff
	// envelope (i.e. a successful dry-run). When set, the runner picks a
	// per-resource RenderDiff function based on ResourceType.
	DiffEnv *invpkg.DiffEnvelope

	// ResourceType identifies which RenderDiff variant to use ("Product",
	// "Booking", "Discount", "GiftCertificate", "PricingTier", or "" for
	// the generic renderer).
	ResourceType string

	// ResourceID, when non-empty, is rendered in the diff header as the
	// subject of the dry-run (e.g. "Booking <id>").
	ResourceID string

	// ResponseID is the top-level data.id (or similar) extracted from the
	// response body for the audit entry.
	ResponseID string

	// AsyncJobID, when non-empty, indicates a 202 + job envelope (currently
	// only BulkUpdateAvailabilities). The runner emits the stderr signal
	// "BULK_UPDATE_ACCEPTED bulk_update_id=<uuid>" (D31) and exits 0.
	AsyncJobID string
}

// Runner is the per-invocation orchestration context.
type Runner struct {
	// Client is the generated CLI v1 client wrapped with the inventory
	// transport chain (auth, idempotency, retry).
	Client *gen.ClientWithResponses

	// AuditLogger is the FileLogger appended to on every successful
	// mutation (D37). Nil when the audit log is disabled.
	AuditLogger *invpkg.FileLogger

	// Abilities is the cached token ability set for this invocation.
	Abilities invpkg.Set

	// Profile is the resolved config used for audit (Tenant slug + profile
	// name) and for the multipart upload outlier (which sidesteps the
	// gen client and posts directly to a constructed URL).
	Profile     *config.Resolved
	ProfileName string
	Tenant      string

	// Format is the resolved output format for this invocation.
	Format string

	Verbose bool
	Out     io.Writer
	Err     io.Writer

	// HTTPClient is the wrapped http.Client (transport chain attached). The
	// multipart-upload outlier uses this directly because the generated
	// UploadProductMediaWithBody signature accepts an io.Reader but the
	// body must be a multipart writer.
	HTTPClient *http.Client
}

// renderResult formats a RunResult onto r.Out, honoring the format flag
// and the dry-run diff envelope.
func (r *Runner) renderResult(_ CommandDef, res *RunResult) error {
	if res == nil {
		return nil
	}

	// 202 async-accepted: emit the side-channel signal first (D31), then
	// continue with normal rendering of the response body.
	if res.AsyncJobID != "" {
		fmt.Fprintf(r.Err, "BULK_UPDATE_ACCEPTED bulk_update_id=%s\n", res.AsyncJobID)
	}

	// Dry-run with a diff envelope: in JSON mode, dump the raw body; in
	// table mode, render the per-resource diff renderer.
	if res.DiffEnv != nil && r.Format == "table" {
		switch res.ResourceType {
		case "Product":
			return invpkg.RenderProductDiff(r.Out, res.ResourceID, *res.DiffEnv)
		case "Booking":
			return invpkg.RenderBookingDiff(r.Out, res.ResourceID, *res.DiffEnv)
		case "Discount":
			return invpkg.RenderDiscountDiff(r.Out, res.ResourceID, *res.DiffEnv)
		case "GiftCertificate":
			return invpkg.RenderGiftCertificateDiff(r.Out, res.ResourceID, *res.DiffEnv)
		case "PricingTier":
			return invpkg.RenderPricingTierDiff(r.Out, res.ResourceID, *res.DiffEnv)
		default:
			return invpkg.RenderDiff(r.Out, res.ResourceType, *res.DiffEnv)
		}
	}

	if len(res.Body) == 0 {
		return nil
	}
	if err := output.Format(r.Out, res.Body, r.Format); err != nil {
		// Fall back to raw bytes if the formatter can't parse (e.g. 204
		// no-content endpoints or non-JSON responses).
		_, _ = r.Out.Write(res.Body)
	}
	return nil
}

// runRead orchestrates the read path. No dry-run, no audit — just preflight
// the ability, invoke Run, render, return.
func runRead(ctx context.Context, r *Runner, def CommandDef, args RunArgs) error {
	if err := invpkg.Refuse(def.Ability, r.Abilities); err != nil {
		return err
	}
	res, err := def.Run(ctx, r, args)
	if err != nil {
		return err
	}
	return r.renderResult(def, res)
}

// runMutation orchestrates the mutation path: ability preflight → dry-run
// gate → invoke Run → audit → render.
//
// On a typed inventory error, runMutation appends an audit entry with the
// error_code and returns the error (cobra renders UserMessage).
func runMutation(ctx context.Context, r *Runner, def CommandDef, args RunArgs) error {
	if err := invpkg.Refuse(def.Ability, r.Abilities); err != nil {
		return err
	}

	// D32: --dry-run on NotSupported is a hard error before network.
	if args.DryRun && def.DryRunMode == DryRunNotSupported {
		return &api.ExitError{
			Err:  fmt.Errorf("--dry-run is not supported by %s %s (this endpoint has no server-side dry-run capability)", def.Verb, def.Path),
			Code: api.ExitValidation,
		}
	}

	// Body capture: we need bytes for the audit body_sha256.
	bodyForHash := args.RawData

	start := time.Now()
	res, runErr := def.Run(ctx, r, args)
	duration := time.Since(start)

	auditEntry := invpkg.AuditEntry{
		Ts:              time.Now().UTC(),
		Profile:         r.ProfileName,
		Tenant:          r.Tenant,
		Command:         def.Verb + " " + def.Path,
		Endpoint:        def.Path,
		IdempotencyKey:  args.IdempotencyKey,
		BodySHA256:      sha256Hex(bodyForHash),
		AbilityUsed:     string(def.Ability),
		DryRun:          args.DryRun,
		ForensicSummary: forensicSummary(def, args),
		Version:         invpkg.AuditSchemaVersion,
		DurationMs:      duration.Milliseconds(),
	}
	if res != nil {
		auditEntry.Status = res.Status
		auditEntry.ResponseID = res.ResponseID
	}
	if runErr != nil {
		auditEntry.ErrorCode = errorCode(runErr)
	}

	if r.AuditLogger != nil {
		_ = r.AuditLogger.Append(auditEntry)
	}

	if runErr != nil {
		return runErr
	}
	return r.renderResult(def, res)
}

// errorCode returns the typed-error code (e.g. "VALIDATION_FAILED") for the
// audit entry. Unknown errors surface as "" so the audit reader can
// distinguish "code not recorded" from "code = INTERNAL_ERROR".
func errorCode(err error) string {
	switch e := err.(type) {
	case *invpkg.AuthError:
		return "UNAUTHENTICATED"
	case *invpkg.AbilityMissingError:
		return "ABILITY_MISSING"
	case *invpkg.NotFoundError:
		return "NOT_FOUND"
	case *invpkg.ValidationError:
		return "VALIDATION_FAILED"
	case *invpkg.IdempotencyConflictError:
		return "IDEMPOTENCY_CONFLICT"
	case *invpkg.IdempotencyInProgressError:
		return "IDEMPOTENCY_IN_PROGRESS"
	case *invpkg.IdempotencyUnknownError:
		return "IDEMPOTENCY_UNKNOWN"
	case *invpkg.DiscountNotApplicableError:
		return "DISCOUNT_NOT_APPLICABLE"
	case *invpkg.ResourceInUseError:
		return "RESOURCE_IN_USE"
	case *invpkg.PayloadTooLargeError:
		return "PAYLOAD_TOO_LARGE"
	case *invpkg.UnsupportedMediaTypeError:
		return "UNSUPPORTED_MEDIA_TYPE"
	case *invpkg.RateLimitError:
		return "RATE_LIMITED"
	case *invpkg.ServerError:
		return "INTERNAL_ERROR"
	case *invpkg.RawAPIError:
		return e.Code
	}
	return ""
}

// forensicSummary captures the per-CommandDef ForensicFields slice into a
// map for the audit entry's forensic_summary (D37).
func forensicSummary(def CommandDef, args RunArgs) map[string]any {
	if len(def.ForensicFields) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, name := range def.ForensicFields {
		if v, ok := args.Flags[name]; ok {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sha256Hex returns the hex-encoded sha256 of body, or "" for nil/empty.
func sha256Hex(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// bindCommands constructs one cobra.Command per CommandDef and attaches it
// to parent. Common flags (--format, --dry-run, --idempotency-key, --data)
// are wired automatically based on Kind / DryRunMode.
func bindCommands(parent *cobra.Command, defs []CommandDef, runner *Runner) {
	for i := range defs {
		def := defs[i] // capture
		c := &cobra.Command{
			Use:     def.Use,
			Short:   def.Short,
			Long:    def.Long,
			Example: def.Example,
		}

		// Per-command-kind format default (cherry-pick #6). The flag is
		// declared here so cobra's "Changed" tracker correctly distinguishes
		// "user passed --format" from "default applied". Reads default to
		// table; mutations to json.
		var formatDefault string
		if def.Kind == KindRead {
			formatDefault = "table"
		} else {
			formatDefault = "json"
		}
		c.Flags().StringP("format", "f", formatDefault, "Output format: json, table, csv")

		// Common mutation flags.
		if def.Kind == KindMutation {
			if def.DryRunMode != DryRunNotSupported {
				c.Flags().Bool("dry-run", false, "Preview the change without committing it")
			}
			c.Flags().String("idempotency-key", "", "Override the auto-minted UUIDv7 idempotency key")
			c.Flags().String("data", "", "JSON request body (literal or @file.json)")
		}

		// Per-command flags.
		for _, fd := range def.Flags {
			declareFlag(c, fd)
		}

		// Positional args.
		if n := len(def.PositionalArgs); n > 0 {
			c.Args = cobra.ExactArgs(n)
		}

		// Annotate ability in --help so users see at a glance what the
		// command needs.
		if def.Ability != "" {
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations["ability"] = string(def.Ability)
		}

		c.RunE = makeRunE(def, runner)
		parent.AddCommand(c)
	}
}

func declareFlag(c *cobra.Command, fd FlagDef) {
	switch fd.Type {
	case "string":
		def, _ := fd.Default.(string)
		if fd.Short != "" {
			c.Flags().StringP(fd.Name, fd.Short, def, fd.Description)
		} else {
			c.Flags().String(fd.Name, def, fd.Description)
		}
	case "int":
		def, _ := fd.Default.(int)
		if fd.Short != "" {
			c.Flags().IntP(fd.Name, fd.Short, def, fd.Description)
		} else {
			c.Flags().Int(fd.Name, def, fd.Description)
		}
	case "bool":
		def, _ := fd.Default.(bool)
		if fd.Short != "" {
			c.Flags().BoolP(fd.Name, fd.Short, def, fd.Description)
		} else {
			c.Flags().Bool(fd.Name, def, fd.Description)
		}
	case "stringSlice":
		var def []string
		if d, ok := fd.Default.([]string); ok {
			def = d
		}
		if fd.Short != "" {
			c.Flags().StringSliceP(fd.Name, fd.Short, def, fd.Description)
		} else {
			c.Flags().StringSlice(fd.Name, def, fd.Description)
		}
	}
	if fd.Required {
		_ = c.MarkFlagRequired(fd.Name)
	}
}

// makeRunE wraps a CommandDef into a cobra RunE function.
func makeRunE(def CommandDef, runner *Runner) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, posArgs []string) error {
		// Resolve format. The local --format (declared with the per-kind
		// default in bindCommands) shadows the root persistent flag, so
		// we just read it directly.
		fmtFlag, _ := cmd.Flags().GetString("format")
		if !output.ValidFormat(fmtFlag) {
			return &api.ExitError{
				Err:  fmt.Errorf("Unknown format %q (use json, table, or csv)", fmtFlag),
				Code: api.ExitValidation,
			}
		}
		runner.Format = fmtFlag

		args := RunArgs{
			Flags:    map[string]any{},
			PathArgs: posArgs,
		}

		if def.Kind == KindMutation {
			if def.DryRunMode != DryRunNotSupported {
				args.DryRun, _ = cmd.Flags().GetBool("dry-run")
			}
			args.IdempotencyKey, _ = cmd.Flags().GetString("idempotency-key")
			data, _ := cmd.Flags().GetString("data")
			if data != "" {
				raw, err := readDataFlag(data)
				if err != nil {
					return &api.ExitError{Err: err, Code: api.ExitValidation}
				}
				args.RawData = raw
			}
		}

		// Pull declared flags into args.Flags. Only flags the user actually
		// changed are recorded so forensic_summary stays clean.
		for _, fd := range def.Flags {
			if !cmd.Flags().Changed(fd.Name) {
				continue
			}
			switch fd.Type {
			case "string":
				v, _ := cmd.Flags().GetString(fd.Name)
				args.Flags[fd.Name] = v
			case "int":
				v, _ := cmd.Flags().GetInt(fd.Name)
				args.Flags[fd.Name] = v
			case "bool":
				v, _ := cmd.Flags().GetBool(fd.Name)
				args.Flags[fd.Name] = v
			case "stringSlice":
				v, _ := cmd.Flags().GetStringSlice(fd.Name)
				args.Flags[fd.Name] = v
			}
		}

		runner.Out = cmd.OutOrStdout()
		runner.Err = cmd.ErrOrStderr()

		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}

		switch def.Kind {
		case KindRead:
			return runRead(ctx, runner, def, args)
		case KindMutation:
			return runMutation(ctx, runner, def, args)
		}
		return nil
	}
}

// readDataFlag returns the bytes named by a --data flag value. A leading
// "@" indicates a file path (curl convention); otherwise the value is
// treated as a literal JSON blob.
func readDataFlag(v string) ([]byte, error) {
	if strings.HasPrefix(v, "@") {
		path := strings.TrimPrefix(v, "@")
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading --data file %q: %w", path, err)
		}
		return b, nil
	}
	return []byte(v), nil
}

// ParseGenResponse turns a (Body, HTTPResponse) pair from a generated
// *FooResponse into a typed RunResult or a typed inventory error.
//
// Resource closures call this with the .Body and .HTTPResponse fields of
// the generated response struct so that error envelopes, dry-run diffs,
// and async 202 envelopes all flow through one place.
func ParseGenResponse(body []byte, httpResp *http.Response, resourceType, resourceID string) (*RunResult, error) {
	if httpResp == nil {
		return nil, fmt.Errorf("inventory: gen client returned nil HTTPResponse")
	}
	status := httpResp.StatusCode

	if status >= 200 && status < 300 {
		res := &RunResult{
			Status:       status,
			Body:         body,
			ResourceType: resourceType,
			ResourceID:   resourceID,
		}
		if env, ok := tryParseDiffEnvelope(body); ok {
			res.DiffEnv = env
		}
		if status == http.StatusAccepted {
			if jobID := tryParseAsyncJobID(body); jobID != "" {
				res.AsyncJobID = jobID
			}
		}
		res.ResponseID = tryParseDataID(body)
		return res, nil
	}

	parsed := invpkg.ParseError(status, body)
	if status == http.StatusTooManyRequests {
		parsed = invpkg.WithRetryAfter(parsed, invpkg.ParseRetryAfter(httpResp.Header.Get("Retry-After")))
	}
	return nil, parsed
}

// tryParseDiffEnvelope returns a *invpkg.DiffEnvelope if body looks like a
// MutationResult dry-run response.
func tryParseDiffEnvelope(body []byte) (*invpkg.DiffEnvelope, bool) {
	var env struct {
		Data invpkg.DiffEnvelope `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, false
	}
	if env.Data.Diff.Before == nil && env.Data.Diff.After == nil && !env.Data.WouldApply {
		return nil, false
	}
	cp := env.Data
	return &cp, true
}

// tryParseAsyncJobID returns the bulk_update_id from a 202 response, or "".
func tryParseAsyncJobID(body []byte) string {
	var env struct {
		Data struct {
			BulkUpdateID string `json:"bulk_update_id"`
			JobID        string `json:"job_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	if env.Data.BulkUpdateID != "" {
		return env.Data.BulkUpdateID
	}
	return env.Data.JobID
}

// tryParseDataID extracts data.id (or data.<resource>.id as a fallback) for
// audit logging.
func tryParseDataID(body []byte) string {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	var direct struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &direct); err == nil && direct.ID != "" {
		return direct.ID
	}
	return ""
}

// MintIdempotencyKey returns a fresh UUIDv7 string.
func MintIdempotencyKey() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// JSONBodyFromArgs is a small helper resource closures use when both
// --data and per-field flags are present. It starts with args.RawData (or
// "{}" when nil), unmarshals into a generic map, applies any flag overrides,
// optionally sets dry_run, and returns canonical JSON bytes ready for the
// gen client.
func JSONBodyFromArgs(args RunArgs, dryRun bool, fieldMap map[string]string) ([]byte, error) {
	body := map[string]any{}
	if len(args.RawData) > 0 {
		if err := json.Unmarshal(args.RawData, &body); err != nil {
			return nil, fmt.Errorf("--data: invalid JSON: %w", err)
		}
	}
	// Per-field flag overrides: fieldMap is flag-name → JSON-key.
	for flagName, jsonKey := range fieldMap {
		v, ok := args.Flags[flagName]
		if !ok {
			continue
		}
		body[jsonKey] = v
	}
	if dryRun {
		body["dry_run"] = true
	}
	return json.Marshal(body)
}

// asReader is a tiny convenience used by *WithBody resource closures.
func asReader(b []byte) io.Reader { return bytes.NewReader(b) }

// errMissingPathArg is surfaced when a CommandDef declared positional args
// but cobra's Args validator was bypassed (defensive; should never fire).
var errMissingPathArg = errors.New("inventory: missing required positional argument")

// pathArg returns args.PathArgs[0] or errMissingPathArg.
func pathArg(args RunArgs) (string, error) {
	if len(args.PathArgs) == 0 {
		return "", errMissingPathArg
	}
	return args.PathArgs[0], nil
}

// -----------------------------------------------------------------------------
// Cmd() — the parent inventory command + Runner constructor.
// -----------------------------------------------------------------------------

// Cmd returns the parent inventory cobra command. cmd/root.go calls this
// in init() and adds it to the root.
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Manage inventory (products, bookings, discounts, …)",
		Long: "Inventory CLI v1: per-resource subcommands for the captainbook " +
			"inventory API. All mutations support --dry-run where the server " +
			"supports it; mutations are audited to ~/.ceebee/audit.jsonl. " +
			"Read commands default to --format=table; mutations default to " +
			"--format=json.",
	}

	// PersistentPreRunE: lazy runner construction. We defer ability
	// preflight (and config resolution + transport build) until a
	// subcommand actually fires so `ceebee inventory --help` doesn't
	// require config or network.
	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		if sharedRunner.Client != nil {
			return nil
		}
		r, err := newRunner(c)
		if err != nil {
			return err
		}
		*sharedRunner = *r
		return nil
	}

	if sharedRunner == nil {
		sharedRunner = &Runner{}
	}

	// Each resource is nested under a parent command (D9: nested
	// `ceebee inventory <resource> <verb>` namespace) so the help tree
	// reads as one resource per line rather than 60+ flat verbs.
	bindCommands(cmd, authDefs(), sharedRunner) // whoami is a top-level verb, not nested
	cmd.AddCommand(makeResourceParent("products", "Manage products", productsDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("product-options", "Manage product options", productOptionsDefs(), sharedRunner))
	cmd.AddCommand(availabilitiesCmd(sharedRunner))
	cmd.AddCommand(makeResourceParent("pricing-tiers", "Manage pricing tiers", pricingTiersDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("discounts", "Manage discounts", discountsDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("gift-certificates", "Manage gift certificates", giftCertificatesDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("bookings", "Manage bookings", bookingsDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("transactions", "Read transactions", transactionsDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("customers", "Read customers", customersDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("guests", "Manage guests", guestsDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("extras", "Manage extras", extrasDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("questions", "Manage questions", questionsDefs(), sharedRunner))
	cmd.AddCommand(makeResourceParent("categories", "Manage categories", categoriesDefs(), sharedRunner))
	cmd.AddCommand(mediaCmd(sharedRunner))
	cmd.AddCommand(makeResourceParent("notifications", "Send notifications", notificationsDefs(), sharedRunner))

	return cmd
}

// makeResourceParent wraps a []CommandDef in a per-resource cobra parent
// command. It strips the leading "<resource> " prefix from each
// CommandDef.Use so the resulting tree is `inventory <resource> <verb>`
// rather than `inventory <resource> <resource> <verb>`.
func makeResourceParent(name, short string, defs []CommandDef, runner *Runner) *cobra.Command {
	parent := &cobra.Command{Use: name, Short: short}
	stripped := make([]CommandDef, len(defs))
	for i, d := range defs {
		// "products list" -> "list"; "products get <id>" -> "get <id>".
		d.Use = strings.TrimPrefix(d.Use, name+" ")
		stripped[i] = d
	}
	bindCommands(parent, stripped, runner)
	return parent
}

// sharedRunner is the per-process singleton mutated by PersistentPreRunE.
// All subcommands close over this exact pointer at bind time so by the
// time RunE fires, *sharedRunner is fully initialised.
var sharedRunner *Runner

// ProfileFlag and VerboseFlag are set by cmd/root.go before Execute().
// The inventory package reads them in newRunner rather than reaching into
// the root cobra command's flag set, so root and inventory stay decoupled.
var (
	ProfileFlag = ""
	VerboseFlag = false
)

// newRunner resolves the profile, builds the transport, runs the abilities
// preflight, and opens the audit log.
func newRunner(_ *cobra.Command) (*Runner, error) {
	if testNewRunner != nil {
		return testNewRunner()
	}

	resolved, err := config.Resolve(ProfileFlag)
	if err != nil {
		return nil, &api.ExitError{Err: err, Code: api.ExitConfig}
	}

	u, err := url.Parse(resolved.URL)
	if err != nil || u.Host == "" {
		return nil, &api.ExitError{
			Err:  fmt.Errorf("invalid profile URL %q", resolved.URL),
			Code: api.ExitConfig,
		}
	}

	verboseW := io.Writer(os.Stderr)
	transport := invpkg.New(invpkg.Config{
		Token:        resolved.Token,
		ExpectedHost: u.Host,
		Verbose:      VerboseFlag,
		VerboseW:     verboseW,
	}, nil)

	httpClient := &http.Client{Transport: transport, Timeout: 60 * time.Second}
	client, err := gen.NewClientWithResponses(resolved.URL, gen.WithHTTPClient(httpClient))
	if err != nil {
		return nil, &api.ExitError{Err: err, Code: api.ExitConfig}
	}

	cache, _ := invpkg.NewDiskCache()
	whoamiFn := func(ctx context.Context) (invpkg.Set, time.Time, error) {
		resp, err := client.WhoamiWithResponse(ctx)
		if err != nil {
			return nil, time.Time{}, err
		}
		if resp.JSON200 == nil {
			return nil, time.Time{}, invpkg.ParseError(resp.StatusCode(), resp.Body)
		}
		w := resp.JSON200
		var set invpkg.Set
		if w.Data.Token != nil && w.Data.Token.Abilities != nil {
			for _, a := range *w.Data.Token.Abilities {
				set = append(set, invpkg.Ability(a))
			}
		}
		var expires time.Time
		if w.Data.Token != nil && w.Data.Token.ExpiresAt != nil {
			expires = *w.Data.Token.ExpiresAt
		}
		return set, expires, nil
	}
	abilities, err := invpkg.Preflight(context.Background(), u.Host, resolved.Token, cache, whoamiFn)
	if err != nil {
		return nil, err
	}

	var logger *invpkg.FileLogger
	if path, perr := invpkg.DefaultAuditPath(); perr == nil {
		logger, _ = invpkg.NewFileLogger(path)
	}

	return &Runner{
		Client:      client,
		HTTPClient:  httpClient,
		AuditLogger: logger,
		Abilities:   abilities,
		Profile:     resolved,
		ProfileName: ProfileFlag,
		Tenant:      u.Host,
		Verbose:     VerboseFlag,
		Out:         os.Stdout,
		Err:         os.Stderr,
	}, nil
}

// testNewRunner is the override used by inventory_test.go to inject a fake
// transport / config without touching disk or env.
var testNewRunner func() (*Runner, error)
