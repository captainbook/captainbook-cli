package inventory

// Wire-level capture for the cli-v1 1.56.0 → 1.60.0 sync.
//
// This sync adds no operation and no flag, which is exactly why it needs its
// own file: everything it changes lives on the two edges the drift tests do not
// watch. One edge is a local gate that must refuse BEFORE the request (1.60.0's
// step_type/action_type pairing) — a gate that let the call through and leaned
// on the server's 422 would still read as a gate by inspection while costing a
// round trip, so each refusal case asserts that nothing reached the server, and
// each legal pair asserts that it did. (The audit row is written either way:
// runMutation appends it at inventory.go:690, after the closure returns and
// regardless of the error it returned, so a refusal from INSIDE the closure —
// this gate — still records the command and its forensic fields, and only the
// round trip is saved. A refusal that happens earlier than the closure, such as
// the ability gate at inventory.go:601, returns before that Append and records
// nothing.) The other is the error
// path: three refusals changed shape or meaning (1.57.0's child-bearing step
// delete, 1.58.0's trial 403, 1.60.0's INVALID_STEP_CONFIG), and a code that
// parses into the wrong type still prints SOMETHING, so only an assertion on
// the rendered UserMessage can tell a working mapping from a plausible one.
//
// Both halves drive the real registered tree through the testNewRunner seam.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/captainbook/captainbook-cli/internal/api"
	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
)

// stepArgs is a minimal VALID `workflows steps create` invocation, so each case
// below differs from a call that works by exactly the thing it claims to
// refuse. pflag takes the LAST occurrence of a repeated flag, so appending
// overrides a baseline value without restating the rest.
func stepArgs(extra ...string) []string {
	return append([]string{"workflows", "steps", "create",
		"7b9e5a2c-0000-4000-8000-000000000001",
		"--step-type", "delay",
		"--action-type", "delay",
		"--order", "0",
	}, extra...)
}

// refuseAgainst runs the real tree against a stub that answers every request
// with one canned refusal, and reports whether the request arrived.
//
// `hit` is load-bearing in both directions here: a local gate has to show
// hit=false, and an error-path assertion is worthless unless the request it
// claims to be reading a response to was actually made.
func refuseAgainst(t *testing.T, status int, respBody string, args ...string) (hit bool, err error) {
	t.Helper()

	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	})

	// sharedRunner is a package singleton and PersistentPreRunE short-circuits
	// once its Client is set, so without this reset every later call would keep
	// talking to the first (already closed) stub.
	prevShared := sharedRunner
	sharedRunner = &Runner{}
	prev := testNewRunner
	testNewRunner = func() (*Runner, error) { return runner, nil }
	t.Cleanup(func() {
		testNewRunner = prev
		sharedRunner = prevShared
	})

	root := Cmd()
	root.SetArgs(args)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err = root.Execute()
	return hit, err
}

// userMessage renders err the way cmd/root.go would, failing the test if the
// error cannot be rendered that way at all.
func userMessage(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("no error to render")
	}
	var um invpkg.UserMessenger
	if !errors.As(err, &um) {
		t.Fatalf("%T does not implement UserMessenger, so cmd/root.go prints Error() instead: %v", err, err)
	}
	return um.UserMessage()
}

// -----------------------------------------------------------------------------
// 1.60.0 — the step_type/action_type pairing gate
// -----------------------------------------------------------------------------

