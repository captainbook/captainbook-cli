package inventory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// helperEnv is the env var our cross-process helper checks to know it's
// running as a child writer instead of as a normal `go test`.
const helperEnv = "CEEBEE_AUDIT_HELPER_PATH"

// TestMain double-dispatches: when invoked with the helper env var set, it
// becomes a child process that performs N audit writes and exits. This is
// the engine for the cross-process flock test below.
func TestMain(m *testing.M) {
	if path := os.Getenv(helperEnv); path != "" {
		runHelperWriter(path)
		return
	}
	os.Exit(m.Run())
}

// runHelperWriter is invoked in the child process. It opens the audit log
// at the given path and writes N entries tagged with a per-process prefix
// so the parent can verify nothing was lost. Uses the SAME FileLogger code
// path as production — that's the whole point.
func runHelperWriter(path string) {
	prefix := os.Getenv("CEEBEE_AUDIT_HELPER_PREFIX")
	countStr := os.Getenv("CEEBEE_AUDIT_HELPER_COUNT")
	thresholdStr := os.Getenv("CEEBEE_AUDIT_HELPER_THRESHOLD")
	count, _ := strconv.Atoi(countStr)
	threshold, _ := strconv.ParseInt(thresholdStr, 10, 64)
	if count == 0 {
		count = 50
	}

	opts := []FileLoggerOption{}
	if threshold > 0 {
		opts = append(opts, WithThresholdBytes(threshold))
	}
	logger, err := NewFileLogger(path, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: NewFileLogger:", err)
		os.Exit(2)
	}
	for i := 0; i < count; i++ {
		_ = logger.Append(AuditEntry{
			Tenant:         "helper-tenant",
			Command:        "inventory.helper.write",
			Endpoint:       "POST /api/v1/cli/helper",
			IdempotencyKey: fmt.Sprintf("%s-%d", prefix, i),
			BodySHA256:     "deadbeef",
			AbilityUsed:    "cli:write",
			Status:         200,
			DurationMs:     1,
		})
	}
}

// makeEntry builds a representative AuditEntry for tests. Keeps the noise
// out of every test body so each one focuses on its own assertion.
func makeEntry(key string) AuditEntry {
	return AuditEntry{
		Profile:        "prod",
		Tenant:         "acme",
		Command:        "inventory.products.update",
		Endpoint:       "POST /api/v1/cli/products",
		IdempotencyKey: key,
		BodySHA256:     "ab" + strings.Repeat("0", 62),
		AbilityUsed:    "cli:write",
		DryRun:         false,
		Status:         200,
		ResponseID:     "prod_123",
		DurationMs:     42,
		ForensicSummary: map[string]any{
			"product_id": 1,
			"price":      9.99,
		},
	}
}

func TestFileLogger_AppendWritesValidJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := logger.Append(makeEntry("k-1")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("expected trailing newline, got %q", string(data))
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var got AuditEntry
	if err := json.Unmarshal(lines[0], &got); err != nil {
		t.Fatalf("invalid JSON line: %v", err)
	}
	if got.IdempotencyKey != "k-1" {
		t.Errorf("idempotency_key: got %q", got.IdempotencyKey)
	}
	if got.Version != AuditSchemaVersion {
		t.Errorf("version: got %d, want %d", got.Version, AuditSchemaVersion)
	}
	if got.Ts.IsZero() {
		t.Error("ts should be populated by Append when caller leaves it zero")
	}
}

func TestFileLogger_FilePerms0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits don't translate cleanly on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := logger.Append(makeEntry("k-perm")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("perms: got %#o, want 0600", mode)
	}
}

func TestFileLogger_AllSchemaFieldsPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	entry := makeEntry("k-schema")
	entry.ErrorCode = "E_TEST"
	entry.Ts = time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	if err := logger.Append(entry); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Round-trip into a generic map so we can assert presence of every
	// field by its JSON name (catches accidental tag drift).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	required := []string{
		"ts", "profile", "tenant", "command", "endpoint", "idempotency_key",
		"body_sha256", "ability_used", "dry_run", "status", "response_id",
		"duration_ms", "error_code", "forensic_summary", "version",
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			t.Errorf("missing schema field %q in %v", k, m)
		}
	}
	if v, _ := m["version"].(float64); int(v) != AuditSchemaVersion {
		t.Errorf("version: got %v, want %d", m["version"], AuditSchemaVersion)
	}
}

