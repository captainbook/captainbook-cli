package inventory

import (
	"context"
	"fmt"
)

// authDefs declares the auth resource (the only endpoint is whoami).
//
// Whoami is a read; the runner has already preflighted the token's
// abilities via Preflight before this command runs, so all whoami does
// here is render the cached / fresh result for the user.
func authDefs() []CommandDef {
	return []CommandDef{
		{
			Use:     "whoami",
			Short:   "Show the current token's identity, abilities, and tenant",
			Long:    "Calls /auth/whoami and prints actor + tenant + token-abilities.\n\nNo ability required (this IS the ability discovery endpoint).",
			Example: "  ceebee inventory whoami\n  ceebee inventory whoami --format json",
			Kind:    KindRead,
			Verb:    "GET",
			Path:    "/auth/whoami",
			Ability: "", // No ability gate — whoami is what reveals abilities.
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				resp, err := r.Client.WhoamiWithResponse(ctx)
				if err != nil {
					return nil, fmt.Errorf("whoami: %w", err)
				}
				return ParseGenResponse(resp.Body, resp.HTTPResponse, "Token", "")
			},
		},
	}
}

