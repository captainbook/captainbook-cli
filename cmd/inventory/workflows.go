package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

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
			Use: "list", Short: "List workflows", Kind: KindRead,
			Verb: "GET", Path: "/workflows", Ability: invpkg.Read,
			Flags: []FlagDef{
				{Name: "limit", Type: "int"},
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
				"step, then run `activate <id>` to flip to ACTIVE.",
			Flags: []FlagDef{
				{Name: "name", Type: "string", Required: true, Description: "Workflow name (≤200 chars)"},
				{Name: "description", Type: "string"},
				{Name: "business-unit-id", Type: "int", Description: "Defaults to the caller's current business unit"},
				{Name: "max-credits-per-run", Type: "int", Description: "Per-execution credit ceiling (default 50)"},
				{Name: "notify-on-fail", Type: "bool"},
			},
			ForensicFields: []string{"name", "business-unit-id", "max-credits-per-run", "notify-on-fail"},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				body, err := JSONBodyFromArgs(args, args.DryRun, map[string]string{
					"name":                "name",
					"description":         "description",
					"business-unit-id":    "business_unit_id",
					"max-credits-per-run": "max_credits_per_run",
					"notify-on-fail":      "notify_on_fail",
				})
				if err != nil {
					return nil, err
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
			"via PATCH — to change it, run `trigger create` (which atomically replaces).",
	}
	bindCommands(parent, []CommandDef{
		{
			Use: "create <workflow-id>", Short: "Create or replace the trigger (singleton)",
			Kind: KindMutation, Verb: "POST", Path: "/workflows/{id}/trigger",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"workflow-id"},
			Flags: []FlagDef{
				{Name: "action-type", Type: "string", Required: true, Description: "booking_confirmed|booking_cancelled|booking_changed|booking_rescheduled|booking_transaction_processed|documents_signed_complete|customer_created|diary_notes_updated|scheduled_time|webhook|unknown_booking_cancellation|booking_resource_attached|booking_resource_detached|auxiliary_resource_attached_to_booking|auxiliary_resource_detached_from_booking|all_mandatory_questions_answered|abandoned_booking|custom_attribute_updated|waitlist_joined"},
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
				"`trigger create` to atomically replace. 404 if no trigger exists yet.",
			Flags: []FlagDef{
				{Name: "config", Type: "string", Required: true, Description: "JSON object (literal or @file.json)"},
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
			"reshape a step, delete and re-create.",
	}
	bindCommands(parent, []CommandDef{
		{
			Use: "create <workflow-id>", Short: "Create a workflow step (tree-shaped)",
			Kind: KindMutation, Verb: "POST", Path: "/workflows/{id}/steps",
			Ability: invpkg.Write, DryRunMode: DryRunBody, PositionalArgs: []string{"workflow-id"},
			Flags: []FlagDef{
				{Name: "step-type", Type: "string", Required: true, Description: "action|condition|delay|delay_until|loop"},
				{Name: "action-type", Type: "string", Description: "Required for action/delay/delay_until/loop. See ActionType enum."},
				{Name: "condition-type", Type: "string", Description: "value_check|filter (required for condition step_type)"},
				{Name: "config", Type: "string", Description: "JSON object validated against the resolved action's rules() (literal or @file.json)"},
				{Name: "order", Type: "int", Required: true, Description: "Position within the parent branch (≥0)"},
				{Name: "parent-step-id", Type: "int", Description: "Parent step in the tree (CONDITION/LOOP children)"},
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
				"delete and re-create to move or reshape.",
			Flags: []FlagDef{
				{Name: "config", Type: "string", Required: true, Description: "JSON object (literal or @file.json)"},
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
				{Name: "limit", Type: "int"},
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
				{Name: "limit", Type: "int"},
				{Name: "cursor", Type: "string"},
				{Name: "step-id", Type: "int", Description: "Filter to one step's history"},
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
