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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/captainbook/captainbook-cli/internal/api"
	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
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
// Value: protects=the parent_ref gate refuses only what it can prove, so a legal branch, an unresolvable ref and a mistyped one all still reach the server; fails_when=the gate negates a condition/loop test instead of listing the childless types, or starts refusing what it cannot judge (an unknown or duplicate ref, a non-string ref, a non-array steps), making documented shapes unreachable from the CLI; why_new=the new gate's pass-through arms had no coverage; seam=captureReq; no_claim=says nothing about what the server answers for these bodies
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
		{
			// The mirror on the map-building pass: a non-string `ref` leaves
			// the parent unresolvable, so the referrer falls through the
			// unknown-ref arm instead of being judged on a type the gate
			// never actually read.
			name:  "the named parent's ref is not a string",
			steps: `[{"step_type":"action","action_type":"send_email","ref":7,"order":1},{"step_type":"action","action_type":"send_sms","parent_ref":"7","branch_type":"true","order":2}]`,
		},
		{
			// `steps` present but not an array at all. Both composed gates
			// read it through the same `.([]any)` assertion, so a non-array
			// has to leave BOTH of them with nothing to judge rather than
			// producing a refusal over a shape the server's validator names.
			name:  "steps is an object rather than an array",
			steps: `{"step_type":"action","action_type":"send_email","parent_ref":"mail"}`,
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

// TestWire_OneBodyBreakingBothComposedRulesIsRefusedOnTheFirst pins the order
// of the two composed gates, which is operator-facing and not visible to
// either gate's own test.
//
// `workflows create` now runs two local gates over the same `steps` array:
// 1.60.0's pairing gate, then 1.61.0's parent gate. A body that breaks both
// gets exactly ONE sentence, and which one is a choice — the pairing gate
// returns first, so the operator is told about the pair. That is the right
// half to lead with: a mismatched pair makes the element's step_type doubtful,
// and a parent complaint computed from a doubtful step_type would send the
// operator to the wrong step. Either way the request must not leave, and the
// two messages must not be concatenated into one unreadable refusal.
//
// Value: protects=a body breaking both composed rules is refused once, before the request, on the pairing gate; fails_when=the parent gate is moved ahead of the pairing gate so a mismatched pair is reported as a parent problem, or a gate stops returning and both sentences are emitted; why_new=the second gate added in this sync created an ordering neither gate's own test can see; seam=captureReq
func TestWire_OneBodyBreakingBothComposedRulesIsRefusedOnTheFirst(t *testing.T) {
	// steps[0] is a delay carrying send_email (pairing gate) AND is named as a
	// parent by steps[1] (parent gate). Both refusals point at a real problem.
	hit, err := composedCreate(t, `[{"step_type":"delay","action_type":"send_email","ref":"wait","order":1},{"step_type":"action","action_type":"send_email","parent_ref":"wait","branch_type":"true","order":2}]`)
	if hit {
		t.Error("the request reached the server — two local refusals applied and neither stopped it")
	}
	if err == nil {
		t.Fatal("a body breaking both composed rules was accepted")
	}
	msg := err.Error()
	if !strings.Contains(msg, `steps[0]: step_type delay takes action_type delay and nothing else, got "send_email"`) {
		t.Errorf("error = %q, want the pairing gate's sentence, which returns first", msg)
	}
	if strings.Contains(msg, "parent_ref") {
		t.Errorf("error = %q, want ONE refusal, not both gates' sentences concatenated", msg)
	}
	var ee *api.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want *api.ExitError, got %T", err)
	}
	if ee.Code != api.ExitValidation {
		t.Errorf("exit code = %d, want %d (validation)", ee.Code, api.ExitValidation)
	}
}

// -----------------------------------------------------------------------------
// 1.63.0 — credentials must not reach the local audit log
// -----------------------------------------------------------------------------