// TestWire_MismatchedStepAndActionTypeNeverReachTheServer covers the two
// mismatches the spec's 1.60.0 entry names, from both sides: a delay step
// holding an action type, and an action step holding a delay.
//
// Value: protects=a (step_type, action_type) pair the server refuses with 422 INVALID_STEP_CONFIG is refused locally, before the request; fails_when=the gate is dropped from the steps create closure, or stops reading the assembled body so --data walks around it; why_new=the refusal is new in this sync and the CLI had no local check at all; seam=captureReq
func TestWire_MismatchedStepAndActionTypeNeverReachTheServer(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantWord string
	}{
		{
			name:     "delay step holding an action type",
			args:     stepArgs("--action-type", "send_email"),
			wantWord: `step_type delay takes action_type delay and nothing else, got "send_email"`,
		},
		{
			name:     "delay_until step holding an action type",
			args:     stepArgs("--step-type", "delay_until", "--action-type", "send_notification"),
			wantWord: `step_type delay_until takes action_type delay_until and nothing else, got "send_notification"`,
		},
		{
			name:     "action step holding a delay",
			args:     stepArgs("--step-type", "action", "--action-type", "delay"),
			wantWord: `step_type action cannot carry action_type "delay"`,
		},
		{
			name:     "action step holding a delay_until",
			args:     stepArgs("--step-type", "action", "--action-type", "delay_until"),
			wantWord: `step_type action cannot carry action_type "delay_until"`,
		},
		{
			// The gate reads the assembled body, not the flags, so a pair
			// pushed in through --data is the same wire request and gets the
			// same refusal.
			name:     "mismatched pair supplied through --data",
			args:     []string{"workflows", "steps", "create", "7b9e5a2c-0000-4000-8000-000000000001", "--step-type", "delay", "--order", "0", "--data", `{"action_type":"send_email"}`},
			wantWord: `step_type delay takes action_type delay and nothing else, got "send_email"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, hit, err := captureReq(t, tc.args...)
			if hit {
				t.Error("the request reached the server — the pair was refused by the server's 422, not by the local gate")
			}
			if err == nil {
				t.Fatal("the mismatched pair was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantWord)
			}
			var ee *api.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("want *api.ExitError so the shell sees a validation exit, got %T", err)
			}
			if ee.Code != api.ExitValidation {
				t.Errorf("exit code = %d, want %d (validation)", ee.Code, api.ExitValidation)
			}
		})
	}
}

// TestWire_ComposedStepsAreGatedTooAndNameTheirIndex covers the other shape a
// step arrives in: `workflows create --steps` writes the whole automation in
// one request, and one bad pair refuses all of it — the workflow, the trigger
// and every other step. The index is the part that makes the refusal usable on
// an array of ten.
//
// Value: protects=a mismatched pair inside --steps is refused locally, naming the element; fails_when=the gate is wired only to `steps create` and the composed path keeps paying a whole-request 422; why_new=composed steps are a second call site for a rule added in this sync; seam=captureReq
func TestWire_ComposedStepsAreGatedTooAndNameTheirIndex(t *testing.T) {
	cases := []struct {
		name     string
		steps    string
		wantWord string // "" = must reach the wire
	}{
		{
			name:     "second element holds a delay in an action step",
			steps:    `[{"step_type":"action","action_type":"send_email","order":0},{"step_type":"action","action_type":"delay","order":1}]`,
			wantWord: `steps[1]: step_type action cannot carry action_type "delay"`,
		},
		{
			name:     "first element is a delay step holding an action",
			steps:    `[{"step_type":"delay","action_type":"send_email","order":0}]`,
			wantWord: `steps[0]: step_type delay takes action_type delay and nothing else, got "send_email"`,
		},
		{
			// delay and delay_until share one arm in stepPairProblem (want ==
			// stepType for both), so the composed path was proven for `delay`
			// alone. The composed path is also the only one that wraps the
			// sentence in the `steps[i]: ` prefix, so each kind renders through
			// code the other does not exercise.
			name:     "delay_until is gated inside the array too",
			steps:    `[{"step_type":"delay_until","action_type":"send_email","order":0}]`,
			wantWord: `steps[0]: step_type delay_until takes action_type delay_until and nothing else, got "send_email"`,
		},
		{
			// The loop skips an element that is not an object rather than
			// inventing a second vocabulary for a shape problem the server's
			// own validator already names. The request therefore has to leave:
			// a local refusal here would be the CLI guessing at a shape rule.
			//
			// Value: protects=a --steps element that is not an object is left to the server, not refused locally; fails_when=the non-object arm turns into an error and the CLI invents a shape vocabulary; why_new=the skip arm of the new gate had no case; seam=captureReq; no_claim=says nothing about what the server answers for that body
			name:  "a non-object element is left to the server's validator",
			steps: `[{"step_type":"delay","action_type":"delay","order":0},"nope"]`,
		},
		{
			name:  "every element fits, including a loop's free string",
			steps: `[{"step_type":"delay","action_type":"delay","order":0},{"step_type":"loop","action_type":"iterate_over_guests","order":1},{"step_type":"condition","condition_type":"value_check","order":2}]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, hit, err := captureReq(t, "workflows", "create",
				"--name", "pre-trip reminders", "--steps", tc.steps)
			if tc.wantWord == "" {
				if !hit {
					t.Fatalf("a legal composed create never left: %v", err)
				}
				return
			}
			if hit {
				t.Error("the request reached the server — a whole-request 422 is what the local gate exists to avoid")
			}
			if err == nil {
				t.Fatal("the mismatched pair was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantWord)
			}
			var ee *api.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("want *api.ExitError so the shell sees a validation exit, got %T", err)
			}
			if ee.Code != api.ExitValidation {
				t.Errorf("exit code = %d, want %d (validation)", ee.Code, api.ExitValidation)
			}
		})
	}
}