func TestFileLogger_ForensicSummaryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	original := AuditEntry{
		Tenant:         "acme",
		Command:        "inventory.cs.refund",
		Endpoint:       "POST /api/v1/cli/refunds",
		IdempotencyKey: "k-fs-1",
		BodySHA256:     "cafe",
		AbilityUsed:    "cli:cs",
		Status:         200,
		DurationMs:     7,
		ForensicSummary: map[string]any{
			"amount":         "12.50",
			"reason":         "customer_request",
			"transaction_id": "txn_abc",
		},
	}
	if err := logger.Append(original); err != nil {
		t.Fatalf("Append: %v", err)
	}

	r, _ := NewReader(path)
	got, err := r.Show("k-fs-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	for _, k := range []string{"amount", "reason", "transaction_id"} {
		if _, ok := got.ForensicSummary[k]; !ok {
			t.Errorf("missing forensic_summary key %q", k)
		}
	}
	if got.ForensicSummary["reason"] != "customer_request" {
		t.Errorf("reason: %v", got.ForensicSummary["reason"])
	}
}

// TestFileLogger_DiskFailureDegrades simulates a disk failure by chmod'ing
// the parent dir to read-only on POSIX. On the failure we expect: warn
// printed to the configured warn writer, Append returns nil (D15).
func TestFileLogger_DiskFailureDegrades(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dir-perm-based failure injection is POSIX-only")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod-based failure injection won't deny writes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	var stderr bytes.Buffer
	logger, err := NewFileLogger(path, WithWarnWriter(&stderr))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	// Make the directory read-only so the OpenFile inside Append fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	got := logger.Append(makeEntry("k-degrade"))
	if got != nil {
		t.Errorf("Append should return nil on disk failure (D15); got %v", got)
	}
	if stderr.Len() == 0 {
		t.Error("expected a warning on stderr; got none")
	}
	if !strings.Contains(stderr.String(), "audit log write failed") {
		t.Errorf("warning text mismatch: %q", stderr.String())
	}
}

// TestFileLogger_RotatesAtThreshold writes enough entries to trigger a
// single rotation and asserts:
//   - audit.jsonl.1 exists, holds the pre-rotation entries
//   - audit.jsonl is fresh and contains only post-rotation entries
//   - the entries we wrote round-trip through Reader (no losses inside
//     the retention window)
func TestFileLogger_RotatesAtThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	// 1 KB threshold so rotation triggers within a few entries. We pick
	// an entry count low enough that we stay within the maxRotations=3
	// retention window — otherwise the oldest entries would be dropped
	// by design and "all entries readable" would not hold.
	logger, err := NewFileLogger(path, WithThresholdBytes(1024))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	// Each entry serializes to roughly 250 bytes. ~5 entries fills the
	// 1 KB threshold, so 8 entries triggers exactly one rotation.
	const n = 8
	for i := 0; i < n; i++ {
		if err := logger.Append(makeEntry(fmt.Sprintf("k-rot-%d", i))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf(".1 should exist after rotation: %v", err)
	}
	infoActive, err := os.Stat(path)
	if err != nil {
		t.Fatalf("active log should exist after rotation: %v", err)
	}
	if infoActive.Size() == 0 {
		t.Error("active log should contain at least one post-rotation entry")
	}

	// Within the retention window every entry must still be readable.
	r, _ := NewReader(path)
	all, err := r.List(0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != n {
		t.Errorf("expected %d entries across log + rotations, got %d", n, len(all))
	}
}

// TestFileLogger_KeepsLastNRotations forces enough rotations that the
// retention policy must drop the oldest. With maxRotations=3, after many
// rotations we expect .1, .2, .3 to exist and .4 to NOT exist.
func TestFileLogger_KeepsLastNRotations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path,
		WithThresholdBytes(512),
		WithMaxRotations(3),
	)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	// Force several rotations.
	for i := 0; i < 100; i++ {
		if err := logger.Append(makeEntry(fmt.Sprintf("k-keep-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	for n := 1; n <= 3; n++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", path, n)); err != nil {
			t.Errorf(".%d should exist: %v", n, err)
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Error(".4 should NOT exist (retention policy violated)")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".4 stat: unexpected error %v", err)
	}
}