// TestAudit_ComposedStepCredentialIsRedactedButStillReachesTheWire pins both
// halves of the forensic redaction, and the second half is the one worth
// worrying about.
//
// `workflows create` declares `steps` and `trigger` as ForensicFields
// deliberately: they ARE the automation, so a row without them cannot
// reconstruct the workflow. capForensicValue only size-caps, so a composed
// step's config.api_key used to land in ~/.ceebee/audit.jsonl in cleartext.
// Spec 1.63.0 is what made that matter: the server now returns "" for those
// fields forever, so the audit file could hold the only plaintext copy of a
// working credential.
//
// The redaction must change the ROW and nothing else. A redactor that also
// reached the request body would silently send a blank over a live key — which
// is the exact accident 1.63.0's write rule exists to prevent — and no
// assertion on the audit row alone could tell the two apart. So this test reads
// the body the SERVER received and the row the CLI wrote, from one invocation.
//
// Value: protects=a step config credential is blanked in the audit row while the real value still reaches the server; fails_when=redactForensicCredentials stops being called, blanks the wrong depth, or mutates args.Flags so the wire body loses the credential; why_new=the leak was confirmed with a canary and nothing pinned either half; seam=testNewRunner+fakeServer; no_claim=says nothing about credentials on any other command, and workflows create is the only one whose ForensicFields carry a config
func TestAudit_ComposedStepCredentialIsRedactedButStillReachesTheWire(t *testing.T) {
	const canary = "sk-live-CANARY-must-not-be-logged"

	var gotBody []byte
	_, runner := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			gotBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{},"meta":{"request_id":"r1"}}`))
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
	root.SetArgs([]string{"workflows", "create", "--name", "pre-trip reminders",
		"--steps", `[{"step_type":"action","action_type":"send_webhook","order":1,"config":{"url":"https://example.test/hook","api_key":"` + canary + `"}}]`})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("a legal composed create failed: %v", err)
	}

	// Half 1: the wire still carries the real credential.
	if len(gotBody) == 0 {
		t.Fatal("the server received no body")
	}
	if !strings.Contains(string(gotBody), canary) {
		t.Errorf("the request body lost the credential, so the redactor reached the wire: %s", gotBody)
	}

	// Half 2: the audit row does not.
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		t.Fatal("audit log is empty: the mutation wrote no row")
	}
	if strings.Contains(line, canary) {
		t.Errorf("the credential is in the audit row in cleartext:\n%s", line)
	}
	// The KEY has to survive with an empty value: the row's job is to make the
	// workflow reconstructable, and "this step had an api_key" is part of that.
	if !strings.Contains(line, `"api_key":""`) {
		t.Errorf("want the api_key key kept with an empty value, got:\n%s", line)
	}
	// A non-credential sibling in the same config must be untouched, or the
	// redactor is blanking the whole object rather than the credential.
	if !strings.Contains(line, "https://example.test/hook") {
		t.Errorf("the non-credential sibling was dropped too:\n%s", line)
	}
}

// TestAudit_AForensicValueWithNoCredentialKeepsItsOwnBytes pins the
// not-changed path.
//
// redactForensicCredentials returns its input unchanged when nothing matched,
// which is what keeps a json-typed flag's own byte order in the row (it rides
// as a json.RawMessage). A redactor that re-encoded unconditionally would pass
// the test above and still quietly reorder every audit row's steps array, so
// that property needs its own assertion.
//
// Value: protects=a forensic value with no credential field is written byte-for-byte, preserving the operator's key order; fails_when=redactForensicCredentials re-encodes unconditionally instead of returning the original on no match; why_new=the unchanged path is invisible to the redaction test; seam=forensicSummary
func TestAudit_AForensicValueWithNoCredentialKeepsItsOwnBytes(t *testing.T) {
	// Key order here is deliberately NOT alphabetical: json.Marshal of a map
	// would sort these, so surviving verbatim is the observable.
	const steps = `[{"step_type":"action","order":1,"action_type":"send_email"}]`

	def := CommandDef{ForensicFields: []string{"steps"}}
	args := RunArgs{Flags: map[string]any{"steps": json.RawMessage(steps)}}

	out := forensicSummary(def, args)
	got, ok := out["steps"].(json.RawMessage)
	if !ok {
		t.Fatalf("steps is %T, want json.RawMessage left as-is", out["steps"])
	}
	if string(got) != steps {
		t.Errorf("forensic value was re-encoded:\n got %s\nwant %s", got, steps)
	}
}

// TestRedactForensicCredentials_CoversEveryArm is a table over the redactor's
// own branches, which the two audit tests above reach only one path of.
//
// CLAUDE.md asks for both branches of a new conditional. The audit test's
// credential sits at steps[0].config.api_key and is a non-empty string, so on
// its own it leaves three deliberate decisions unpinned: the case-insensitive
// match (drop strings.ToLower and `API_KEY` reaches the log), the any-depth
// walk (narrow it to a key named `config` and the test still passes), and the
// already-blank skip (delete it and an operator who legitimately sent "" gets
// their row re-encoded and reordered for nothing).
//
// Value: protects=the redactor blanks a credential whatever its case, depth, container or value type, and returns its input untouched when nothing matched; fails_when=strings.ToLower is dropped, the walk is narrowed to a `config` key, the already-blank skip is removed, or a non-string credential value stops being blanked; why_new=the audit test exercises one shape only, so three new conditionals had no coverage; seam=none; no_claim=says nothing about which commands pass values through this function, which is forensicSummary's concern
func TestRedactForensicCredentials_CoversEveryArm(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantGone  string // substring that must NOT survive ("" = skip)
		wantKept  string // substring that must survive ("" = skip)
		unchanged bool   // the input must come back byte-identical
	}{
		{
			name:     "lowercase api_key under config",
			in:       `[{"config":{"api_key":"LEAK","url":"https://example.test"}}]`,
			wantGone: "LEAK",
			wantKept: `"api_key":""`,
		},
		{
			// The comment calls the case-insensitive match deliberate, so it
			// needs an assertion: dropping strings.ToLower writes this to disk.
			name:     "uppercase API_KEY is matched too",
			in:       `[{"config":{"API_KEY":"LEAK"}}]`,
			wantGone: "LEAK",
			wantKept: `"API_KEY":""`,
		},
		{
			name:     "mixed-case Token is matched too",
			in:       `[{"config":{"Token":"LEAK"}}]`,
			wantGone: "LEAK",
			wantKept: `"Token":""`,
		},
		{
			// The walk is any-depth on purpose. Narrowing it to a key literally
			// named `config` would pass every other row in this table.
			name:     "a credential deeper than config",
			in:       `[{"config":{"auth":{"headers":{"secret":"LEAK"}}}}]`,
			wantGone: "LEAK",
			wantKept: `"secret":""`,
		},
		{
			// `trigger` is the other ForensicField that carries a config, and it
			// is one object rather than an array.
			name:     "a trigger object, not an array",
			in:       `{"action_type":"scheduled_time","config":{"password":"LEAK"}}`,
			wantGone: "LEAK",
			wantKept: `"password":""`,
		},
		{
			// Not a string: the type assertion must not let it through just
			// because the already-blank check does not apply.
			name:     "a non-string credential value is blanked",
			in:       `[{"config":{"api_key":12345}}]`,
			wantGone: "12345",
			wantKept: `"api_key":""`,
		},
		{
			name:     "an object-valued credential is blanked",
			in:       `[{"config":{"token":{"nested":"LEAK"}}}]`,
			wantGone: "LEAK",
			wantKept: `"token":""`,
		},
		{
			// Already blank: 1.63.0 reads "" on write as "leave the stored value
			// alone", so an operator sends it legitimately. Nothing changed, so
			// the value must come back as the operator's own bytes.
			// Key order here is deliberately NOT alphabetical. With the keys
			// sorted, a re-encode produces identical bytes and this case cannot
			// see the skip being removed at all (measured: it passed).
			name:      "an already-blank credential leaves the value untouched",
			in:        `[{"config":{"url":"https://example.test","api_key":""}}]`,
			unchanged: true,
		},
		{
			name:      "no credential anywhere leaves the value untouched",
			in:        `[{"step_type":"action","order":1,"config":{"url":"https://example.test"}}]`,
			unchanged: true, // step_type before order before config: not sorted
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactForensicCredentials(json.RawMessage(tc.in))
			raw, ok := got.(json.RawMessage)
			if !ok {
				t.Fatalf("got %T, want json.RawMessage", got)
			}
			if tc.unchanged {
				if string(raw) != tc.in {
					t.Errorf("value was re-encoded when nothing matched:\n got %s\nwant %s", raw, tc.in)
				}
				return
			}
			if tc.wantGone != "" && strings.Contains(string(raw), tc.wantGone) {
				t.Errorf("credential survived redaction: %s", raw)
			}
			if tc.wantKept != "" && !strings.Contains(string(raw), tc.wantKept) {
				t.Errorf("want %s in the redacted value, got %s", tc.wantKept, raw)
			}
		})
	}
}

// TestForensic_RedactionRunsBeforeTheSizeCap pins the ORDER of the two calls in
// forensicSummary, which the commit calls deliberate and which nothing else
// catches.
//
// Over the cap, capForensicValue throws the value away and keeps a
// canonical_json_sha256 of it. If the cap ran first, that digest would be taken
// over the PLAINTEXT credential — recoverable by anyone who can guess it — and
// redactForensicCredentials would then early-return, because the marker it
// receives is a map rather than a json.RawMessage. Both existing audit tests
// use under-cap values, where capForensicValue returns its input unchanged, so
// swapping the two calls leaves them green.
//
// Value: protects=an oversized forensic value is redacted before it is hashed, so the audit digest is never taken over a plaintext credential; fails_when=the two calls in forensicSummary are swapped, or the cap stops hashing canonical JSON; why_new=both audit tests use under-cap values, where the order is unobservable; seam=none
func TestForensic_RedactionRunsBeforeTheSizeCap(t *testing.T) {
	// One oversized steps array carrying a credential. The filler is what pushes
	// it past the cap; the credential is what must not be in the digest.
	filler := strings.Repeat("x", forensicValueMaxBytes)
	big := `[{"config":{"api_key":"LEAK-IN-THE-DIGEST","note":"` + filler + `"}}]`
	if len(big) <= forensicValueMaxBytes {
		t.Fatalf("test value is %d bytes, not over the %d cap", len(big), forensicValueMaxBytes)
	}

	def := CommandDef{ForensicFields: []string{"steps"}}
	args := RunArgs{Flags: map[string]any{"steps": json.RawMessage(big)}}

	out := forensicSummary(def, args)
	marker, ok := out["steps"].(map[string]any)
	if !ok {
		t.Fatalf("steps is %T, want the oversized-value marker map", out["steps"])
	}
	inner, ok := marker[forensicCapKey].(map[string]any)
	if !ok {
		t.Fatalf("marker has no %q payload: %v", forensicCapKey, marker)
	}
	gotDigest, _ := inner["canonical_json_sha256"].(string)
	if gotDigest == "" {
		t.Fatalf("marker carries no canonical_json_sha256: %v", inner)
	}

	// The digest the CLI should have recorded: of the REDACTED canonical JSON.
	redactedRaw, err := json.Marshal(redactForensicCredentials(json.RawMessage(big)))
	if err != nil {
		t.Fatalf("marshal redacted value: %v", err)
	}
	if want := sha256Hex(redactedRaw); gotDigest != want {
		plaintextRaw, merr := json.Marshal(json.RawMessage(big))
		if merr == nil && gotDigest == sha256Hex(plaintextRaw) {
			t.Fatalf("the digest is over the PLAINTEXT credential: the size cap ran before redaction")
		}
		t.Errorf("canonical_json_sha256 = %q, want %q (the digest of the redacted value)", gotDigest, want)
	}
}
