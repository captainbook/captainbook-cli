package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/captainbook/captainbook-cli/internal/api"
	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
	"github.com/spf13/cobra"
)

// workflowsCmd builds the `inventory workflows` subtree. Most verbs are
// declared via workflowsDefs(); the `trigger` and `steps` sub-resources
// nest under their own parents (workflowsTriggerCmd / workflowsStepsCmd)
// because they share the workflow ID positional arg with their own verb
// fan-out (create/update for trigger; create/update/delete for steps).
func workflowsCmd(runner *Runner) *cobra.Command {
	parent := &cobra.Command{
		Use:   "workflows",
		Short: "Manage workflows (DRAFT → PAUSED → ACTIVE state machine)",
		Long: "Workflow definitions plus their singleton trigger and step tree. " +
			"All write endpoints require workflow.status ∈ {DRAFT, PAUSED}; ACTIVE workflows " +
			"return 409 WORKFLOW_NOT_EDITABLE for /trigger and /steps* writes. " +
			"Shell PATCH (name, description, notify_on_fail, max_credits_per_run) is " +
			"allowed on ACTIVE workflows.",
	}
	bindCommands(parent, workflowsDefs(), runner)
	parent.AddCommand(workflowsTriggerCmd(runner))
	parent.AddCommand(workflowsStepsCmd(runner))
	return parent
}

