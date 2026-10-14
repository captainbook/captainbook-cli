package cmd

// Integrity of `.gstack/pregate.json`, the pre-gate's dependency map.
//
// That file tells the pre-gate which tests cover inputs its import graph cannot
// reach by itself: the vendored OpenAPI spec, the generated client, and the
// operator docs under skills/. Those edges are the only thing standing between
// "this file has a test" and `PREGATE_NO_LANE`, and an entry naming a file that
// does not exist is strictly worse than no entry at all — it reports coverage
// that nothing provides.
//
// A runner-level existence check cannot catch it, which is why this test exists.
// Measured on the pre-gate's own resolver: declaring a nonexistent test file for
// a touched path resolves TRANSITIVELY onward to a real test
// (`cmd/docs_drift_test.go ← cmd/inventory/does_not_exist_test.go ← .gitignore`),
// so the phantom file never reaches the runner's `{files}` selection and the
// lane goes green on the strength of a test that was never named. The map has to
// be checked where it is written.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pregateConfig is the subset of pregate-v1 this test reads.
type pregateConfig struct {
	Runner       string              `json:"runner"`
	Dependencies map[string][]string `json:"dependencies"`
}

func loadPregateConfig(t *testing.T) (pregateConfig, string) {
	t.Helper()
	path := filepath.Join("..", ".gstack", "pregate.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\n"+
			"    The pre-gate's dependency map is how non-Go inputs (the vendored spec, the\n"+
			"    generated client, skills/*.md) get a test lane. If it is gone, `gstack-pregate`\n"+
			"    fails those files with PREGATE_NO_LANE rather than passing them silently.", path, err)
	}
	var cfg pregateConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return cfg, path
}

// TestPregateDependenciesNameExistingFiles is the whole point of this file: a
// declared lane must name a test that is really there.
//
// An empty list is the deliberate "no lane needed" declaration (prose files,
// .gitignore, the CI workflows) and is left alone — that is a decision, not a
// missing file.
func TestPregateDependenciesNameExistingFiles(t *testing.T) {
	cfg, path := loadPregateConfig(t)
	if len(cfg.Dependencies) == 0 {
		t.Fatalf("%s declares no dependencies — either the map was emptied or the "+
			"schema changed; both make the pre-gate's lane check weaker than it reads", path)
	}

	checked := 0
	for glob, tests := range cfg.Dependencies {
		for _, rel := range tests {
			checked++
			if _, err := os.Stat(filepath.Join("..", rel)); err != nil {
				t.Errorf("%s: dependencies[%q] names %q, which does not exist.\n"+
					"    The pre-gate resolves a phantom entry transitively onward to a real test, so\n"+
					"    the lane still reports green and the glob's coverage is imaginary. Point the\n"+
					"    entry at a test that exists, or declare [] if the files genuinely need no lane.",
					path, glob, rel)
			}
		}
	}

	// Fail closed. Every non-empty list being skipped would otherwise look
	// identical to every entry being valid.
	if checked == 0 {
		t.Fatalf("%s: every dependency list is empty, so nothing was verified. That is only "+
			"correct if no input in this repo needs a declared lane, which was not true when "+
			"this test was written (the vendored spec, the generated client and skills/*.md all do).", path)
	}
	t.Logf("verified %d declared test lane(s) across %d globs", checked, len(cfg.Dependencies))
}

// TestPregateDependenciesNameTestFiles keeps the map pointing at tests.
//
// A lane naming a non-test file would satisfy the existence check above while
// selecting something the runner cannot execute as a test, so the glob's inputs
// would again be covered only in the file's own telling.
func TestPregateDependenciesNameTestFiles(t *testing.T) {
	cfg, path := loadPregateConfig(t)
	for glob, tests := range cfg.Dependencies {
		for _, rel := range tests {
			if !strings.HasSuffix(rel, "_test.go") {
				t.Errorf("%s: dependencies[%q] names %q, which is not a _test.go file; "+
					"a lane must name a test, not the code under test, and the runner "+
					"executes the selection as Go packages", path, glob, rel)
			}
		}
	}
}

// requiredLaneGlobs are the inputs this repo cannot leave without a test lane.
//
// Each is an input the pre-gate's import graph cannot reach on its own, which
// is the entire reason the dependency map exists: the two vendored specs, the
// generated client, and the operator docs. An empty list is the map's honest
// "no lane needed" declaration for prose, so it is exempt everywhere else —
// but using it HERE would silence the pre-gate for the files most likely to
// break the CLI, and it would do so quietly, where deleting the key outright
// at least fails loudly with PREGATE_NO_LANE.
var requiredLaneGlobs = []string{
	"api/inventory/cli-v1.yaml",
	"api/statistics/statistics-openapi.yml",
	"internal/inventory/gen/*.go",
	"skills/*.md",
	".gstack/pregate.json",
}

// TestPregateRequiredGlobsHaveLanes closes the `[]` escape hatch for the globs
// that must never use it.
func TestPregateRequiredGlobsHaveLanes(t *testing.T) {
	cfg, path := loadPregateConfig(t)
	for _, glob := range requiredLaneGlobs {
		tests, declared := cfg.Dependencies[glob]
		if !declared {
			t.Errorf("%s: no dependencies entry for %q. Without one the pre-gate fails it "+
				"with PREGATE_NO_LANE, which is loud but blocks every ship until declared.", path, glob)
			continue
		}
		if len(tests) == 0 {
			t.Errorf("%s: dependencies[%q] is the empty \"no lane needed\" declaration, but this "+
				"input has real test coverage and losing it is silent — a spec bump or a docs "+
				"change would sail through the pre-gate with no drift test selected.", path, glob)
		}
	}
}

// TestPregateRunnerMatchesTheCIGate pins the runner, the most fragile line in
// the file: one `sh -c` with nested quoting, a {files} substitution used twice
// and a shell pipeline. A typo there breaks the lane for every glob at once,
// and a runner that quietly dropped -race would make the pre-gate weaker than
// the gate CLAUDE.md documents while still reporting pass.
func TestPregateRunnerMatchesTheCIGate(t *testing.T) {
	cfg, path := loadPregateConfig(t)
	if cfg.Runner == "" {
		t.Fatalf("%s declares no runner; the pre-gate reports `lanes` incomplete without one", path)
	}
	for _, want := range []string{"{files}", "go test", "-race", "-count=1"} {
		if !strings.Contains(cfg.Runner, want) {
			t.Errorf("%s: runner %q is missing %q — CLAUDE.md names `go test ./... -race -count=1` "+
				"as the gate CI runs, and a local lane weaker than that reports a pass it did not earn",
				path, cfg.Runner, want)
		}
	}
}
