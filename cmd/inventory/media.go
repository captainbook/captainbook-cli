package inventory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/captainbook/captainbook-cli/internal/api"
	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// mediaCmd builds the `inventory media` subtree.
//
// list / get / delete go through bindCommands like other resources. Upload
// is the multipart-upload outlier (D18) and is hand-written here:
//   - Pre-flight: stat file size (refuse if > 10 MiB, per spec).
//   - Pre-flight: MIME-sniff first 512 bytes via http.DetectContentType,
//     refuse if not in image/jpeg|image/png|image/webp|image/gif|application/pdf.
//   - Mint a fresh UUIDv7 idempotency key.
//   - Build the multipart/form-data body in memory (fits trivially in 10 MiB).
//   - POST via the gen client's *WithBody method.
//   - Audit entry with forensic_summary={file_size, mime_type, file_name}.
//
// No --dry-run support: D32 NotSupported. The CommandDef declares it and
// runMutation rejects --dry-run before this Run fires.
func mediaCmd(runner *Runner) *cobra.Command {
	parent := &cobra.Command{
		Use:   "media",
		Short: "Manage product media (images, PDFs)",
	}
	bindCommands(parent, mediaDefs(), runner)
	parent.AddCommand(uploadCmd(runner))
	return parent
}

// mediaDefs returns the media commands that flow through the standard
// bindCommands pipeline. The multipart upload outlier (uploadCmd) is
// hand-written separately because it needs pre-flight stat + MIME sniff
// before any network call (D18 + Critical Rule §5).
func mediaDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "list <product-id>", Short: "List media for a product",
			Kind: KindRead, Verb: "GET", Path: "/products/{id}/media",
			Ability: invpkg.Read, PositionalArgs: []string{"product-id"},
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				p := &gen.ListProductMediaParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				resp, err := r.Client.ListProductMediaWithResponse(ctx, id, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Media", id)
			},
		},
		{
			Use: "get <id>", Short: "Show one media item",
			Kind: KindRead, Verb: "GET", Path: "/media/{id}",
			Ability: invpkg.Read, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowMediaWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Media", id)
			},
		},
		{
			Use: "delete <id>", Short: "Delete a media item",
			Kind: KindMutation, Verb: "DELETE", Path: "/media/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunNotSupported,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeleteMediaWithResponse(ctx, id, &gen.DeleteMediaParams{IdempotencyKey: args.IdempotencyKeyUUID})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Media", id)
			},
		},
	}
}