// workflowsDefs declares the workflow shell verbs: list/get/create/update/
// delete/restore/activate/deactivate. The five state-transition verbs
// (delete/restore/activate/deactivate) all take a `{dry_run}` body shape.
func workflowsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "nodes", Short: "The trigger + action catalogue for composing a workflow",
			Kind: KindRead, Verb: "GET", Path: "/workflow-nodes", Ability: invpkg.Read,
			Long: "Lists the trigger and action vocabulary: which action_type values exist " +
				"and what each one's config must contain. Read this before writing " +
				"create --trigger or create --steps.\n\n" +
				"This route sits behind the workflows subscription gate, so a tenant whose " +
				"plan does not include workflows gets a 404 carrying that reason — it is " +
				"not a missing resource.\n\n" +
				"The catalogue publishes each node's rules() verbatim, so it is the " +
				"authoritative channel list for send_notification. As of spec 1.56.0 that " +
				"includes channel: whatsapp, which previously came back 422 " +
				"INVALID_STEP_CONFIG: the message goes out over the account's own paired " +
				"WhatsApp and threads into the Inbox, and it is FREE TEXT, so there is no " +
				"template to name. It needs a connected WhatsApp, and without one the step " +
				"does NOT fall back to SMS.\n\n" +
				"Since spec 1.62.0 a send_notification we declined to send FAILS the step, " +
				"where it used to complete with sent: false and a reason. Read the step " +
				"status in `workflow-executions logs`, not just `sent`: a run whose " +
				"notification never went out is no longer reported as successful. Four " +
				"reasons fail — no_whatsapp_sender, whatsapp_not_in_plan, " +
				"no_booking_in_context and empty_message, all cases where nothing was " +
				"attempted and nothing downstream will fix it. Three deliberately do not: " +
				"not_delivered (the catch-all default, and the only reason mail, SMS and " +
				"push ever produce) and the wallet channel's intended skips throttled and " +
				"no_installed_passes.\n\n" +
				"config.continue_on_error (boolean, optional, send_notification only) " +
				"restores the old behaviour for ONE step: the refusal is still reported in " +
				"reason and the run carries on. That is the migration path for a workflow " +
				"that was relying on the silence. It goes in --config like any other " +
				"rules() field.\n\n" +
				"Every TRIGGER node comes back with config: [] (an empty JSON array) " +
				"where the spec declares an object, because Laravel serializes an empty " +
				"config as [] rather than {}. The CLI reads this response as raw bytes " +
				"for that reason, so the catalogue is fully usable; the spec mismatch is " +
				"tracked in TODOS.md as an upstream fix.",
			Flags: []FlagDef{
				{Name: "kind", Type: "string", Description: "action|logic|trigger"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListWorkflowNodesParams{}
				if v := args.FlagString("kind"); v != "" {
					k := gen.ListWorkflowNodesParamsKind(v)
					p.Kind = &k
				}
				// Untyped read, exactly as workflows list/get do, and for the
				// identical reason: the spec types config as `object` but Laravel
				// serializes an empty one as `[]`, and the strict-typed decode
				// fails with "cannot unmarshal array into ... config of type
				// map[string]string". Every trigger node has an empty config, so
				// the TYPED path made the default invocation — the one the Long
				// text tells you to run first — fail outright, while --kind action
				// and --kind logic happened to work.
				//
				// This was logged as an upstream-only problem. It is not: the
				// remedy was already established in this file for the same cause,
				// five call sites away. The renderers consume raw bytes either way.
				resp, err := r.Client.ListWorkflowNodes(ctx, p)
				if err != nil {
					return nil, err
				}
				return readRawResponse(resp, "WorkflowNode", "")
			},
		},
		{
			Use: "list", Short: "List workflows", Kind: KindRead,
			Verb: "GET", Path: "/workflows", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Max: 200},
				{Name: "cursor", Type: "string"},
				{Name: "include-trashed", Type: "bool"},
				{Name: "since", Type: "string", Description: "ISO 8601 lower-bound on updated_at"},
				{Name: "status", Type: "string", Description: "draft|paused|active"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListWorkflowsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				if v := args.FlagString("since"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--since: invalid RFC3339 timestamp: %w", err)
					}
					p.Since = &t
				}
				if v := args.FlagString("status"); v != "" {
					s := gen.ListWorkflowsParamsStatus(v)
					p.Status = &s
				}
				// Raw HTTP path bypasses oapi-codegen's typed parse: the
				// spec types Workflow.trigger.config / WorkflowStep.config
				// as `object`, but Laravel serializes empty configs as `[]`
				// instead of `{}` and the strict-typed decode blows up. The
				// table/JSON renderers consume raw bytes either way.
				resp, err := r.Client.ListWorkflows(ctx, p)
				if err != nil {
					return nil, err
				}
				return readRawResponse(resp, "Workflow", "")
			},
		},
		{
			Use: "get <id>", Short: "Show a workflow with its trigger + step tree",
			Kind: KindRead, Verb: "GET", Path: "/workflows/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "include-trashed", Type: "bool"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				p := &gen.ShowWorkflowParams{}
				if args.FlagBool("include-trashed") {
					t := true
					p.IncludeTrashed = &t
				}
				resp, err := r.Client.ShowWorkflow(ctx, id, p)
				if err != nil {
					return nil, err
				}
				return readRawResponse(resp, "Workflow", id)
			},
		},
		{
			Use: "create", Short: "Create a workflow (DRAFT)", Kind: KindMutation,
			Verb: "POST", Path: "/workflows", Ability: invpkg.Write, DryRunMode: DryRunBody,
			Long: "Creates an empty workflow shell in DRAFT. Add a trigger and at least one " +
				"step, then run `activate <id>` to flip to ACTIVE.\n\n" +
				"--trigger and --steps compose the automation in this one request, and each " +
				"composed step's action_type has to fit its step_type: since spec 1.60.0 one " +
				"mismatched or misspelt pair refuses the WHOLE request with 422 " +
				"INVALID_STEP_CONFIG, taking the workflow and the other steps with it. The " +
				"CLI refuses the delay mismatches locally, naming the array index.\n\n" +
				"parent_ref is a branch, not an ordering. Since spec 1.61.0 it must name a " +
				"CONDITION or a LOOP — the only step types that run their children — and " +
				"naming a plain action or a delay is refused, where it used to write the " +
				"row and produce a workflow that silently stopped there. A step that simply " +
				"FOLLOWS another has no parent: omit parent_ref and give it a later order. " +
				"The CLI refuses this locally too, naming the array index, because the whole " +
				"request carries both the ref and the step it names.",
			Flags: []FlagDef{
				{Name: "name", Type: "string", Required: true, Description: "Workflow name (≤200 chars)"},
				{Name: "description", Type: "string"},
				{Name: "business-unit-id", Type: "int", Description: "Defaults to the caller's current business unit"},
				{Name: "max-credits-per-run", Type: "int", Description: "Per-execution credit ceiling (default 50)"},
				{Name: "notify-on-fail", Type: "bool"},
				{Name: "trigger", Type: "json", Description: "Trigger as JSON, or @file.json: {action_type, config?}. Run workflows nodes for the vocabulary"},
				{Name: "steps", Type: "json", Description: "Steps as JSON, or @file.json: [{step_type, action_type?, ref?, parent_ref?, ...}, ...], written alongside the workflow. parent_ref names an EARLIER step's ref and must be a condition or a loop (spec 1.61.0); a step that merely follows another omits it"},
			},
			// D20: `steps` and `trigger` ARE the automation this workflow will
			// run. The audit keeps only body_sha256, so leaving them out would
			// make a created workflow unreconstructable after the fact.
			ForensicFields: []string{"name", "business-unit-id", "max-credits-per-run", "notify-on-fail", "trigger", "steps"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":                "name",
					"description":         "description",
					"business-unit-id":    "business_unit_id",
					"max-credits-per-run": "max_credits_per_run",
					"notify-on-fail":      "notify_on_fail",
					"trigger":             "trigger",
					"steps":               "steps",
				})
				if err != nil {
					return nil, err
				}
				if err := assertComposedStepsFitActionTypes(body); err != nil {
					// WireBody so the audit row hashes what the gate judged,
					// not the raw --data fallback runMutation uses when a
					// closure returns no result.
					return &RunResult{WireBody: body}, err
				}
				if err := assertComposedParentRefsRunChildren(body); err != nil {
					// Same WireBody reason as the pairing gate above.
					return &RunResult{WireBody: body}, err
				}
				resp, err := r.Client.CreateWorkflowWithBodyWithResponse(ctx, &gen.CreateWorkflowParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Workflow", "")
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "update <id>", Short: "Update workflow shell fields",
			Kind: KindMutation, Verb: "PATCH", Path: "/workflows/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Long: "Updates name/description/notify_on_fail/max_credits_per_run. Allowed on ACTIVE — " +
				"these fields don't affect the executor. For status changes use activate/deactivate.",
			Flags: []FlagDef{
				{Name: "name", Type: "string", Description: "Workflow name (≤200 chars)"},
				{Name: "description", Type: "string"},
				{Name: "max-credits-per-run", Type: "int"},
				{Name: "notify-on-fail", Type: "bool"},
			},
			ForensicFields: []string{"name", "max-credits-per-run", "notify-on-fail"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":                "name",
					"description":         "description",
					"max-credits-per-run": "max_credits_per_run",
					"notify-on-fail":      "notify_on_fail",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateWorkflowWithBodyWithResponse(ctx, id, &gen.UpdateWorkflowParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Workflow", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "delete <id>", Short: "Soft-delete a workflow (cascades to steps)",
			Kind: KindMutation, Verb: "DELETE", Path: "/workflows/{id}",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := dryRunOnlyBody(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DestroyWorkflowWithBodyWithResponse(ctx, id, &gen.DestroyWorkflowParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Workflow", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "restore <id>", Short: "Restore a soft-deleted workflow",
			Kind: KindMutation, Verb: "POST", Path: "/workflows/{id}/restore",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Long: "Clears deleted_at and cascade-restores steps that were trashed by this " +
				"workflow's prior soft-delete. Idempotent on a live workflow.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := dryRunOnlyBody(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.RestoreWorkflowWithBodyWithResponse(ctx, id, &gen.RestoreWorkflowParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Workflow", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "activate <id>", Short: "Validate the tree and flip to ACTIVE",
			Kind: KindMutation, Verb: "POST", Path: "/workflows/{id}/activate",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Long: "Runs WorkflowActivationValidator over the full step tree. On failure, " +
				"returns 422 WORKFLOW_NOT_ACTIVATABLE with one entry per check in " +
				"error.details.errors[] (NO_TRIGGER, NO_STEPS, ORPHAN_PARENT_REF, " +
				"INVALID_STEP_CONFIG, INVALID_STEP_TYPE, CREDIT_LIMIT_EXCEEDED). " +
				"Idempotent on an already-ACTIVE workflow.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := dryRunOnlyBody(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ActivateWorkflowWithBodyWithResponse(ctx, id, &gen.ActivateWorkflowParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Workflow", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "deactivate <id>", Short: "Flip to PAUSED (idempotent)",
			Kind: KindMutation, Verb: "POST", Path: "/workflows/{id}/deactivate",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"id"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := dryRunOnlyBody(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DeactivateWorkflowWithBodyWithResponse(ctx, id, &gen.DeactivateWorkflowParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "Workflow", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
	}
}

// workflowsTriggerCmd builds the `workflows trigger {create,update}` subtree.
// Both verbs take the workflow ID as a positional and carry a typed `config`
// JSON object validated server-side against the resolved trigger's rules().
func workflowsTriggerCmd(runner *Runner) *cobra.Command {
	parent := &cobra.Command{
		Use:   "trigger",
		Short: "Manage the workflow trigger (singleton per workflow)",
		Long: "POST replaces the trigger atomically (force-deletes the existing one if any); " +
			"PATCH updates only the trigger's `config`. The trigger's action_type is immutable " +
			"via PATCH — to change it, run `trigger create` (which atomically replaces).\n\n" +
			"Three action types joined the enum in spec 1.55.0. whatsapp_message_received " +
			"fires when a guest sends an inbound WhatsApp message to the account's connected " +
			"number; it takes no config and fires once per inbound message, and messages the " +
			"account SENDS do not fire it. segment_entered and segment_left were accepted " +
			"from the day they shipped but were never written down, so the CLI has been " +
			"refusing two live trigger types locally.",
	}
	bindCommands(parent, []CommandDef{
		{
			Use: "create <workflow-id>", Short: "Create or replace the trigger (singleton)",
			Kind: KindMutation, Verb: "POST", Path: "/workflows/{id}/trigger",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"workflow-id"},
			Flags: []FlagDef{
				{Name: "action-type", Type: "string", Required: true, Description: "booking_confirmed|booking_cancelled|booking_changed|booking_rescheduled|booking_transaction_processed|documents_signed_complete|customer_created|diary_notes_updated|scheduled_time|webhook|unknown_booking_cancellation|booking_resource_attached|booking_resource_detached|auxiliary_resource_attached_to_booking|auxiliary_resource_detached_from_booking|all_mandatory_questions_answered|abandoned_booking|custom_attribute_updated|waitlist_joined|segment_entered|segment_left|whatsapp_message_received"},
				{Name: "config", Type: "string", Description: "JSON object validated against the resolved trigger's rules() schema (literal or @file.json)"},
			},
			ForensicFields: []string{"action-type"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := triggerOrStepBody(args, map[string]string{
					"action-type": "action_type",
				})
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.CreateWorkflowTriggerWithBodyWithResponse(ctx, id, &gen.CreateWorkflowTriggerParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "WorkflowTrigger", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "update <workflow-id>", Short: "Update trigger config only",
			Kind: KindMutation, Verb: "PATCH", Path: "/workflows/{id}/trigger",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"workflow-id"},
			Long: "Replaces the trigger's `config`. action_type is immutable here — run " +
				"`trigger create` to atomically replace. 404 if no trigger exists yet. " +
				"Supply config via --config (typed flag) OR --data (whole-body JSON, " +
				"useful for `trigger get | jq .config | trigger update --data @-` round-trips).",
			Flags: []FlagDef{
				{Name: "config", Type: "string", Description: "JSON object (literal or @file.json); optional if --data carries config"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := triggerOrStepBody(args, nil)
				if err != nil {
					return nil, err
				}
				if err := requireBodyKey(body, "config", "--config or --data must supply the trigger `config` field"); err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateWorkflowTriggerWithBodyWithResponse(ctx, id, &gen.UpdateWorkflowTriggerParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "WorkflowTrigger", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
	}, runner)
	return parent
}

// workflowsStepsCmd builds the `workflows steps {create,update,delete}` subtree.
// Step structure (parent_step_id, branch_type, order, step_type, action_type)
// is immutable post-create — update only patches `config`.
func workflowsStepsCmd(runner *Runner) *cobra.Command {
	parent := &cobra.Command{
		Use:   "steps",
		Short: "Manage workflow steps (tree-shaped, non-trigger)",
		Long: "Creates ACTION/CONDITION/DELAY/DELAY_UNTIL/LOOP steps. TRIGGER steps live " +
			"under `workflows trigger`. Step structure is immutable post-create — to move or " +
			"reshape a step, delete and re-create.\n\n" +
			"A step's action_type has to fit the step_type holding it, and since spec 1.60.0 " +
			"the server refuses a pair that does not at WRITE time with 422 " +
			"INVALID_STEP_CONFIG: step_type=delay takes action_type=delay and nothing else, " +
			"delay_until takes delay_until, step_type=action takes any other action type, and " +
			"a misspelt type (sendEmail for send_email) is refused rather than resolved. " +
			"Both used to be accepted with a 201 and then fail every run; activation now " +
			"refuses such a workflow too, with an INVALID_STEP_TYPE entry naming the step.\n\n" +
			"A step stored with a type that no longer exists reads back with that stored " +
			"name in action_type — a value outside TriggerType/ActionType — rather than a " +
			"500. It cannot be patched (the PATCH is refused with a 422 naming it) and the " +
			"workflow cannot be activated until the step is deleted and re-created.\n\n" +
			"--parent-step-id is NOT \"the step before me\". It means \"I am inside this " +
			"step's branch\", so since spec 1.61.0 only a CONDITION or a LOOP may be named: " +
			"those are the only step types that run their children, and the executor skips " +
			"anything whose parent it has already run. Naming any other step is a 422, where " +
			"it used to return 201 and build a workflow that silently stopped there. A step " +
			"that simply FOLLOWS another has no parent — omit the flag and give it a later " +
			"--order. The CLI cannot pre-empt this one: the parent is a row that already " +
			"exists server-side and its step_type is not in the request, so the 422 is the " +
			"server's. The composed form (`workflows create --steps`) IS refused locally, " +
			"because there the whole tree is in one body.\n\n" +
			"Credential fields inside a step's config — api_key, secret, token, password — " +
			"always read back as an EMPTY STRING since spec 1.63.0, whatever is stored: a " +
			"read is governed by view_any_workflow, which is not authority over a credential " +
			"the account installed. The write side pairs with it, and this is the part that " +
			"bites: `update` REPLACES the whole config, but an empty or omitted credential " +
			"field means \"leave the stored value alone\", not \"clear it\" — so the obvious " +
			"get, edit one field, send it back no longer posts the withheld blank over a " +
			"live key. Send a real value to replace one. A credential cannot be cleared " +
			"through this API: delete the step, or clear it in the builder.",
	}
	bindCommands(parent, []CommandDef{
		{
			Use: "create <workflow-id>", Short: "Create a workflow step (tree-shaped)",
			Kind: KindMutation, Verb: "POST", Path: "/workflows/{id}/steps",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"workflow-id"},
			Flags: []FlagDef{
				{Name: "step-type", Type: "string", Required: true, Description: "action|condition|delay|delay_until|loop"},
				{Name: "action-type", Type: "string", Description: "Required for action/delay/delay_until; optional for loop and condition, which ignore it. See ActionType enum. Must fit --step-type: delay takes delay, delay_until takes delay_until, action takes neither."},
				{Name: "condition-type", Type: "string", Description: "value_check|filter (required for condition step_type)"},
				{Name: "config", Type: "string", Description: "JSON object validated against the resolved action's rules() (literal or @file.json). send_notification takes continue_on_error (bool) since spec 1.62.0. Credential fields (api_key, secret, token, password) are withheld on read and left alone when sent empty or omitted (1.63.0)"},
				{Name: "order", Type: "int", Required: true, Description: "Position within the parent branch (≥0)"},
				{Name: "parent-step-id", Type: "int", Description: "Parent step in the tree. Must be a CONDITION or a LOOP — the only types that run children (spec 1.61.0 refuses any other with 422). Means \"inside this step's branch\", not \"after this step\": a step that merely follows another omits this and takes a later --order"},
				{Name: "branch-type", Type: "string", Description: "true|false — required when --parent-step-id is set"},
			},
			ForensicFields: []string{"step-type", "action-type", "condition-type", "order", "parent-step-id", "branch-type"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := pathArg(args)
				if err != nil {
					return nil, err
				}
				body, err := triggerOrStepBody(args, map[string]string{
					"step-type":      "step_type",
					"action-type":    "action_type",
					"condition-type": "condition_type",
					"order":          "order",
					"parent-step-id": "parent_step_id",
					"branch-type":    "branch_type",
				})
				if err != nil {
					return nil, err
				}
				if err := assertStepPairFitsActionType(body); err != nil {
					// WireBody for the same reason as the composed gate above.
					return &RunResult{WireBody: body}, err
				}
				resp, err := r.Client.CreateWorkflowStepWithBodyWithResponse(ctx, id, &gen.CreateWorkflowStepParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "WorkflowStep", id)
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "update <workflow-id> <step-id>", Short: "Update step config only",
			Kind: KindMutation, Verb: "PATCH", Path: "/workflows/{id}/steps/{stepId}",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"workflow-id", "step-id"},
			Long: "Updates only the step's `config` payload. parent_step_id, branch_type, " +
				"order, step_type, action_type, and condition_type are immutable post-create — " +
				"delete and re-create to move or reshape. Supply config via --config (typed " +
				"flag) OR --data (whole-body JSON, useful for read-modify-write loops).\n\n" +
				"This PATCH replaces the WHOLE config, so a read-modify-write loop has to " +
				"send back every field it wants to keep. Credential fields are the one " +
				"exception, and they need it: api_key, secret, token and password read back " +
				"as an empty string since spec 1.63.0 (a read is not authority over a stored " +
				"credential), so on write an empty or omitted one means \"leave the stored " +
				"value alone\", NOT \"clear it\". Without that pairing the loop above would " +
				"post the withheld blank over a working key. Send a real value to replace " +
				"one; a credential cannot be cleared through this API (delete the step, or " +
				"clear it in the builder).",
			Flags: []FlagDef{
				{Name: "config", Type: "string", Description: "JSON object (literal or @file.json); optional if --data carries config. Replaces the whole config, except that an empty or omitted credential field leaves the stored value alone (spec 1.63.0)"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				workflowID, stepID, err := workflowStepArgs(args)
				if err != nil {
					return nil, err
				}
				body, err := triggerOrStepBody(args, nil)
				if err != nil {
					return nil, err
				}
				if err := requireBodyKey(body, "config", "--config or --data must supply the step `config` field"); err != nil {
					return nil, err
				}
				resp, err := r.Client.UpdateWorkflowStepWithBodyWithResponse(ctx, workflowID, stepID, &gen.UpdateWorkflowStepParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "WorkflowStep", strconv.Itoa(stepID))
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
		{
			Use: "delete <workflow-id> <step-id>", Short: "Soft-delete a workflow step",
			Kind: KindMutation, Verb: "DELETE", Path: "/workflows/{id}/steps/{stepId}",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"workflow-id", "step-id"},
			Long: "Deletes exactly the step named, and never cascades. Since spec 1.57.0 a " +
				"condition, filter or loop that still has steps inside it is refused with " +
				"422, and the message names the children — so delete a branch depth-first, " +
				"deepest step first.\n\n" +
				"It previously deleted the named step alone and left its children pointing " +
				"at a soft-deleted parent: activation then failed with ORPHAN_PARENT_REF, " +
				"and WorkflowExecutor ran those children as ROOT steps, so a branch fired " +
				"unconditionally. A script that deleted a parent and expected the subtree " +
				"to go with it now gets the 422 instead of a silently broken workflow — " +
				"read the child ids out of the message and delete those first.\n\n" +
				"Triggers are not deletable here either (that is a separate 422): replace " +
				"one by POSTing `workflows trigger create`.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				workflowID, stepID, err := workflowStepArgs(args)
				if err != nil {
					return nil, err
				}
				body, err := dryRunOnlyBody(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.DestroyWorkflowStepWithBodyWithResponse(ctx, workflowID, stepID, &gen.DestroyWorkflowStepParams{IdempotencyKey: args.IdempotencyKeyUUID}, "application/json", asReader(body))
				if err != nil {
					return &RunResult{WireBody: body}, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "WorkflowStep", strconv.Itoa(stepID))
				if res != nil {
					res.WireBody = body
				}
				return res, perr
			},
		},
	}, runner)
	return parent
}

// workflowExecutionsCmd builds the `inventory workflow-executions` subtree.
// Read-only in v1 — no trigger/cancel/retry endpoints. Support uses the
// Builder UI for those.
func workflowExecutionsCmd(runner *Runner) *cobra.Command {
	parent := &cobra.Command{
		Use:   "workflow-executions",
		Short: "Read workflow executions and their per-step logs",
	}
	bindCommands(parent, workflowExecutionsDefs(), runner)
	return parent
}

func workflowExecutionsDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "list", Short: "List workflow executions", Kind: KindRead,
			Verb: "GET", Path: "/workflow-executions", Ability: invpkg.Read,
			Long: "Default ordering: latest first (created_at DESC, id DESC) — the most " +
				"common support query is \"what failed in the last hour\".",
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Max: 200},
				{Name: "cursor", Type: "string"},
				{Name: "workflow-id", Type: "string", Description: "Filter to one workflow (UUID)"},
				{Name: "status", Type: "string", Description: "pending|running|waiting|completed|failed"},
				{Name: "fail-reason", Type: "string", Description: "e.g. workflow_deleted_mid_run"},
				{Name: "date-from", Type: "string", Description: "RFC3339 lower-bound on created_at"},
				{Name: "date-to", Type: "string", Description: "RFC3339 upper-bound on created_at"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				p := &gen.ListWorkflowExecutionsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if v := args.FlagString("workflow-id"); v != "" {
					p.WorkflowId = &v
				}
				if v := args.FlagString("status"); v != "" {
					s := gen.ListWorkflowExecutionsParamsStatus(v)
					p.Status = &s
				}
				if v := args.FlagString("fail-reason"); v != "" {
					p.FailReason = &v
				}
				if v := args.FlagString("date-from"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--date-from: invalid RFC3339 timestamp: %w", err)
					}
					p.DateFrom = &t
				}
				if v := args.FlagString("date-to"); v != "" {
					t, err := time.Parse(time.RFC3339, v)
					if err != nil {
						return nil, fmt.Errorf("--date-to: invalid RFC3339 timestamp: %w", err)
					}
					p.DateTo = &t
				}
				resp, err := r.Client.ListWorkflowExecutions(ctx, p)
				if err != nil {
					return nil, err
				}
				return readRawResponse(resp, "WorkflowExecution", "")
			},
		},
		{
			Use: "get <id>", Short: "Show an execution (with embedded failed log)",
			Kind: KindRead, Verb: "GET", Path: "/workflow-executions/{id}", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Long: "Returns the execution row plus `failed_log` — the first " +
				"WorkflowExecutionLog row with status=failed, so support can debug a failed " +
				"run without a second /logs round-trip.",
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := executionIDArg(args)
				if err != nil {
					return nil, err
				}
				resp, err := r.Client.ShowWorkflowExecution(ctx, id)
				if err != nil {
					return nil, err
				}
				return readRawResponse(resp, "WorkflowExecution", strconv.Itoa(id))
			},
		},
		{
			Use: "logs <id>", Short: "Paginated per-step logs for an execution",
			Kind: KindRead, Verb: "GET", Path: "/workflow-executions/{id}/logs", Ability: invpkg.Read,
			PositionalArgs: []string{"id"},
			Flags: []FlagDef{
				{Name: "limit", Type: "int", Min: 1, Max: 200},
				{Name: "cursor", Type: "string"},
				{Name: "step-id", Type: "int", Min: 1, Description: "Filter to one step's history"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				id, err := executionIDArg(args)
				if err != nil {
					return nil, err
				}
				p := &gen.ListWorkflowExecutionLogsParams{}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				if args.FlagSet("step-id") {
					sid := args.FlagInt("step-id")
					p.StepId = &sid
				}
				resp, err := r.Client.ListWorkflowExecutionLogs(ctx, id, p)
				if err != nil {
					return nil, err
				}
				return readRawResponse(resp, "WorkflowExecutionLog", strconv.Itoa(id))
			},
		},
	}
}

// readRawResponse drains a raw *http.Response and feeds (body, response)
// into ParseGenResponse. Used by workflow reads to bypass the gen client's
// strict typed parse — the spec types config payloads as `object` but the
// server returns `[]` for empty configs (Laravel idiom), and the typed
// decode rejects that with "cannot unmarshal array into map[string]any".
// The CLI's renderers only need raw bytes, so going around the typed
// parse is harmless here.
func readRawResponse(resp *http.Response, resourceType, resourceID string) (*RunResult, error) {
	if resp == nil {
		return nil, fmt.Errorf("inventory: gen client returned nil *http.Response")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	return ParseGenResponse(body, resp, resourceType, resourceID)
}

// dryRunOnlyBody builds the body for the {dry_run} endpoints (workflow
// delete/restore/activate/deactivate, step delete). Allows --data to push
// debug fields onto the wire, then overlays dry_run when set.
func dryRunOnlyBody(args RunArgs) ([]byte, error) {
	body := map[string]any{}
	if len(args.RawData) > 0 {
		if err := json.Unmarshal(args.RawData, &body); err != nil {
			return nil, fmt.Errorf("--data: invalid JSON: %w", err)
		}
	}
	if args.DryRun {
		body["dry_run"] = true
	}
	return json.Marshal(body)
}

// triggerOrStepBody builds the body for trigger/step create/update. Typed
// flags listed in fieldMap are overlaid onto --data; --config (when set)
// is parsed as JSON and assigned to body["config"] so the server sees a real
// object, not a JSON-encoded string. Mirrors JSONBodyFromArgs's --data-first
// ordering so the closure stays consistent with the rest of the mutation
// surface.
func triggerOrStepBody(args RunArgs, fieldMap map[string]string) ([]byte, error) {
	body := map[string]any{}
	if len(args.RawData) > 0 {
		if err := json.Unmarshal(args.RawData, &body); err != nil {
			return nil, fmt.Errorf("--data: invalid JSON: %w", err)
		}
	}
	for flagName, jsonKey := range fieldMap {
		v, ok := args.Flags[flagName]
		if !ok {
			continue
		}
		body[jsonKey] = v
	}
	if v := args.FlagString("config"); v != "" {
		raw, err := readDataFlag(v)
		if err != nil {
			return nil, fmt.Errorf("--config: %w", err)
		}
		// Unmarshal into `any` (not `map[string]any`) so an empty array
		// (Laravel's serialization for empty configs) round-trips cleanly
		// when piping read output back as input.
		var parsed any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("--config: invalid JSON: %w", err)
		}
		body["config"] = parsed
	}
	if args.DryRun {
		body["dry_run"] = true
	}
	return json.Marshal(body)
}

// assertStepPairFitsActionType refuses, before the request leaves, a top-level
// (step_type, action_type) pair that spec 1.60.0 made a write-time 422
// INVALID_STEP_CONFIG. It belongs to `workflows steps create`, whose request
// body IS one step.
//
// Scoped to this one route on purpose. An earlier version judged the top-level
// pair AND a `steps` array on both routes, on the theory that a body only ever
// carries one of the two. `--data` makes that false: neither request schema
// forbids extra keys, so `workflows create --name x --data
// '{"step_type":"delay","action_type":"send_email"}'` carried a pair the server
// simply ignores (CreateWorkflowRequest does not declare those fields), and the
// gate refused a shell create the server would have accepted. A gate whose
// whole justification is "never refuse what the server accepts" cannot afford
// that, so each route now reads only the shape it actually sends.
//
// Reads the assembled body rather than the flags so --data is covered too:
// triggerOrStepBody merges --data first and overlays the typed flags, and a
// mismatched pair pushed in through --data is the same wire request.
func assertStepPairFitsActionType(body []byte) error {
	m, err := decodeBodyFields(body)
	if err != nil {
		return err
	}
	stepType, _ := m["step_type"].(string)
	actionType, _ := m["action_type"].(string)
	if problem := stepPairProblem(stepType, actionType); problem != "" {
		return stepPairRefusal(problem)
	}
	return nil
}

// assertComposedStepsFitActionTypes applies the same rule to each element of
// the `steps` array that `workflows create` composes in one request. It is
// worth its own gate: the server refuses the WHOLE request on one bad pair, so
// a mismatch in step 7 costs the workflow, the trigger and the other six.
//
// Only `steps` is read here, for the reason above: `workflows create`'s own
// body has no step of its own, and an extra top-level key is the caller's to
// send.
func assertComposedStepsFitActionTypes(body []byte) error {
	m, err := decodeBodyFields(body)
	if err != nil {
		return err
	}
	steps, _ := m["steps"].([]any)
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			// Not an object: let the server's own validator name the shape
			// problem rather than inventing a second vocabulary for it.
			continue
		}
		st, _ := step["step_type"].(string)
		at, _ := step["action_type"].(string)
		if problem := stepPairProblem(st, at); problem != "" {
			return stepPairRefusal(fmt.Sprintf("steps[%d]: %s", i, problem))
		}
	}
	return nil
}

// assertComposedParentRefsRunChildren refuses, before the request leaves, a
// `parent_ref` naming a composed step that never runs children — the 422 spec
// 1.61.0 made explicit.
//
// `parent_ref` does not mean "the step before me". It means "I am inside this
// step's branch", and only a CONDITION or a LOOP runs children: every other
// step type is marked processed the moment it runs, and the executor then skips
// anything parented to it. Before 1.61.0 the server wrote the row, answered
// 201, and produced a workflow that silently stopped at that step.
//
// Local on this route only, and only for the cases the request itself settles.
// `--steps` carries every step AND its `ref` in one body, so the named parent's
// step_type is right there to read. `workflows steps create --parent-step-id`
// names a row that already exists server-side, whose type the CLI cannot know
// without a fetch it does not make; that half stays the server's 422 and is
// documented as such on the flag.
//
// Worth its own gate for the same reason the pairing gate is: the server
// refuses the WHOLE request on one bad ref, so a mistake in step 7 costs the
// workflow, the trigger and the other six.
func assertComposedParentRefsRunChildren(body []byte) error {
	m, err := decodeBodyFields(body)
	if err != nil {
		return err
	}
	steps, _ := m["steps"].([]any)

	// Resolve refs over the whole array rather than only the elements before
	// the referrer. Position is a separate rule with its own 422 (a forward or
	// unknown ref), which the server's validator already names; reading every
	// ref keeps this gate to the ONE question it can answer with certainty —
	// whether the named step is a kind that runs children.
	refType := make(map[string]string, len(steps))
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ref, _ := step["ref"].(string)
		if ref == "" {
			continue
		}
		if _, dup := refType[ref]; dup {
			// A duplicate ref is itself a 422 (refs must be unique within the
			// request). Keep the first and do not let a second definition
			// decide a refusal.
			continue
		}
		refType[ref] = stepTypeOf(step)
	}

	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parentRef, _ := step["parent_ref"].(string)
		if parentRef == "" {
			continue
		}
		parentType, known := refType[parentRef]
		if !known {
			// An unknown or forward ref: the server's own 422 names it, and it
			// was a 422 before 1.61.0 too. Inventing a second vocabulary for
			// it is the mistake the non-object arm above is written to avoid.
			continue
		}
		if problem := parentRefProblem(parentRef, parentType); problem != "" {
			return parentRefRefusal(fmt.Sprintf("steps[%d]: %s", i, problem))
		}
	}
	return nil
}

// stepTypeOf reads a composed step's declared step_type, or "" when it is
// absent or not a string. "" reaches parentRefProblem's default arm, which
// refuses nothing.
func stepTypeOf(step map[string]any) string {
	st, _ := step["step_type"].(string)
	return st
}

// parentRefProblem returns a sentence saying why a step of parentType cannot
// parent another step, or "" for a parent the server accepts — or one this
// gate cannot judge.
//
// The arms are explicit rather than a negated condition/loop test so that a
// step_type the spec adds after this build falls through to "" instead of being
// refused by a CLI that has not heard of it. The spec enumerates step_type as
// [action, condition, delay, delay_until, loop], so the two arms below cover
// the whole of today's vocabulary; `trigger` (not accepted in a composed
// `steps` array at all), an empty value and anything new are the server's call.
func parentRefProblem(parentRef, parentType string) string {
	switch parentType {
	case "condition", "loop":
		return ""
	case "action", "delay", "delay_until":
		// Phrased as "whose step_type is X" rather than "an X step" so the
		// sentence stays grammatical for all three values (a/an).
		return fmt.Sprintf("parent_ref %q names a step whose step_type is %s, and only a "+
			"condition or a loop runs its children, so this step would be written and then "+
			"never run. A step that simply FOLLOWS another has no parent — omit parent_ref "+
			"and order it after",
			parentRef, parentType)
	}
	return ""
}

// parentRefRefusal wraps a parentRefProblem sentence in the exit error, with
// the part the sentence cannot carry: why it is refused at all, and that one
// bad ref costs the whole composed request.
func parentRefRefusal(problem string) error {
	return &api.ExitError{
		Err: fmt.Errorf(
			"%s. The executor skips anything whose parent it has already run, so the "+
				"server refuses the parent outright (spec 1.61.0) — and it refuses the "+
				"WHOLE request, taking the workflow, the trigger and every other step "+
				"with it",
			problem,
		),
		Code: api.ExitValidation,
	}
}

// decodeBodyFields decodes an assembled request body for the gates above.
//
// UseNumber keeps every JSON number as its literal text instead of converting
// it to float64, so a value outside float64's range — `1e400` is legal JSON the
// server may well accept — cannot fail the decode and make the gate refuse a
// request over a number in a field it does not even read. Measured: with
// UseNumber the gate returns nil for such a body. Note the refusal an operator
// actually sees for `--data '{"x":1e400}'` today comes from further upstream,
// in triggerOrStepBody's own `json.Unmarshal` into `map[string]any`
// (`--data: invalid JSON: ... into Go value of type float64`); that is
// pre-existing and untouched here.
func decodeBodyFields(body []byte) (map[string]any, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("validating request body: %w", err)
	}
	return m, nil
}

