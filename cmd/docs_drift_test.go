package cmd

// Documentation drift for the parts of the tree `cmd/inventory` cannot see.
//
// cmd/inventory/skills_drift_test.go validates every documented
// `ceebee inventory …` invocation against the live command tree, and says so
// itself: anything whose first token is not `inventory` is skipped. That left
// `stats`, `audit`, `config` and `version` examples validated by NOTHING.
//
// It mattered immediately. This sync added skills/statistics.md — a whole
// cookbook of `ceebee stats` invocations, including a per-metric filter table —
// plus `ceebee audit` examples in skills/index.md. A doc still showing the
// removed `--time-from`, or naming a filter the server answers 400 for, would
// have shipped green. The one prose flag the inventory suite ever caught
// (`bookings list --since`, which never existed) is the same failure in the
// other half of the tree.
//
// The filter-table assertion is the part that cannot drift quietly: the table in
// statistics.md claims which filters each metric applies, and the server REFUSES
// an unsupported one rather than ignoring it, so a wrong row is a documented 400.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/captainbook/captainbook-cli/internal/api"
	"github.com/spf13/cobra"
)

var ceebeeLine = regexp.MustCompile(`\bceebee\s`)

type docInvocation struct {
	File  string
	Line  int
	Text  string
	Args  []string
	Flags []string
}

