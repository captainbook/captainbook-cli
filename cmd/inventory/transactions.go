package inventory

import (
	"context"
	"time"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// transactionsDefs declares transactions: list, get.
//
// Transactions are read-only at the CLI level — refunds happen via the
// `bookings refund` command, which mints a refund transaction server-side.
func transactionsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "transactions list", Short: "List transactions", Kind: KindRead,
			Verb: "GET", Path: "/transactions", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"}, {Name: "cursor", Type: "string"},
				{Name: "status", Type: "string", Description: "succeeded|pending|failed"},
				{Name: "type", Type: "string", Description: "charge|refund|chargeback"},
				{Name: "from", Type: "string", Description: "Transaction created_at >= ISO 8601"},
				{Name: "to", Type: "string", Description: "Transaction created_at <= ISO 8601"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListTransactionsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("status"); v != "" {
					s := gen.ListTransactionsParamsStatus(v)
					p.Status = &s
				}
				if v := args.FlagString("type"); v != "" {
					t := gen.ListTransactionsParamsType(v)
					p.Type = &t
				}
				if v := args.FlagString("from"); v != "" {
					if t, err := time.Parse(time.RFC3339, v); err == nil {
						p.From = &t
					}
				}
				if v := args.FlagString("to"); v != "" {
					if t, err := time.Parse(time.RFC3339, v); err == nil {
						p.To = &t
					}
				}
				resp, err := r.Client.ListTransactionsWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Transaction", "")
			},
		},
		{
			Use: "transactions get <id>", Short: "Show one transaction", Kind: KindRead,
			Verb: "GET", Path: "/transactions/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowTransactionWithResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Transaction", id)
			},
		},
	}
}

// timeParseDate is shared by bookings.go (parseDate) — defined here so
// transactions.go owns the time import.
func timeParseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}