// stepPairProblem returns a sentence saying why action_type cannot sit in a
// step of this step_type, or "" for a pair the server accepts.
//
// The server decides this in PolymorphicWorkflowStepTypeEnumCast::isKnown; the
// two arms below are the subset of it that can be decided without the
// ActionType enum. The rest of that rule is "action_type is a real ActionType",
// and the spec never enumerates ActionType — it points at `workflows nodes`,
// whose catalogue is the authority and is per-tenant. A local list would be a
// second contract that drifts, and would refuse a type the day upstream ships
// it (the trigger's hand-maintained enum run did exactly that, twice).
//
// condition and loop are absent on purpose rather than by omission: the
// server's resolver returns their evaluator whatever action_type says, so any
// value is legal there and refusing one would make a documented step
// unreachable from the CLI. An absent or empty action_type is likewise the
// server's call — it is required for action / delay / delay_until and genuinely
// optional for loop.
func stepPairProblem(stepType, actionType string) string {
	if actionType == "" {
		return ""
	}
	switch stepType {
	case "delay", "delay_until":
		// want == stepType for both: a delay step runs a delay and nothing else.
		if actionType != stepType {
			return fmt.Sprintf("step_type %s takes action_type %s and nothing else, got %q",
				stepType, stepType, actionType)
		}
	case "action":
		if actionType == "delay" || actionType == "delay_until" {
			return fmt.Sprintf("step_type action cannot carry action_type %q — a delay runs as "+
				"its own step type, so use step_type %s instead", actionType, actionType)
		}
	}
	return ""
}