// uploadCmd builds the multipart-upload outlier as a hand-written cobra
// command, sidestepping bindCommands so we can implement the pre-flight
// rules (file size + MIME) before any network call. Audit logging mirrors
// the runMutation path (D37 forensic_summary with file metadata).
func uploadCmd(runner *Runner) *cobra.Command {
	c := &cobra.Command{
		Use:   "upload <product-id>",
		Short: "Upload a media file for a product (multipart, no --dry-run)",
		Long: "Pre-flight checks (Critical Rules §5):\n" +
			"  - File size MUST be <= 10 MiB\n" +
			"  - MIME type MUST be one of: image/jpeg, image/png, image/webp, image/gif, application/pdf\n" +
			"\nNo --dry-run support (D32 NotSupported).",
		Args: cobra.ExactArgs(1),
	}
	c.Flags().StringP("file", "F", "", "Path to the media file (required)")
	c.Flags().StringP("format", "f", "json", "Output format: json, table, csv")
	c.Flags().String("idempotency-key", "", "Override the auto-minted UUIDv7 idempotency key")
	// --dry-run is declared so users get the typed "not supported" error
	// from D32 instead of cobra's "unknown flag --dry-run" output.
	c.Flags().Bool("dry-run", false, "Not supported by uploadProductMedia (multipart endpoint has no server-side dry-run)")
	_ = c.MarkFlagRequired("file")
	c.RunE = func(cmd *cobra.Command, posArgs []string) error {
		productID := posArgs[0]
		path, _ := cmd.Flags().GetString("file")
		idemKey, _ := cmd.Flags().GetString("idempotency-key")
		fmtFlag, _ := cmd.Flags().GetString("format")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// D32: --dry-run on a NotSupported endpoint = hard error.
		if dryRun {
			return &api.ExitError{
				Err:  fmt.Errorf("--dry-run is not supported by POST /products/{id}/media (multipart upload has no server-side dry-run capability)"),
				Code: api.ExitValidation,
			}
		}

		// Ability gate first.
		if err := invpkg.Refuse(invpkg.Write, runner.Abilities); err != nil {
			return err
		}

		// Pre-flight: stat + MIME sniff.
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("upload: stat %q: %w", path, err)
		}
		if info.Size() > maxUploadBytes {
			return &invpkg.PayloadTooLargeError{ActualBytes: info.Size(), MaxBytes: maxUploadBytes}
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("upload: open %q: %w", path, err)
		}
		defer f.Close()

		head := make([]byte, 512)
		n, _ := f.Read(head)
		mime := http.DetectContentType(head[:n])
		if !isAllowedMIME(mime) {
			return &invpkg.UnsupportedMediaTypeError{Got: mime, Allowed: allowedMIMEs}
		}
		// Reset to start so we re-read the full file into the multipart body.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("upload: seek: %w", err)
		}

		// Build multipart body in memory (D18: 10 MiB cap means trivial).
		var bodyBuf bytes.Buffer
		mw := multipart.NewWriter(&bodyBuf)
		// `file` is the field name per the spec's UploadProductMedia operation.
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filepath.Base(path)))
		h.Set("Content-Type", mime)
		part, err := mw.CreatePart(h)
		if err != nil {
			return fmt.Errorf("upload: create part: %w", err)
		}
		if _, err := io.Copy(part, f); err != nil {
			return fmt.Errorf("upload: copy: %w", err)
		}
		if err := mw.Close(); err != nil {
			return fmt.Errorf("upload: close multipart: %w", err)
		}

		// Mint or honor the idempotency key. Parse into a *UUID so we
		// can pass it via Params.IdempotencyKey (matching how runMutation
		// threads keys through the gen client). This keeps the audit
		// entry's idempotency_key in lockstep with the wire request and
		// satisfies the structural TestSpecDrift_IdempotencyKeyThreaded
		// guard that no mutation Params literal goes out without a key.
		if idemKey == "" {
			idemKey, err = MintIdempotencyKey()
			if err != nil {
				return fmt.Errorf("upload: minting idempotency key: %w", err)
			}
		}
		parsedKey, err := uuid.Parse(idemKey)
		if err != nil {
			return &api.ExitError{
				Err:  fmt.Errorf("--idempotency-key %q is not a valid UUID: %w", idemKey, err),
				Code: api.ExitValidation,
			}
		}
		// Canonicalize so audit's idempotency_key matches the wire form
		// (parsed.String() — lowercase, hyphenated, no braces).
		idemKey = parsedKey.String()

		params := &gen.UploadProductMediaParams{IdempotencyKey: &parsedKey}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}

		// Snapshot the multipart bytes for audit BEFORE the read consumes
		// the buffer. bytes.Buffer's read pointer advances during the
		// upload; calling .Bytes() afterward returns the unread tail
		// (typically empty), which would hash to e3b0c44... on every
		// upload — a silent forensics bug. The buffer is at most 10 MiB
		// (D18) so this snapshot is bounded.
		multipartBytes := append([]byte(nil), bodyBuf.Bytes()...)
		bodyHash := sha256.Sum256(multipartBytes)

		start := time.Now()
		resp, err := runner.Client.UploadProductMediaWithBodyWithResponse(ctx, productID, params, mw.FormDataContentType(), &bodyBuf)
		duration := time.Since(start)
		var status int
		var responseID string
		var runRes *RunResult
		var runErr error
		if err != nil {
			runErr = err
		} else {
			runRes, runErr = ParseGenResponse(resp.Body, resp.HTTPResponse, "Media", productID)
			if runRes != nil {
				status = runRes.Status
				responseID = runRes.ResponseID
			}
		}
		entry := invpkg.AuditEntry{
			Ts:             time.Now().UTC(),
			Profile:        runner.ProfileName,
			Tenant:         runner.Tenant,
			Command:        "POST /products/{id}/media",
			Endpoint:       "/products/{id}/media",
			IdempotencyKey: idemKey,
			BodySHA256:     hex.EncodeToString(bodyHash[:]),
			AbilityUsed:    string(invpkg.Write),
			DryRun:         false,
			Status:         status,
			ResponseID:     responseID,
			DurationMs:     duration.Milliseconds(),
			Version:        invpkg.AuditSchemaVersion,
			ForensicSummary: map[string]any{
				"file_size": info.Size(),
				"mime_type": mime,
				"file_name": filepath.Base(path),
			},
		}
		if runErr != nil {
			entry.ErrorCode = errorCode(runErr)
		}
		if runner.AuditLogger != nil {
			_ = runner.AuditLogger.Append(entry)
		}

		if runErr != nil {
			return runErr
		}
		runner.Format = fmtFlag
		runner.Out = cmd.OutOrStdout()
		runner.Err = cmd.ErrOrStderr()
		return runner.renderResult(CommandDef{}, runRes)
	}
	return c
}

const maxUploadBytes int64 = 10 * 1024 * 1024 // 10 MiB (Critical Rule §5)

var allowedMIMEs = []string{
	"image/jpeg", "image/png", "image/webp", "image/gif", "application/pdf",
}

func isAllowedMIME(got string) bool {
	got = strings.SplitN(got, ";", 2)[0]
	got = strings.TrimSpace(got)
	for _, m := range allowedMIMEs {
		if got == m {
			return true
		}
	}
	return false
}
