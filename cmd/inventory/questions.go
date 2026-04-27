package inventory

import (
	"context"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// questionsDefs declares the questions resource: list, get, create, update,
// delete, restore.
func questionsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "questions list", Short: "List questions", Kind: KindRead,
			Verb: "GET", Path: "/questions", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
				{Name: "product-option-id", Type: "string", Description: "Filter by option"},
				{Name: "required", Type: "bool", Description: "Filter required-only"},
				{Name: "include-trashed", Type: "bool"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListQuestionsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("product-option-id"); v != "" {
					p.ProductOptionId = &v
				}
				if args.FlagSet("required") {
					b := args.FlagBool("required")
					p.Required = &b
				}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				resp, err := r.Client.ListQuestionsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Question", "")
			},
		},
		{
			Use: "questions get <id>", Short: "Show one question", Kind: KindRead,
			Verb: "GET", Path: "/questions/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowQuestionWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Question", id)
			},
		},
		{
			Use: "questions create", Short: "Create a question", Kind: KindMutation,
			Verb: "POST", Path: "/questions", Ability: invpkg.Write, DryRunMode: DryRunBody,
			Flags: []FlagDef{
				{Name: "label", Type: "string", Required: true, Description: "Question text shown to customer"},
				{Name: "type", Type: "string", Required: true, Description: "text|textarea|select|date|number|boolean"},
				{Name: "product-option-id", Type: "string", Required: true, Description: "Owning product option"},
				{Name: "required", Type: "bool", Description: "Whether the answer is required"},
				{Name: "options", Type: "stringSlice", Description: "Options (for type=select)"},
			},
			ForensicFields: []string{"required", "type"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"label":             "label",
					"type":              "type",
					"product-option-id": "product_option_id",
					"required":          "required",
					"options":           "options",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateQuestionWithBodyWithResponse(ctx, &gen.CreateQuestionParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Question", "")
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "questions update <id>", Short: "Update a question", Kind: KindMutation,
			Verb: "PATCH", Path: "/questions/{id}", Ability: invpkg.Write,
			DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "label", Type: "string", Description: "Question text shown to customer"},
				{Name: "type", Type: "string", Description: "text|textarea|select|date|number|boolean"},
				{Name: "required", Type: "bool", Description: "Whether the answer is required"},
				{Name: "options", Type: "stringSlice", Description: "Options (for type=select)"},
			},
			ForensicFields: []string{"required", "type"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"label":    "label",
					"type":     "type",
					"required": "required",
					"options":  "options",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateQuestionWithBodyWithResponse(ctx, id, &gen.UpdateQuestionParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				res, err := ParseGenResponse(resp.Body, resp.HTTPResponse, "Question", id)
				if res != nil {
					res.WireBody = body
				}
				return res, err
			},
		},
		{
			Use: "questions delete <id>", Short: "Soft-delete a question", Kind: KindMutation,
			Verb: "DELETE", Path: "/questions/{id}", Ability: invpkg.Write,
			DryRunMode: DryRunNotSupported, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeleteQuestionWithResponse(ctx, id, &gen.DeleteQuestionParams{})
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Question", id)
			},
		},
		{
			Use: "questions restore <id>", Short: "Restore a soft-deleted question",
			Kind: KindMutation, Verb: "POST", Path: "/questions/{id}/restore",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, nil)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RestoreQuestionWithBodyWithResponse(ctx, id, &gen.RestoreQuestionParams{}, "application/json", asReader(body))
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Question", id)
			},
		},
	}
}
