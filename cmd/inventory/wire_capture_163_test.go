package inventory

// Wire-level capture for the cli-v1 1.60.0 → 1.63.0 sync.
//
// Like the 1.60.0 file, this sync adds no operation and no flag, so every
// drift direction stays green over all three changes. Two of them are prose
// the CLI can only pass on (1.62.0's notification failure, 1.63.0's withheld
// credentials); the third, 1.61.0's parent rule, is a local gate, and a gate
// is only a gate if the request does not leave.
//
// So each refusal case below asserts hit=false, and — the half a drift test
// structurally cannot see — each case the CLI must NOT judge asserts hit=true.
// That second group is the point of the file. A gate that refuses a parent_ref
// the server would have accepted, or one the CLI cannot resolve, is worse than
// no gate: it makes a documented shape unreachable from the CLI and the
// operator has no way around it. The same reasoning is why
// `workflows steps create --parent-step-id` has no gate at all and is pinned
// here as reaching the wire.

import (
	"errors"
	"strings"
	"testing"

	"github.com/captainbook/captainbook-cli/internal/api"
)

// composedCreate runs `workflows create` with a --steps array, which is the one
// route that carries both a parent_ref and the step it names.
func composedCreate(t *testing.T, steps string) (hit bool, err error) {
	t.Helper()
	_, _, _, hit, err = captureReq(t, "workflows", "create",
		"--name", "pre-trip reminders", "--steps", steps)
	return hit, err
}

// -----------------------------------------------------------------------------
// 1.61.0 — the parent_ref gate
// -----------------------------------------------------------------------------

