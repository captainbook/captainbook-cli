package inventory

// TestSkillsDocDrift catches the bug class that TestSpecDrift cannot see:
// drift between the `ceebee …` invocations in skills/*.md and the command
// tree the CLI actually builds. TestSpecDrift checks code against the spec;
// nothing checked the docs against the code, so skills/*.md accumulated
// commands that fail at parse time:
//
//   - `availabilities bulk-update pricing --fare k=v,k=v` (the flag is
//     `--fares`, taking a single JSON array) — the documented form of the
//     ONLY write path for availability_pricing_tier was unrunnable.
//   - `pricing-tiers delete pt_42 --dry-run` in index.md, on the one
//     mutation whose DryRunMode is NotSupported — and contradicting the
//     capability table in the same file.
//   - `pricing-tiers list --product-option-id` in product-options.md, a
//     filter the spec explicitly refuses (tiers scope by product).
//
// The test walks the real cobra tree from Cmd() (no config, no network —
// runner construction is lazy in PersistentPreRunE) and every fenced code
// block in skills/*.md, then asserts three things per invocation:
//
//  1. The command path resolves. An unknown verb under a known resource
//     parent fails loudly rather than being skipped.
//  2. Every --flag used is declared on the resolved command (or is a
//     global registered on the root command in cmd/root.go).
//  3. --dry-run is not documented on a command whose DryRunMode is
//     NotSupported (read from the "dryRun" annotation set in bindCommands).
//
// Deliberately NOT checked: the reverse direction (flags that exist but no
// doc mentions). Docs are curated, not exhaustive; asserting total coverage
// would fail on every intentionally-omitted flag.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// globalFlags are registered on the root command (cmd/root.go) or by
// bindCommands for every mutation, so they are legal on any invocation but
// are not found by walking a leaf command's own flag set.
var globalFlags = map[string]bool{
	"--help":    true,
	"--profile": true,
	"--verbose": true,
}

// -----------------------------------------------------------------------------
// Command tree — flatten Cmd() into "resource verb" → *cobra.Command.
// -----------------------------------------------------------------------------

// resolveCommand walks tokens down the tree from root, consuming each token
// that names a child command. It stops at the first token that doesn't
// match a child (a positional arg like an id, or a flag). It returns the
// deepest command reached plus the number of tokens consumed.
func resolveCommand(root *cobra.Command, tokens []string) (*cobra.Command, int) {
	cur := root
	used := 0
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "-") {
			break
		}
		var next *cobra.Command
		for _, child := range cur.Commands() {
			if child.Name() == tok {
				next = child
				break
			}
		}
		if next == nil {
			break
		}
		cur = next
		used++
	}
	return cur, used
}

// flagsOf collects every flag name declared on c, including inherited
// persistent flags from its parents.
func flagsOf(c *cobra.Command) map[string]bool {
	out := map[string]bool{}
	c.Flags().VisitAll(func(f *pflag.Flag) { out["--"+f.Name] = true })
	c.InheritedFlags().VisitAll(func(f *pflag.Flag) { out["--"+f.Name] = true })
	return out
}

// -----------------------------------------------------------------------------
// Doc scanner — extract `ceebee …` invocations from fenced code blocks.
// -----------------------------------------------------------------------------

// invocation is one `ceebee …` command line found in a doc code block.
type invocation struct {
	File  string
	Line  int
	Text  string   // the full invocation, continuations joined
	Args  []string // tokens after "ceebee"
	Flags []string // --flags appearing in the invocation
}

var ceebeeRe = regexp.MustCompile(`\bceebee\b`)
var flagRe = regexp.MustCompile(`(--[a-z][a-z0-9-]*)`)

