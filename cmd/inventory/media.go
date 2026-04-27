package inventory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
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

	bindCommands(parent, []CommandDef{
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
				resp, err := r.Client.DeleteMediaWithResponse(ctx, id, &gen.DeleteMediaParams{})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Media", id)
			},
		},
	}, runner)

	parent.AddCommand(uploadCmd(runner))
	return parent
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
	_ = c.MarkFlagRequired("file")
	c.RunE = func(cmd *cobra.Command, posArgs []string) error {
		productID := posArgs[0]
		path, _ := cmd.Flags().GetString("file")
		idemKey, _ := cmd.Flags().GetString("idempotency-key")
		fmtFlag, _ := cmd.Flags().GetString("format")

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

		// Mint or honor the idempotency key.
		if idemKey == "" {
			idemKey, err = MintIdempotencyKey()
			if err != nil {
				return fmt.Errorf("upload: minting idempotency key: %w", err)
			}
		}

		// Send via gen client's WithBody method. The transport's
		// idempotencyKeyRT will see the key already set on the request
		// and skip re-minting.
		params := &gen.UploadProductMediaParams{}
		// Convert idemKey to *IdempotencyKey (UUID under the hood); the
		// transport sets the header from req.Header rather than the typed
		// param, so we attach via reqEditors below for correctness.
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		reqEditor := func(_ context.Context, req *http.Request) error {
			req.Header.Set("Idempotency-Key", idemKey)
			return nil
		}

		start := time.Now()
		resp, err := runner.Client.UploadProductMediaWithBodyWithResponse(ctx, productID, params, mw.FormDataContentType(), &bodyBuf, reqEditor)
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

		// Audit entry (D37: file_size, mime_type, file_name).
		entry := invpkg.AuditEntry{
			Ts:             time.Now().UTC(),
			Profile:        runner.ProfileName,
			Tenant:         runner.Tenant,
			Command:        "POST /products/{id}/media",
			Endpoint:       "/products/{id}/media",
			IdempotencyKey: idemKey,
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