// scanDocsForCeebee pulls every `ceebee …` invocation out of the shell fences of
// a markdown file. Fenced blocks only, matching the inventory scanner: `text`
// fences hold sample output, not commands.
func scanDocsForCeebee(t *testing.T, path string) []docInvocation {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(raw), "\n")

	var out []docInvocation
	inFence, lang := false, ""
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				inFence, lang = false, ""
			} else {
				inFence, lang = true, strings.TrimPrefix(trimmed, "```")
			}
			continue
		}
		if !inFence {
			continue
		}
		if lang != "" && lang != "bash" && lang != "sh" && lang != "shell" {
			continue
		}
		if !ceebeeLine.MatchString(lines[i]) {
			continue
		}

		start := i + 1
		text := lines[i]
		for strings.HasSuffix(strings.TrimRight(text, " \t"), `\`) && i+1 < len(lines) {
			text = strings.TrimSuffix(strings.TrimRight(text, " \t"), `\`)
			i++
			text += " " + lines[i]
		}

		if inv, ok := parseDocInvocation(text); ok {
			inv.File, inv.Line = path, start
			out = append(out, inv)
		}
	}
	return out
}

// parseDocInvocation splits one line into the args and flags following `ceebee`.
// It stops at a shell pipe or redirect, so `… | jq '…'` does not leak jq's own
// flags into the assertion.
func parseDocInvocation(text string) (docInvocation, bool) {
	// Drop a leading prompt and anything before `ceebee`.
	idx := strings.Index(text, "ceebee ")
	if idx < 0 {
		return docInvocation{}, false
	}
	rest := text[idx+len("ceebee "):]
	for _, stop := range []string{"|", ">", "&&", ";", "2>"} {
		if j := strings.Index(rest, stop); j >= 0 {
			rest = rest[:j]
		}
	}

	inv := docInvocation{Text: strings.TrimSpace(text)}
	for _, tok := range strings.Fields(rest) {
		switch {
		case strings.HasPrefix(tok, "--"):
			name := tok
			if eq := strings.Index(tok, "="); eq >= 0 {
				name = tok[:eq]
			}
			inv.Flags = append(inv.Flags, name)
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			// Short flag; the tree is checked by long name only.
		default:
			// A value belonging to the previous flag, or a subcommand /
			// positional. Only unprefixed tokens seen before any flag can be
			// subcommands.
			if len(inv.Flags) == 0 {
				inv.Args = append(inv.Args, tok)
			}
		}
	}
	return inv, len(inv.Args) > 0 || len(inv.Flags) > 0
}

// docRoot builds the real tree, minus `inventory` (already covered, and it is
// the slow half to construct).
func docRoot() *cobra.Command {
	root := &cobra.Command{Use: "ceebee"}
	root.PersistentFlags().StringP("format", "f", "json", "Output format")
	root.PersistentFlags().String("profile", "", "Config profile")
	root.PersistentFlags().BoolP("verbose", "v", false, "Debug output")
	root.AddCommand(statsCmd())
	root.AddCommand(configCmd())
	root.AddCommand(auditCmd())
	root.AddCommand(versionCmd)
	return root
}

func TestDocsDrift_NonInventoryInvocations(t *testing.T) {
	docs, err := filepath.Glob(filepath.Join("..", "skills", "*.md"))
	if err != nil {
		t.Fatalf("glob skills: %v", err)
	}
	docs = append(docs, filepath.Join("..", "README.md"))

	root := docRoot()
	// Namespaces this test owns. `inventory` belongs to the other suite.
	owned := map[string]bool{"stats": true, "audit": true, "config": true, "version": true}

	checked := 0
	for _, doc := range docs {
		for _, inv := range scanDocsForCeebee(t, doc) {
			if len(inv.Args) == 0 || !owned[inv.Args[0]] {
				continue
			}
			cmd, _, err := root.Find(inv.Args)
			if err != nil {
				t.Errorf("%s:%d: `%s` — %v", inv.File, inv.Line, inv.Text, err)
				continue
			}
			// Find() falls back to the nearest parent, so an unknown verb shows
			// up as a parent that still has children.
			if cmd.HasSubCommands() && len(inv.Args) > 1 {
				t.Errorf("%s:%d: `%s` — %q is not a subcommand of `%s`",
					inv.File, inv.Line, inv.Text, inv.Args[len(inv.Args)-1], cmd.CommandPath())
				continue
			}
			checked++

			for _, fl := range inv.Flags {
				name := strings.TrimPrefix(fl, "--")
				if cmd.Flags().Lookup(name) != nil || cmd.InheritedFlags().Lookup(name) != nil ||
					root.PersistentFlags().Lookup(name) != nil {
					continue
				}
				t.Errorf("%s:%d: `%s` — command `%s` has no %s flag",
					inv.File, inv.Line, inv.Text, cmd.CommandPath(), fl)
			}
		}
	}

	if checked == 0 {
		t.Fatal("scanned no non-inventory ceebee invocations — the doc scanner is broken, not the docs")
	}
	t.Logf("validated %d non-inventory invocations across %d docs", checked, len(docs))
}

// TestDocsDrift_StatisticsFilterTable asserts the per-metric filter table in
// skills/statistics.md against api.Endpoints.
//
// The table tells the operator which filters each metric applies. The server
// refuses an unsupported one with 400 rather than ignoring it, so a row claiming
// a filter a metric does not apply sends the reader to a guaranteed error, and a
// row MISSING a filter hides a capability that exists. Prose cannot be trusted to
// track a 13-row matrix by hand.
func TestDocsDrift_StatisticsFilterTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "skills", "statistics.md"))
	if err != nil {
		t.Fatalf("read statistics.md: %v", err)
	}
	doc := string(raw)

	// Every metric must appear in the doc, and every filter it declares must be
	// named somewhere in the doc's own filter vocabulary.
	checked := 0
	for i := range api.Endpoints {
		ep := &api.Endpoints[i]
		// Require the INVOCATION, not just the word. An earlier version only
		// checked that the name appeared in backticks somewhere, which the
		// filter table alone satisfies — so dropping a metric from the command
		// table left the test green.
		if !strings.Contains(doc, "ceebee stats "+ep.Name) {
			t.Errorf("skills/statistics.md documents no `ceebee stats %s` invocation — the metric is reachable and undocumented", ep.Name)
			continue
		}
		for _, f := range ep.Filters {
			flag := "--" + api.FlagName(f)
			if !strings.Contains(doc, flag) {
				t.Errorf("skills/statistics.md documents no %s, but %q applies it", flag, ep.Name)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("checked no metric/filter pairs — the endpoint table is broken, not the doc")
	}

	// A filter named in the doc that no metric applies would send readers to a
	// 400. Check the vocabulary line's flags resolve to real FilterDefs.
	declared := map[string]bool{}
	for i := range api.Endpoints {
		for _, f := range api.Endpoints[i].Filters {
			declared["--"+api.FlagName(f)] = true
		}
	}
	for _, m := range regexp.MustCompile("`(--[a-z0-9-]+)`").FindAllStringSubmatch(doc, -1) {
		flag := m[1]
		// Common/period flags and the deliberately-documented dead names are not
		// per-metric filters.
		switch flag {
		case "--from", "--to", "--granularity", "--compare-from", "--compare-to",
			"--compare", "--format", "--help", "--time-from", "--time-to", "--limit", "--sort-by":
			continue
		}
		if !declared[flag] {
			t.Errorf("skills/statistics.md names %s as a filter, but no metric declares it — the server would answer 400", flag)
		}
	}
}