// scanDoc pulls invocations out of ```-fenced blocks only. Prose is
// skipped: docs legitimately name flags in sentences ("`--fares` is a
// single JSON array"), and those are not invocations to validate.
func scanDoc(t *testing.T, path string) []invocation {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(raw), "\n")

	var out []invocation
	inFence := false
	fenceLang := ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				inFence = false
				fenceLang = ""
			} else {
				inFence = true
				fenceLang = strings.TrimPrefix(strings.TrimSpace(line), "```")
			}
			continue
		}
		if !inFence {
			continue
		}
		// Only shell-ish blocks carry runnable commands. `text` blocks hold
		// sample stderr/stdout.
		if fenceLang != "" && fenceLang != "bash" && fenceLang != "sh" && fenceLang != "shell" {
			continue
		}
		if !ceebeeRe.MatchString(line) {
			continue
		}

		startLine := i + 1
		// Join backslash continuations.
		text := line
		for strings.HasSuffix(strings.TrimRight(text, " \t"), `\`) && i+1 < len(lines) {
			text = strings.TrimSuffix(strings.TrimRight(text, " \t"), `\`)
			i++
			text += " " + lines[i]
		}

		inv, ok := parseInvocation(text)
		if !ok {
			continue
		}
		inv.File = path
		inv.Line = startLine
		out = append(out, inv)
	}
	return out
}

// parseInvocation isolates the ceebee call inside a line and cuts it at the
// first shell operator. Cutting at `|` matters: several examples pipe into
// jq, and jq programs contain their own text that must not be mistaken for
// CLI flags.
//
// The cut is QUOTE-AWARE, and that is not a nicety. An unquoted scan cuts at
// the first `)` in `--label "Shoe size (EU)"` and silently drops the rest of
// the invocation — which is how `questions create --position 2` sat in the
// docs, validated, for as long as it did: the test was checking a prefix and
// reporting success. A truncated invocation is worse than an unparsed one,
// because it passes.
func parseInvocation(text string) (invocation, bool) {
	loc := ceebeeRe.FindStringIndex(text)
	if loc == nil {
		return invocation{}, false
	}
	seg := cutAtShellOperator(text[loc[1]:])
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return invocation{}, false
	}

	return invocation{
		Text:  "ceebee " + seg,
		Args:  strings.Fields(seg),
		Flags: dedupe(flagRe.FindAllString(seg, -1)),
	}, true
}

// shellOperators end an invocation when they appear outside quotes. `)` is
// here for the `$(ceebee …)` capture idiom; the others end or redirect the
// command.
var shellOperators = []string{"&&", "||", "|", ";", "#", ">", ")"}

// cutAtShellOperator truncates s at the first shell operator that is not
// inside single or double quotes. Backslash escapes the next character
// inside double quotes only, matching POSIX sh: inside single quotes a
// backslash is literal, so `'\'` does not escape the closing quote.
func cutAtShellOperator(s string) string {
	var inSingle, inDouble bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
			continue
		case inDouble:
			if c == '\\' {
				i++ // skip the escaped character
				continue
			}
			if c == '"' {
				inDouble = false
			}
			continue
		case c == '\'':
			inSingle = true
			continue
		case c == '"':
			inDouble = true
			continue
		}
		for _, op := range shellOperators {
			if strings.HasPrefix(s[i:], op) {
				return s[:i]
			}
		}
	}
	return s
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// -----------------------------------------------------------------------------
// The test.
// -----------------------------------------------------------------------------

