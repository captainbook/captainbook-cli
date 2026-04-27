package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// giftCertificatesDefs declares gift certificates: list-available,
// list-issued, get-issued, create-available, issue, void, resend.
//
// Tuned diff renderer: "GiftCertificate" — fired on dry-runs of issue / void.
//
// Note: the spec separates "available" (templates) from "issued" (an
// instance with a code). The CLI mirrors that split:
//   - list-available / get-available / create-available work on the
//     /gift-certificates resource (templates).
//   - list-issued / get-issued / issue / void / resend work on the
//     /issued-gift-certificates resource.
func giftCertificatesDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "gift-certificates list-available", Short: "List available (template) gift certs",
			Kind: KindRead, Verb: "GET", Path: "/gift-certificates", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListAvailableGiftCertsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				resp, err := r.Client.ListAvailableGiftCertsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "GiftCertificate", "")
			},
		},
		{
			Use: "gift-certificates create-available", Short: "Create a gift cert template",
			Kind: KindMutation, Verb: "POST", Path: "/gift-certificates",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateAvailableGiftCertWithBodyWithResponse(ctx, &gen.CreateAvailableGiftCertParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "GiftCertificate", "")
			},
		},
		{
			Use: "gift-certificates list-issued", Short: "List issued gift certs",
			Kind: KindRead, Verb: "GET", Path: "/issued-gift-certificates", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
				{Name: "status", Type: "string", Description: "active|redeemed|voided"},
				{Name: "recipient-email", Type: "string", Description: "Filter by recipient email"},
				{Name: "code", Type: "string", Description: "Filter by code"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListIssuedGiftCertsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("status"); v != "" {
					s := gen.ListIssuedGiftCertsParamsStatus(v)
					p.Status = &s
				}
				if v := args.FlagString("recipient-email"); v != "" {
					e := openapi_types.Email(v)
					p.RecipientEmail = &e
				}
				if v := args.FlagString("code"); v != "" {
					p.Code = &v
				}
				resp, err := r.Client.ListIssuedGiftCertsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "GiftCertificate", "")
			},
		},
		{
			Use: "gift-certificates get-issued <id>", Short: "Show one issued gift cert",
			Kind: KindRead, Verb: "GET", Path: "/issued-gift-certificates/{id}",
			Ability: invpkg.Read, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowIssuedGiftCertWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "GiftCertificate", id)
			},
		},
		{
			Use: "gift-certificates issue", Short: "Issue a gift cert from a template",
			Kind: KindMutation, Verb: "POST", Path: "/issued-gift-certificates",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.IssueGiftCertWithBodyWithResponse(ctx, &gen.IssueGiftCertParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "GiftCertificate", "")
			},
		},
		{
			Use: "gift-certificates void <id>", Short: "Void an issued gift cert",
			Kind: KindMutation, Verb: "POST", Path: "/issued-gift-certificates/{id}/void",
			Ability:    invpkg.CS, // Voiding is a CS-only override action.
			DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "reason", Type: "string", Required: true, Description: "Void reason (required)"},
				{Name: "notify-recipient", Type: "bool", Description: "Notify recipient of void"},
			},
			ForensicFields: []string{"reason", "notify-recipient"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"reason":           "reason",
					"notify-recipient": "notify_recipient",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.VoidGiftCertWithBodyWithResponse(ctx, id, &gen.VoidGiftCertParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "GiftCertificate", id)
			},
		},
		{
			Use: "gift-certificates resend <id>", Short: "Resend a gift cert email",
			Kind: KindMutation, Verb: "POST", Path: "/issued-gift-certificates/{id}/resend",
			Ability: invpkg.Write, DryRunMode: DryRunBody,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "recipient-email", Type: "string", Description: "Override original recipient"},
			},
			ForensicFields: []string{"recipient-email"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"recipient-email": "recipient_email",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ResendGiftCertWithBodyWithResponse(ctx, id, &gen.ResendGiftCertParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "GiftCertificate", id)
			},
		},
	}
}