// stepPairRefusal wraps a stepPairProblem sentence in the exit error, with the
// part the sentence cannot carry: why the pair is refused at all, and where to
// look up the types.
func stepPairRefusal(problem string) error {
	return &api.ExitError{
		Err: fmt.Errorf(
			"%s. The executor picks its path from the step type, so a mismatched pair "+
				"fails every run — the server refuses it with 422 INVALID_STEP_CONFIG "+
				"(spec 1.60.0). `ceebee inventory workflows nodes` lists the action types",
			problem,
		),
		Code: api.ExitValidation,
	}
}

// requireBodyKey returns an error when the assembled body is missing a
// field that the server requires. Used by trigger/step update closures
// where we accept config from either --config or --data and want to
// fail-fast client-side instead of round-tripping a 422.
//
// Takes the marshaled body bytes (what triggerOrStepBody returns) and
// re-decodes — slightly redundant but keeps the API simple for closures
// that already have the bytes in hand. A nil JSON value (`"config":null`)
// counts as missing.
func requireBodyKey(body []byte, key, msg string) error {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		// If the body doesn't round-trip as a map we'd have failed
		// earlier in triggerOrStepBody. Surface the unmarshal error
		// rather than silently passing.
		return fmt.Errorf("validating request body: %w", err)
	}
	v, ok := m[key]
	if !ok || v == nil {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// workflowStepArgs unpacks the (workflow-id, step-id) positional pair for
// step update/delete. workflow ID is a UUID string; step ID is an int.
func workflowStepArgs(args RunArgs) (string, int, error) {
	if len(args.PathArgs) < 2 {
		return "", 0, fmt.Errorf("workflows steps requires <workflow-id> <step-id>")
	}
	stepID, err := strconv.Atoi(args.PathArgs[1])
	if err != nil {
		return "", 0, fmt.Errorf("step-id must be an integer, got %q", args.PathArgs[1])
	}
	return args.PathArgs[0], stepID, nil
}

// executionIDArg parses the single integer positional ID for
// workflow-executions {get,logs}.
func executionIDArg(args RunArgs) (int, error) {
	if len(args.PathArgs) == 0 {
		return 0, fmt.Errorf("workflow-executions requires <id>")
	}
	id, err := strconv.Atoi(args.PathArgs[0])
	if err != nil {
		return 0, fmt.Errorf("id must be an integer, got %q", args.PathArgs[0])
	}
	return id, nil
}