func TestSkillsDocDrift(t *testing.T) {
	skillsDir := filepath.Join("..", "..", "skills")
	docs, err := filepath.Glob(filepath.Join(skillsDir, "*.md"))
	if err != nil || len(docs) == 0 {
		t.Fatalf("no skills/*.md found (err=%v)", err)
	}
	// README carries a quickstart tour of the same commands and drifts the
	// same way — it documented `products list --status` before the flag
	// existed.
	docs = append(docs, filepath.Join("..", "..", "README.md"))

	// Cmd() builds the whole tree; runner construction is deferred to
	// PersistentPreRunE, so this needs neither config nor network.
	root := Cmd()

	checked := 0
	for _, doc := range docs {
		for _, inv := range scanDoc(t, doc) {
			// Docs cover `ceebee inventory …`; anything else (stats, config,
			// completion) lives outside this package's tree.
			if len(inv.Args) == 0 || inv.Args[0] != root.Name() {
				continue
			}
			// Args[0] is "inventory" — the root itself, not a child of it.
			rest := inv.Args[1:]
			cmd, used := resolveCommand(root, rest)

			// A parent command with children that we stopped short of means
			// the doc named a verb that doesn't exist.
			if cmd.HasSubCommands() && used < len(rest) {
				next := rest[used]
				if !strings.HasPrefix(next, "-") {
					t.Errorf("%s:%d: `%s` — %q is not a subcommand of `%s`",
						inv.File, inv.Line, inv.Text, next, cmd.CommandPath())
					continue
				}
			}
			checked++

			declared := flagsOf(cmd)
			for _, fl := range inv.Flags {
				if globalFlags[fl] || declared[fl] {
					continue
				}
				t.Errorf("%s:%d: `%s` — command `%s` has no %s flag",
					inv.File, inv.Line, inv.Text, cmd.CommandPath(), fl)
			}

			// Documenting --dry-run on an endpoint that rejects it produces
			// an example that always errors.
			if contains(inv.Flags, "--dry-run") && cmd.Annotations["dryRun"] == "none" {
				t.Errorf("%s:%d: `%s` — command `%s` does not support --dry-run "+
					"(DryRunNotSupported); the documented example would fail",
					inv.File, inv.Line, inv.Text, cmd.CommandPath())
			}
		}
	}

	if checked == 0 {
		t.Fatal("scanned no ceebee invocations — the doc scanner is broken, not the docs")
	}
	t.Logf("validated %d `ceebee inventory` invocations across %d docs", checked, len(docs))
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Spec query params → flags, and flag-description hygiene.
// -----------------------------------------------------------------------------

// intentionallyUnexposedQueryParams lists "VERB /path" → query params the
// CLI deliberately does NOT surface as flags. Each entry needs a reason:
// the point is to force a decision at spec-sync time rather than let a new
// filter go unnoticed, which is how `products list --status` stayed missing
// while README documented it.
var intentionallyUnexposedQueryParams = map[string][]string{
	// The Transaction schema's status enum is [succeeded] with the note
	// "Always `succeeded` — failed payments don't produce a row at all", so
	// filtering on pending/failed/partial is guaranteed zero results and
	// `succeeded` filters nothing. See the NOTE in transactions.go.
	"GET /transactions": {"status"},
}

func TestSpecQueryParamsAreExposedAsFlags(t *testing.T) {
	spec := loadSpecDoc(t)

	for _, cl := range walkInventoryCmdLits(t) {
		if cl.Verb != "GET" || cl.Path == "" {
			continue
		}
		op := spec.findOp(cl.Verb, cl.Path)
		if op == nil {
			continue
		}
		declared := map[string]bool{}
		for _, f := range cl.Flags {
			declared[kebabToSnake(f.Name)] = true
		}
		allowed := map[string]bool{}
		for _, name := range intentionallyUnexposedQueryParams[cl.Verb+" "+cl.Path] {
			allowed[name] = true
		}
		var missing []string
		for name := range op.QueryParams {
			if !declared[name] && !allowed[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s (%s %s): spec query params with no flag: %s — expose them, "+
				"or add them to intentionallyUnexposedQueryParams with a reason",
				cl.Use, cl.Verb, cl.Path, strings.Join(missing, ", "))
		}
	}
}

// TestFlagDescriptionsHaveNoBackquotes guards a subtle help-text bug:
// cobra's UnquoteUsage() treats the first backquoted word in a flag
// description as the flag's VALUE PLACEHOLDER, replacing the type name. So
// "Price (minor units, persisted as `fare`)" rendered as `--amount fare`,
// and "extra days added to the `to` timestamp" rendered as
// `--add-days-count to` — both hiding that the flags take an int.
func TestFlagDescriptionsHaveNoBackquotes(t *testing.T) {
	for _, cl := range walkInventoryCmdLits(t) {
		for _, f := range cl.Flags {
			if strings.Contains(f.Description, "`") {
				t.Errorf("%s: --%s description contains a backquote: %q\n"+
					"    cobra renders the backquoted word as the flag's value placeholder "+
					"(e.g. `--amount fare` instead of `--amount int`). Use plain text.",
					cl.Use, f.Name, f.Description)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// Endpoint tables — the "| command | METHOD /path | ability | dry-run |" grids
// that open almost every skill doc.
// -----------------------------------------------------------------------------

// tableRowRe matches one endpoint-table row. The ability cell may carry a
// parenthetical qualifier ("`cli:cs` (or `cli:write` for …)"), so only the
// first backquoted token is treated as the ability.
var tableRowRe = regexp.MustCompile(
	"^\\|\\s*`(inventory [^`]+)`\\s*\\|\\s*([A-Z]+)\\s+(/\\S*)\\s*\\|\\s*`([^`]+)`[^|]*\\|\\s*([^|]+?)\\s*\\|\\s*$")

// dryRunCellRe normalizes the dry-run column. Docs write "n/a", "n/a (read)",
// "none", "body", "query" — and reads legitimately have no dry-run at all.
func normalizeDryRunCell(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if idx := strings.Index(s, "("); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

func TestSkillsDocEndpointTables(t *testing.T) {
	docs, err := filepath.Glob(filepath.Join("..", "..", "skills", "*.md"))
	if err != nil || len(docs) == 0 {
		t.Fatalf("no skills/*.md found (err=%v)", err)
	}
	docs = append(docs, filepath.Join("..", "..", "README.md"))
	root := Cmd()

	rows := 0
	for _, doc := range docs {
		raw, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			m := tableRowRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			where := fmt.Sprintf("%s:%d", doc, i+1)
			docCmd, docVerb, docPath, docAbility, docDryRun := m[1], m[2], m[3], m[4], normalizeDryRunCell(m[5])

			// Drop placeholder positionals ("<id>", "<booking-id>") before
			// resolving; they are documentation, not command names.
			var tokens []string
			for _, tok := range strings.Fields(docCmd)[1:] { // skip "inventory"
				if strings.HasPrefix(tok, "<") {
					break
				}
				tokens = append(tokens, tok)
			}
			cmd, used := resolveCommand(root, tokens)
			if used != len(tokens) {
				t.Errorf("%s: table documents `%s`, but %q is not a command (resolved only as far as `%s`)",
					where, docCmd, tokens[used], cmd.CommandPath())
				continue
			}
			rows++

			// Fail CLOSED on missing annotations. Commands built outside
			// bindCommands (uploadCmd) must set them by hand; without this
			// check such a command would silently pass all four column
			// assertions and the row would go unverified.
			if cmd.Annotations["verb"] == "" || cmd.Annotations["path"] == "" {
				t.Errorf("%s: `%s` — command `%s` carries no verb/path annotation, so its table row "+
					"cannot be verified. Hand-built commands must set Annotations{verb,path,ability,dryRun}.",
					where, docCmd, cmd.CommandPath())
				continue
			}
			if got := cmd.Annotations["verb"]; got != docVerb {
				t.Errorf("%s: `%s` — table says %s, command calls %s", where, docCmd, docVerb, got)
			}
			if got := cmd.Annotations["path"]; got != docPath {
				t.Errorf("%s: `%s` — table says path %s, command calls %s", where, docCmd, docPath, got)
			}
			if got := cmd.Annotations["ability"]; got != "" && got != docAbility {
				t.Errorf("%s: `%s` — table says ability %s, command requires %s", where, docCmd, docAbility, got)
			}

			// Dry-run column. Reads have no dry-run concept and are written
			// "n/a"; mutations must match the DryRunMode the command binds.
			want := cmd.Annotations["dryRun"]
			if want == "" { // read command
				if docDryRun != "n/a" {
					t.Errorf("%s: `%s` — read command, but dry-run column says %q (expected \"n/a\")",
						where, docCmd, docDryRun)
				}
				continue
			}
			if docDryRun != want {
				t.Errorf("%s: `%s` — table says dry-run %q, command binds DryRunMode %q",
					where, docCmd, docDryRun, want)
			}
		}
	}

	if rows == 0 {
		t.Fatal("matched no endpoint-table rows — the row parser is broken, not the docs")
	}
	t.Logf("validated %d endpoint-table rows across %d docs", rows, len(docs))
}

// TestCutAtShellOperator guards the truncation bug that made TestSkillsDocDrift
// report success on an invocation it had only half-read. A `)` inside
// `--label "Shoe size (EU)"` cut the invocation there, so every flag after it
// went unvalidated — which is how `questions create --position 2` stayed in
// the docs despite no such flag existing. A silently-truncated invocation is
// worse than an unparsed one, because it passes.
func TestCutAtShellOperator(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// The regression. Everything after the quoted `)` must survive.
			name: "paren inside double quotes does not truncate",
			in:   ` inventory questions create --label "Shoe size (EU)" --required true`,
			want: ` inventory questions create --label "Shoe size (EU)" --required true`,
		},
		{
			// The reason `)` is a cut token at all: the $(ceebee …) capture
			// idiom. An unquoted `)` still ends the invocation.
			name: "unquoted paren still ends the invocation",
			in:   ` inventory whoami --format json)`,
			want: ` inventory whoami --format json`,
		},
		{
			// jq programs are single-quoted and contain `|` constantly. The
			// pipe that matters is the one before jq, not the ones inside it.
			name: "pipe outside quotes cuts, pipe inside single quotes does not",
			in:   ` inventory bookings list | jq '.data[] | .id'`,
			want: ` inventory bookings list `,
		},
		{
			name: "hash inside quotes is not a comment",
			in:   ` inventory discounts create --code "SUMMER#1"`,
			want: ` inventory discounts create --code "SUMMER#1"`,
		},
		{
			name: "trailing comment is cut",
			in:   ` inventory whoami   # validates auth`,
			want: ` inventory whoami   `,
		},
		{
			name: "redirect outside quotes cuts",
			in:   ` inventory whoami > out.json`,
			want: ` inventory whoami `,
		},
		{
			// JSON payloads are single-quoted and carry `>` and `|` freely
			// inside string values.
			name: "json payload in single quotes survives intact",
			in:   ` inventory availabilities update av_1 --fares '[{"pricing_tier_id":"pt_7","amount":null}]'`,
			want: ` inventory availabilities update av_1 --fares '[{"pricing_tier_id":"pt_7","amount":null}]'`,
		},
		{
			// Inside double quotes a backslash escapes the next character, so
			// the escaped quote must not be read as closing the string.
			name: "escaped quote inside double quotes does not end the string",
			in:   ` inventory products create --name "The \"Big\" One" --slug x`,
			want: ` inventory products create --name "The \"Big\" One" --slug x`,
		},
		{
			name: "two-char operator wins over its one-char prefix",
			in:   ` inventory whoami && echo ok`,
			want: ` inventory whoami `,
		},
		{
			name: "no operator returns the whole segment",
			in:   ` inventory products list --limit 5`,
			want: ` inventory products list --limit 5`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cutAtShellOperator(tc.in); got != tc.want {
				t.Errorf("cutAtShellOperator(%q)\n = %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseInvocation_SeesFlagsAfterAQuotedParen is the end-to-end form of
// the same guard: the flags list parseInvocation hands the drift test must
// include the ones that follow a quoted `)`, or the test validates a prefix
// and calls it a pass.
func TestParseInvocation_SeesFlagsAfterAQuotedParen(t *testing.T) {
	inv, ok := parseInvocation(`ceebee inventory questions create --label "Shoe size (EU)" --position 2 --dry-run`)
	if !ok {
		t.Fatal("parseInvocation returned !ok")
	}
	if !contains(inv.Flags, "--position") {
		t.Errorf("flags = %v; want them to include --position (the flag that slipped through)", inv.Flags)
	}
	if !contains(inv.Flags, "--dry-run") {
		t.Errorf("flags = %v; want them to include --dry-run", inv.Flags)
	}
}

// TestDocsNeverUseSpaceFormBoolFlags guards the bug class that made
// `--send-now false` dispatch the email it was meant to suppress.
//
// pflag registers every bool with NoOptDefVal="true", so the space form
// `--flag false` sets the flag to TRUE and leaves the bare word "false" as
// a positional argument. Only the `--flag=false` form carries the value.
// Commands declaring positionals caught the stray token via ExactArgs;
// argument-less ones swallowed it under cobra's default ArbitraryArgs and
// silently did the opposite of what the doc said. bindCommands now binds
// cobra.NoArgs there (see inventory.go), which makes the mistake loud —
// but a doc that still prints the space form is teaching an invocation
// that now hard-errors, so the docs have to stay in the equals form.
//
// The check is doc-driven rather than grep-driven: it resolves each
// invocation against the real cobra tree and asks the flag whether it is
// actually a bool, so a string flag that legitimately takes the literal
// word "true" is never flagged.
func TestDocsNeverUseSpaceFormBoolFlags(t *testing.T) {
	docs, err := filepath.Glob(filepath.Join("..", "..", "skills", "*.md"))
	if err != nil || len(docs) == 0 {
		t.Fatalf("no skills/*.md found (err=%v)", err)
	}
	docs = append(docs, filepath.Join("..", "..", "README.md"))
	root := Cmd()

	checked := 0
	for _, doc := range docs {
		for _, inv := range scanDoc(t, doc) {
			if len(inv.Args) == 0 || inv.Args[0] != root.Name() {
				continue
			}
			cmd, _ := resolveCommand(root, inv.Args[1:])
			// Walk the token stream: a bool flag followed by a bare
			// true/false is the bug. `--flag=false` never reaches here
			// because the value is part of the same token.
			for i := 0; i < len(inv.Args)-1; i++ {
				tok, next := inv.Args[i], inv.Args[i+1]
				if !strings.HasPrefix(tok, "--") || strings.Contains(tok, "=") {
					continue
				}
				if next != "true" && next != "false" {
					continue
				}
				f := cmd.Flags().Lookup(strings.TrimPrefix(tok, "--"))
				if f == nil {
					f = cmd.InheritedFlags().Lookup(strings.TrimPrefix(tok, "--"))
				}
				if f == nil || f.Value.Type() != "bool" {
					continue
				}
				checked++
				t.Errorf("%s:%d: `%s` — %s is a bool flag, so `%s %s` sets it to TRUE "+
					"and leaves %q as a stray argument (pflag NoOptDefVal). Write `%s=%s`.",
					inv.File, inv.Line, inv.Text, tok, tok, next, next, tok, next)
			}
		}
	}
	if checked == 0 {
		t.Logf("no space-form bool invocations in the docs")
	}
}