// TestWire_LegalStepAndActionTypePairsStillReachTheServer is the other half of
// the gate, and the half that catches an over-eager one. The server's rule is
// narrower than "action_type must be a known ActionType": condition and loop
// steps ignore action_type entirely, so any value is legal there, and refusing
// one locally would make a documented operation unreachable from the CLI.
//
// Value: protects=every (step_type, action_type) pair the server accepts still reaches the wire with both fields; fails_when=the gate widens to step types whose resolver ignores action_type, or starts checking action_type against a hard-coded ActionType list; why_new=the gate is new in this sync and its failure mode is refusing valid work; seam=captureReq
func TestWire_LegalStepAndActionTypePairsStillReachTheServer(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want map[string]any
	}{
		{
			name: "delay with delay",
			args: stepArgs(),
			want: map[string]any{"step_type": "delay", "action_type": "delay"},
		},
		{
			name: "delay_until with delay_until",
			args: stepArgs("--step-type", "delay_until", "--action-type", "delay_until"),
			want: map[string]any{"step_type": "delay_until", "action_type": "delay_until"},
		},
		{
			name: "action with an ordinary action type",
			args: stepArgs("--step-type", "action", "--action-type", "send_email"),
			want: map[string]any{"step_type": "action", "action_type": "send_email"},
		},
		{
			// The resolver returns LoopEvaluator whatever action_type says, so
			// the CLI must not judge it. A type the CLI has never heard of is
			// the case that matters: the catalogue is per-tenant.
			name: "loop keeps its free-string action type",
			args: stepArgs("--step-type", "loop", "--action-type", "iterate_over_guests"),
			want: map[string]any{"step_type": "loop", "action_type": "iterate_over_guests"},
		},
		{
			name: "condition carries a condition type instead",
			args: []string{"workflows", "steps", "create", "7b9e5a2c-0000-4000-8000-000000000001", "--step-type", "condition", "--condition-type", "value_check", "--order", "1"},
			want: map[string]any{"step_type": "condition", "condition_type": "value_check"},
		},
		{
			// The gate's comment excuses `condition` and `loop` together, but
			// the loop row above only proves the loop half: it passes a type
			// the gate would never refuse anyway. This row carries a type the
			// gate DOES refuse under delay and action, so it fails the moment
			// a `case "condition":` is added to stepPairProblem — which is the
			// regression that would make a documented step unreachable.
			name: "condition is never judged, even holding a type the gate refuses elsewhere",
			args: []string{"workflows", "steps", "create", "7b9e5a2c-0000-4000-8000-000000000001", "--step-type", "condition", "--condition-type", "value_check", "--action-type", "delay", "--order", "1"},
			want: map[string]any{"step_type": "condition", "condition_type": "value_check", "action_type": "delay"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body, req, hit, err := captureReq(t, tc.args...)
			if !hit {
				t.Fatalf("the request never left: %v", err)
			}
			if req.Method != "POST" || req.Path != "/workflows/7b9e5a2c-0000-4000-8000-000000000001/steps" {
				t.Errorf("request line = %s %s, want POST /workflows/{id}/steps", req.Method, req.Path)
			}
			for key, want := range tc.want {
				if got := body[key]; got != want {
					t.Errorf("body[%q] = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

// TestWire_AbsentActionTypeIsLeftToTheServer pins the gate's third branch. A
// missing action_type is the server's call: it is required for action / delay /
// delay_until and genuinely optional for loop, and the CLI cannot tell which
// resolver path a type will take. Refusing it here would block the loop case.
//
// Value: protects=steps create with no --action-type still issues the request; fails_when=the gate grows a required-ness check and refuses loop steps the server accepts; why_new=the empty-string branch is new code; seam=captureReq
func TestWire_AbsentActionTypeIsLeftToTheServer(t *testing.T) {
	_, body, _, hit, err := captureReq(t, "workflows", "steps", "create",
		"7b9e5a2c-0000-4000-8000-000000000001",
		"--step-type", "loop", "--order", "0")
	if !hit {
		t.Fatalf("the request never left: %v", err)
	}
	if got, ok := body["action_type"]; ok && got != "" {
		t.Errorf("body carried action_type = %#v, want it absent or empty", got)
	}
}

// -----------------------------------------------------------------------------
// The three refusals this sync changed
// -----------------------------------------------------------------------------

// TestWire_InvalidStepConfigSurfacesWhichTypeIsWrong is the regression test for
// the gap 1.60.0 made expensive: INVALID_STEP_CONFIG was unregistered, so it
// reached RawAPIError, which carries `message` alone — the fixed sentence "Step
// config does not match the action schema." Every word identifying the field or
// the type lives in `details.errors`, and was dropped on the floor.
//
// The invocation is one the local gate does NOT catch (a misspelt type, which
// needs the ActionType enum the spec never publishes), so the request really is
// answered by the server.
//
// Value: protects=details.errors from a 422 INVALID_STEP_CONFIG reaches the user, with the pairing rule the server's sentence omits; fails_when=the registry entry is dropped and the code falls back to RawAPIError, which prints the message alone; why_new=the code was unregistered before this sync; seam=testNewRunner
func TestWire_InvalidStepConfigSurfacesWhichTypeIsWrong(t *testing.T) {
	hit, err := refuseAgainst(t, http.StatusUnprocessableEntity,
		`{"meta":{"request_id":"r1"},"error":{"code":"INVALID_STEP_CONFIG",`+
			`"message":"Step config does not match the action schema.",`+
			`"hint":"Check the action_type and the required config fields.",`+
			`"retriable":false,`+
			`"details":{"errors":{"action_type":["Unknown action type \"sendEmail\"."]}}}}`,
		stepArgs("--step-type", "action", "--action-type", "sendEmail")...)
	if !hit {
		t.Fatalf("the request never left, so the response under test was never read: %v", err)
	}
	msg := userMessage(t, err)
	if !strings.Contains(msg, `Unknown action type "sendEmail".`) {
		t.Errorf("UserMessage() = %q, want it to carry details.errors", msg)
	}
	if !strings.Contains(msg, "action_type") {
		t.Errorf("UserMessage() = %q, want it to name the field that was rejected", msg)
	}
	if !strings.Contains(msg, "step_type=delay takes action_type=delay") {
		t.Errorf("UserMessage() = %q, want it to state the pairing rule the server's sentence omits", msg)
	}
}

// TestWire_StepDeleteRefusedForItsChildrenNamesThem covers 1.57.0. The refusal
// is a Laravel ValidationException, so it arrives as VALIDATION_FAILED with the
// child ids inside the message — the one thing the caller needs in order to
// delete depth-first. A breaking change whose remediation is in the message is
// only breaking-with-a-fix if the message survives the client.
//
// Value: protects=the 422 naming a step's children reaches the user intact; fails_when=the delete closure stops parsing the response (the 422 is new on this operation in 1.57.0) or swallows details; why_new=the operation could not return 422 before this sync; seam=testNewRunner
func TestWire_StepDeleteRefusedForItsChildrenNamesThem(t *testing.T) {
	hit, err := refuseAgainst(t, http.StatusUnprocessableEntity,
		`{"meta":{"request_id":"r1"},"error":{"code":"VALIDATION_FAILED",`+
			`"message":"The given data was invalid.","retriable":false,`+
			`"details":{"step_id":["Step 31 still has steps inside it (32, 33). Delete those first."]}}}`,
		"workflows", "steps", "delete", "7b9e5a2c-0000-4000-8000-000000000001", "31")
	if !hit {
		t.Fatalf("the request never left: %v", err)
	}
	msg := userMessage(t, err)
	for _, want := range []string{"step_id", "Step 31 still has steps inside it (32, 33)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("UserMessage() = %q, want it to contain %q", msg, want)
		}
	}
}

// TestWire_TrialRefusalIsNotReadAsAPermissionsProblem covers 1.58.0. An unpaid
// trial is refused on every endpoint with 403 FORBIDDEN, which is also what a
// missing ability, a missing role permission and a cross-tenant read answer —
// so the message has to carry both the server's reason and the fact that the
// other three exist, or an operator spends the afternoon editing token scopes.
//
// Driven through a READ, deliberately: the gate is middleware, so it refuses
// before the handler on every route, and a read is the first call any session
// makes.
//
// Value: protects=a 403 FORBIDDEN keeps the server's reason and names the four states that produce it; fails_when=the registry entry is dropped, leaving RawAPIError's "FORBIDDEN: <msg>" with no remediation; why_new=FORBIDDEN gained the trial cause in this sync and was never typed; seam=testNewRunner
func TestWire_TrialRefusalIsNotReadAsAPermissionsProblem(t *testing.T) {
	hit, err := refuseAgainst(t, http.StatusForbidden,
		`{"meta":{"request_id":"r1"},"error":{"code":"FORBIDDEN",`+
			`"message":"API and MCP access are not available during the free trial. Subscribe to a paid plan to use them.",`+
			`"retriable":false}}`,
		"products", "list")
	if !hit {
		t.Fatalf("the request never left: %v", err)
	}
	var fe *invpkg.ForbiddenError
	if !errors.As(err, &fe) {
		t.Fatalf("want *invpkg.ForbiddenError, got %T: %v", err, err)
	}
	msg := userMessage(t, err)
	if !strings.Contains(msg, "Subscribe to a paid plan") {
		t.Errorf("UserMessage() = %q, want the server's own reason", msg)
	}
	if !strings.Contains(msg, "whoami") {
		t.Errorf("UserMessage() = %q, want the remediation for the ability cause too", msg)
	}
}

// TestWire_EachGateReadsOnlyItsOwnRouteShape pins the scoping both outside
// readers asked for. Neither request schema forbids extra keys, so `--data` can
// put a step-shaped pair on a shell create and a `steps` array on a single-step
// create. The server ignores the ones its own schema does not declare, so the
// CLI must too: an earlier version judged both shapes on both routes and
// refused a `workflows create` the server would have accepted.
//
// Value: protects=each gate reads only the shape its own route sends, so an extra key the server ignores never costs a local refusal; fails_when=either gate widens back to reading both shapes, or one is wired to the other's route; why_new=the single combined gate refused a shell create carrying a stray pair; seam=captureReq; no_claim=says nothing about what the server does with the stray key
func TestWire_EachGateReadsOnlyItsOwnRouteShape(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want map[string]any
	}{
		{
			// A shell create carrying a step-shaped pair the server ignores.
			// The pair is a mismatched one, so the old combined gate refused it.
			name: "workflows create is not judged on a stray top-level pair",
			args: []string{"workflows", "create", "--name", "pre-trip reminders",
				"--data", `{"step_type":"delay","action_type":"send_email"}`},
			want: map[string]any{"name": "pre-trip reminders", "step_type": "delay", "action_type": "send_email"},
		},
		{
			// The mirror case: a single-step create carrying a stray `steps`
			// array, which CreateWorkflowStepRequest does not declare.
			name: "steps create is not judged on a stray steps array",
			args: []string{"workflows", "steps", "create", "7b9e5a2c-0000-4000-8000-000000000001",
				"--step-type", "action", "--action-type", "send_email", "--order", "0",
				"--data", `{"steps":[{"step_type":"delay","action_type":"send_email"}]}`},
			want: map[string]any{"step_type": "action", "action_type": "send_email"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body, _, hit, err := captureReq(t, tc.args...)
			if !hit {
				t.Fatalf("the request was refused locally, but the server accepts it: %v", err)
			}
			for key, want := range tc.want {
				if got := body[key]; got != want {
					t.Errorf("body[%q] = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

// TestAudit_LocalPairRefusalRecordsItsCodeAndBody pins the forensic half of the
// gate, which four independent reviewers raised and the last one asked for by
// name: a refusal decided inside the closure still writes an audit row, and
// that row used to be indistinguishable from a transport failure.
//
// Two things make it legible. `errorCode` has a case for `*api.ExitError`, so
// `error_code` is present at all (its fallthrough is "" and the field is
// omitempty, so a missing case deletes the field rather than blanking it). And
// the closure returns its assembled body as WireBody, so `body_sha256` covers
// what the gate actually judged instead of the raw `--data` fallback
// runMutation uses when a closure returns no result — here that fallback would
// have hashed nothing at all, since this invocation passes no --data.
//
// Value: protects=a locally refused mutation is identifiable in the audit log by its code and the body the gate judged; fails_when=errorCode loses its *api.ExitError case, or a gate stops returning WireBody, either of which silently drops a field from the row; why_new=no test pinned the audit row for any local-gate refusal; seam=testNewRunner; no_claim=says nothing about whether such rows should be written at all, which is a separate policy choice
func TestAudit_LocalPairRefusalRecordsItsCodeAndBody(t *testing.T) {
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request reached the server; the gate should have refused it first")
	})

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := invpkg.NewFileLogger(auditPath)
	if err != nil {
		t.Fatalf("new audit logger: %v", err)
	}
	runner.AuditLogger = logger

	prevShared := sharedRunner
	sharedRunner = &Runner{}
	prev := testNewRunner
	testNewRunner = func() (*Runner, error) { return runner, nil }
	t.Cleanup(func() {
		testNewRunner = prev
		sharedRunner = prevShared
	})

	root := Cmd()
	root.SetArgs(stepArgs("--action-type", "send_email"))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("the mismatched pair was accepted")
	}

	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		t.Fatal("audit log is empty: a refused mutation wrote no row")
	}
	var entry struct {
		ErrorCode  string `json:"error_code"`
		BodySHA256 string `json:"body_sha256"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("parse audit entry: %v", err)
	}

	if entry.ErrorCode != "CLIENT_REFUSED" {
		t.Errorf("error_code = %q, want CLIENT_REFUSED; an empty value here means the field "+
			"was dropped from the row entirely, which is how a local refusal became "+
			"indistinguishable from a transport failure", entry.ErrorCode)
	}

	// The body the gate judged, as triggerOrStepBody assembles it: json.Marshal
	// sorts map keys, so this spelling is the contract.
	wantBody := `{"action_type":"send_email","order":0,"step_type":"delay"}`
	if want := sha256Hex([]byte(wantBody)); entry.BodySHA256 != want {
		t.Errorf("body_sha256 = %q, want %q (the hash of %s)", entry.BodySHA256, want, wantBody)
	}
	if entry.BodySHA256 == sha256Hex(nil) {
		t.Error("body_sha256 is the hash of no bytes: the row fell back to raw --data, " +
			"so it records nothing about the body the gate refused")
	}
}