// TestWire_ParentRefNamingAChildlessStepNeverReachesTheServer covers the three
// step types that never run children, from inside the composed array.
//
// `parent_ref` reads like "the step before me" and does not mean that, which is
// the whole reason 1.61.0 exists: before it, naming a plain action returned 201
// and built a workflow that silently stopped at that step. The refusal has to
// name the array index, because that is the only thing that makes it usable on
// an array of ten.
//
// Value: protects=a parent_ref naming a step type that cannot run children is refused locally, before the request, naming the element; fails_when=the gate is dropped from the create closure, or stops resolving refs so a childless parent reaches the wire and costs a whole-request 422; why_new=the rule is new in this sync and the CLI had no check for it; seam=captureReq
func TestWire_ParentRefNamingAChildlessStepNeverReachesTheServer(t *testing.T) {
	cases := []struct {
		name     string
		steps    string
		wantWord string
	}{
		{
			name:     "parent is a plain action",
			steps:    `[{"step_type":"action","action_type":"send_email","ref":"mail","order":1},{"step_type":"action","action_type":"send_email","parent_ref":"mail","branch_type":"true","order":2}]`,
			wantWord: `steps[1]: parent_ref "mail" names a step whose step_type is action`,
		},
		{
			name:     "parent is a delay",
			steps:    `[{"step_type":"delay","action_type":"delay","ref":"wait","order":1},{"step_type":"action","action_type":"send_email","parent_ref":"wait","branch_type":"true","order":2}]`,
			wantWord: `steps[1]: parent_ref "wait" names a step whose step_type is delay`,
		},
		{
			name:     "parent is a delay_until",
			steps:    `[{"step_type":"delay_until","action_type":"delay_until","ref":"until","order":1},{"step_type":"action","action_type":"send_email","parent_ref":"until","branch_type":"true","order":2}]`,
			wantWord: `steps[1]: parent_ref "until" names a step whose step_type is delay_until`,
		},
		{
			// The index has to be the REFERRER's position, not the parent's.
			// Reporting the parent would send the operator to a step that is
			// fine and leave the broken one unnamed.
			name:     "the index names the referring step, not the parent",
			steps:    `[{"step_type":"condition","condition_type":"value_check","ref":"check","order":1},{"step_type":"action","action_type":"send_email","ref":"mail","order":2},{"step_type":"action","action_type":"send_sms","parent_ref":"mail","branch_type":"true","order":3}]`,
			wantWord: `steps[2]: parent_ref "mail" names a step whose step_type is action`,
		},
		{
			// A forward ref is ALSO a 422 on its own (the ref is not listed
			// earlier), so refusing here cannot refuse something the server
			// would have taken. The gate judges the type it can see; it does
			// not claim to police ordering.
			name:     "a forward ref to a childless step is still refused on type",
			steps:    `[{"step_type":"action","action_type":"send_email","parent_ref":"mail","branch_type":"true","order":1},{"step_type":"action","action_type":"send_email","ref":"mail","order":2}]`,
			wantWord: `steps[0]: parent_ref "mail" names a step whose step_type is action`,
		},
		{
			// The gate reads the assembled body, not the flags, so a steps
			// array pushed in through --data is the same wire request and gets
			// the same refusal. Mirrors the 1.60.0 gate's --data case.
			name:     "a childless parent supplied through --data",
			steps:    "",
			wantWord: `steps[1]: parent_ref "mail" names a step whose step_type is action`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hit bool
			var err error
			if tc.steps == "" {
				_, _, _, hit, err = captureReq(t, "workflows", "create",
					"--name", "pre-trip reminders", "--data",
					`{"steps":[{"step_type":"action","action_type":"send_email","ref":"mail","order":1},{"step_type":"action","action_type":"send_sms","parent_ref":"mail","branch_type":"true","order":2}]}`)
			} else {
				hit, err = composedCreate(t, tc.steps)
			}

			if hit {
				t.Error("the request reached the server — a whole-request 422 is what the local gate exists to avoid")
			}
			if err == nil {
				t.Fatal("the childless parent_ref was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantWord)
			}
			// The sentence has to carry the correction, not just the refusal:
			// an operator who reached for parent_ref as "after this step" needs
			// to be told what to use instead.
			if !strings.Contains(err.Error(), "omit parent_ref") {
				t.Errorf("error = %q, want it to say to omit parent_ref and order the step after", err.Error())
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

// TestWire_ParentRefTheGateCannotJudgeStillReachesTheServer is the mirror
// image, and the half that keeps the gate honest.
//
// Every case here is a body the CLI must pass on: either the parent genuinely
// runs children, or the gate cannot resolve what the parent IS, in which case
// the server's own validator names it. Asserting on a refusal message would be
// useless for these — the assertion is that the request LEFT.
//
// Value: protects=the parent_ref gate refuses only what it can prove, so a legal branch and an unresolvable ref both still reach the server; fails_when=the gate negates a condition/loop test instead of listing the childless types, or starts refusing unknown refs and duplicate refs it cannot judge, making documented shapes unreachable from the CLI; why_new=the new gate's pass-through arms had no coverage; seam=captureReq; no_claim=says nothing about what the server answers for these bodies
func TestWire_ParentRefTheGateCannotJudgeStillReachesTheServer(t *testing.T) {
	cases := []struct {
		name  string
		steps string
	}{
		{
			name:  "parent is a condition, which runs children",
			steps: `[{"step_type":"condition","condition_type":"value_check","ref":"check","order":1},{"step_type":"action","action_type":"send_email","parent_ref":"check","branch_type":"true","order":2}]`,
		},
		{
			name:  "parent is a loop, which runs children",
			steps: `[{"step_type":"loop","action_type":"iterate_over_guests","ref":"each","order":1},{"step_type":"action","action_type":"send_email","parent_ref":"each","branch_type":"true","order":2}]`,
		},
		{
			name:  "no parent_ref anywhere",
			steps: `[{"step_type":"action","action_type":"send_email","order":1},{"step_type":"action","action_type":"send_sms","order":2}]`,
		},
		{
			// An unknown ref is its own 422, and was one before 1.61.0. The
			// CLI has no idea what type it names, so inventing a refusal here
			// would be a second vocabulary for a shape error the server
			// already reports precisely.
			name:  "parent_ref names a ref that is nowhere in the array",
			steps: `[{"step_type":"action","action_type":"send_email","ref":"mail","order":1},{"step_type":"action","action_type":"send_sms","parent_ref":"ghost","branch_type":"true","order":2}]`,
		},
		{
			// step_type absent on the parent: required, so the server refuses
			// the element on its own terms. Not the parent gate's call.
			name:  "the named parent declares no step_type",
			steps: `[{"ref":"mystery","order":1},{"step_type":"action","action_type":"send_email","parent_ref":"mystery","branch_type":"true","order":2}]`,
		},
		{
			// A step_type this build has not heard of must fall through, or the
			// CLI would refuse a type the day upstream ships it. This is the
			// explicit-arms property in parentRefProblem, and a negated
			// condition/loop test would fail exactly here.
			name:  "the named parent declares a step_type this build does not know",
			steps: `[{"step_type":"fan_out","ref":"future","order":1},{"step_type":"action","action_type":"send_email","parent_ref":"future","branch_type":"true","order":2}]`,
		},
		{
			// Duplicate refs are themselves a 422, so the first definition
			// stands and a second one does not get to decide a refusal. Pins
			// the dup arm: were the LAST definition to win, this body would be
			// refused on a ref whose first meaning is a condition.
			name:  "a duplicate ref does not let the second definition decide",
			steps: `[{"step_type":"condition","condition_type":"value_check","ref":"dup","order":1},{"step_type":"action","action_type":"send_email","ref":"dup","order":2},{"step_type":"action","action_type":"send_sms","parent_ref":"dup","branch_type":"true","order":3}]`,
		},
		{
			// An element that is not an object is skipped rather than given a
			// vocabulary of its own, same as the pairing gate's arm.
			name:  "a non-object element does not stop the array being passed on",
			steps: `[{"step_type":"condition","condition_type":"value_check","ref":"check","order":1},"nope",{"step_type":"action","action_type":"send_email","parent_ref":"check","branch_type":"true","order":2}]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit, err := composedCreate(t, tc.steps)
			if !hit {
				t.Fatalf("the request never left, so the gate refused a body it cannot prove wrong: %v", err)
			}
		})
	}
}

// TestWire_ParentStepIDIsLeftToTheServer pins the deliberate gap.
//
// `workflows steps create --parent-step-id` carries an id, not a type: the
// parent is a row that already exists server-side and nothing in the request
// says what step_type it has. The CLI could only judge it by fetching the
// workflow first, which would turn one write into two requests and still race
// anyone editing the tree. So this half stays the server's 422 — and that is a
// decision worth pinning, because the obvious "finish the job" follow-up is to
// add a gate here, where it can only be built on a guess.
//
// Value: protects=the parent_step_id half reaches the server, so the CLI never refuses a parent whose type it cannot know; fails_when=a local gate is added on an id the request does not describe, or the composed gate is wired onto this route and reads a top-level parent_ref that is not in its schema; why_new=records the documented asymmetry between the two routes; seam=captureReq; no_claim=says nothing about what the server answers
func TestWire_ParentStepIDIsLeftToTheServer(t *testing.T) {
	_, _, req, hit, err := captureReq(t, "workflows", "steps", "create",
		"7b9e5a2c-0000-4000-8000-000000000001",
		"--step-type", "action", "--action-type", "send_email",
		"--order", "1", "--parent-step-id", "42", "--branch-type", "true")
	if !hit {
		t.Fatalf("the request never left: the CLI refused a parent whose step_type it cannot know: %v", err)
	}
	if req.Method != "POST" {
		t.Errorf("method = %q, want POST", req.Method)
	}
}