// TestFileLogger_ParallelGoroutines launches N goroutines, each writing M
// entries, and asserts every one lands as a parseable JSON line — i.e.
// the in-process mutex + file lock combination prevents interleaved
// writes within a single process.
func TestFileLogger_ParallelGoroutines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	const goroutines = 10
	const perGoroutine = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = logger.Append(makeEntry(fmt.Sprintf("g%d-i%d", g, i)))
			}
		}(g)
	}
	wg.Wait()

	r, _ := NewReader(path)
	all, err := r.List(0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != goroutines*perGoroutine {
		t.Errorf("expected %d entries, got %d", goroutines*perGoroutine, len(all))
	}

	// Verify there are no corrupt lines (List skips them silently, so
	// we re-read raw and check every non-empty line parses).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for i, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Errorf("line %d failed to parse: %v: %q", i, err, line)
		}
	}
}

// TestFileLogger_CrossProcessFlock spawns sibling processes (via the
// TestMain helper above) that all write to the same audit.jsonl
// concurrently. This is the load-bearing flock test: without
// cross-process locking, lines would interleave and parsing would fail.
func TestFileLogger_CrossProcessFlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	// Locate this test binary so we can re-exec it as a child writer.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	const procs = 4
	const perProc = 75
	var wg sync.WaitGroup
	errs := make(chan error, procs)
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			cmd := exec.Command(self, "-test.run=^$") // don't actually run any tests in the child
			cmd.Env = append(os.Environ(),
				helperEnv+"="+path,
				"CEEBEE_AUDIT_HELPER_PREFIX=p"+strconv.Itoa(p),
				"CEEBEE_AUDIT_HELPER_COUNT="+strconv.Itoa(perProc),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				errs <- fmt.Errorf("proc %d: %v: %s", p, err, out)
			}
		}(p)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	// All entries must be parseable, count must match, and every prefix
	// must show up the expected number of times.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	counts := make(map[string]int, procs)
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	parsed := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("corrupt line under flock: %v: %q", err, line)
		}
		parsed++
		// IdempotencyKey is "<prefix>-<i>"; group by prefix.
		dash := strings.LastIndexByte(e.IdempotencyKey, '-')
		if dash < 0 {
			continue
		}
		counts[e.IdempotencyKey[:dash]]++
	}
	if parsed != procs*perProc {
		t.Errorf("expected %d entries, got %d (lost lines under flock)", procs*perProc, parsed)
	}
	for p := 0; p < procs; p++ {
		want := perProc
		got := counts["p"+strconv.Itoa(p)]
		if got != want {
			t.Errorf("proc %d: got %d entries, want %d", p, got, want)
		}
	}
}

func TestReader_ListMostRecentFirstAcrossRotations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	// Big retention so the oldest entry survives — we want to verify the
	// reader traverses rotations in the right order, not that retention
	// kicks in (covered separately).
	logger, err := NewFileLogger(path, WithThresholdBytes(512), WithMaxRotations(10))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	const n = 12
	for i := 0; i < n; i++ {
		entry := makeEntry(fmt.Sprintf("k-list-%d", i))
		// Make timestamps strictly increasing so we can verify ordering.
		entry.Ts = time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)
		if err := logger.Append(entry); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	r, _ := NewReader(path)
	got, err := r.List(5)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("len: got %d, want 5", len(got))
	}
	// Most recent first → got[0].Ts should be after got[4].Ts.
	if !got[0].Ts.After(got[4].Ts) {
		t.Errorf("not newest-first: %v vs %v", got[0].Ts, got[4].Ts)
	}
	// And the first entry should be the last we wrote.
	wantKey := fmt.Sprintf("k-list-%d", n-1)
	if got[0].IdempotencyKey != wantKey {
		t.Errorf("newest key: got %q, want %q", got[0].IdempotencyKey, wantKey)
	}
}

func TestReader_ShowFindsEntryInRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	// Generous retention so a few rotations don't evict the entry we
	// look up. The point of THIS test is "Show traverses rotations",
	// not "rotation evicts oldest" (covered separately).
	logger, err := NewFileLogger(path, WithThresholdBytes(512), WithMaxRotations(10))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	// Write enough entries to push k-show-0 into a rotation but not so
	// many that retention drops it.
	for i := 0; i < 8; i++ {
		if err := logger.Append(makeEntry(fmt.Sprintf("k-show-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Confirm we did rotate.
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotation to have happened: %v", err)
	}

	r, _ := NewReader(path)
	got, err := r.Show("k-show-0")
	if err != nil {
		t.Fatalf("Show k-show-0: %v", err)
	}
	if got.IdempotencyKey != "k-show-0" {
		t.Errorf("got idempotency_key %q", got.IdempotencyKey)
	}
}

func TestReader_ShowMissingReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := logger.Append(makeEntry("k-only")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	r, _ := NewReader(path)

	if _, err := r.Show("does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Show missing: got %v, want ErrNotFound", err)
	}
	if _, err := r.Show(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("Show empty: got %v, want ErrNotFound", err)
	}
}

func TestReader_ListOnMissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	r, err := NewReader(path)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, err := r.List(10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %d entries", len(got))
	}
}

func TestReader_ToleratesCorruptLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := logger.Append(makeEntry("k-good-1")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Inject a corrupt line in the middle of the file.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString("not-json\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()
	if err := logger.Append(makeEntry("k-good-2")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	r, _ := NewReader(path)
	got, err := r.List(0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 valid entries (corrupt line skipped), got %d", len(got))
	}
}

// TestPIIContractByOmission asserts the audit package's storage contract:
// ForensicSummary contains exactly what the caller passed, with no fields
// silently injected by the audit package itself. Specifically, when the
// caller deliberately omits PII keys (email, phone, passport,
// customer_name, etc.), they MUST NOT appear in the marshaled JSON. The
// test is here to make the contract a regression-tested invariant rather
// than a vibes-based promise.
func TestPIIContractByOmission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	// Realistic forensic_summary for a refund: all non-PII.
	entry := makeEntry("k-pii")
	entry.Command = "inventory.cs.refund"
	entry.AbilityUsed = "cli:cs"
	entry.ForensicSummary = map[string]any{
		"booking_id":     "bk_42",
		"amount":         "12.50",
		"reason":         "duplicate_charge",
		"transaction_id": "txn_xyz",
	}
	if err := logger.Append(entry); err != nil {
		t.Fatalf("Append: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Bytewise check: PII keys must not appear in the persisted line.
	piiKeys := []string{"email", "phone", "passport", "customer_name", "first_name", "last_name", "address"}
	body := string(data)
	for _, k := range piiKeys {
		if strings.Contains(body, "\""+k+"\"") {
			t.Errorf("audit log unexpectedly contains PII key %q in: %s", k, body)
		}
	}
}

// TestNewFileLogger_EmptyPathErrors guards the basic precondition.
func TestNewFileLogger_EmptyPathErrors(t *testing.T) {
	if _, err := NewFileLogger(""); err == nil {
		t.Error("expected error for empty path")
	}
	if _, err := NewReader(""); err == nil {
		t.Error("expected error for empty path")
	}
}

// TestDefaultAuditPath sanity-checks the default path resolution against
// the homeDirFn convention. Uses the same indirection so a test-side
// override doesn't pollute the user's real home dir.
func TestDefaultAuditPath(t *testing.T) {
	saved := homeDirFn
	t.Cleanup(func() { homeDirFn = saved })
	homeDirFn = func() (string, error) { return "/fake/home", nil }

	got, err := DefaultAuditPath()
	if err != nil {
		t.Fatalf("DefaultAuditPath: %v", err)
	}
	want := filepath.Join("/fake/home", ".ceebee", "audit.jsonl")
	if got != want {
		t.Errorf("DefaultAuditPath: got %q, want %q", got, want)
	}
}
