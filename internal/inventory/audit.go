// audit.go implements the local mutation audit log for ceebee (Lane C).
//
// Every cli:write / cli:cs mutation appends a structured JSONL entry to
// ~/.ceebee/audit.jsonl after the mutation succeeds. The append-and-rotate
// critical section is guarded by an OS file lock (flock on POSIX, LockFileEx
// on Windows; see audit_flock_unix.go / audit_flock_windows.go) so concurrent
// ceebee processes can never interleave entries or race on rotation.
//
// Schema (pinned, v1):
//
//	{
//	  "ts":               RFC3339,
//	  "profile":          string|null,
//	  "tenant":           string,
//	  "command":          string,
//	  "endpoint":         string,
//	  "idempotency_key":  uuid,
//	  "body_sha256":      hex string,
//	  "ability_used":     "cli:read|cli:write|cli:cs",
//	  "dry_run":          bool,
//	  "status":           int,
//	  "response_id":      string|null,
//	  "duration_ms":      int,
//	  "error_code":       string|null,
//	  "forensic_summary": object|null,
//	  "version":          1
//	}
//
// Decisions: D15 (write failure degrades to stderr warn), D19 (50 MB
// rotation, keep last 3), D36 (flock around append + rotate), D37
// (forensic_summary is opaque to the audit pkg — caller decides shape).
package inventory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// AuditSchemaVersion is the version stamped on every AuditEntry written
// through FileLogger. Bumping it is a breaking change for downstream
// parsers and must be coordinated with Lane H + the docs team.
const AuditSchemaVersion = 1

// DefaultRotateThresholdBytes is the size threshold at which the active
// audit.jsonl file is rotated. Per D19: 50 MB.
const DefaultRotateThresholdBytes int64 = 50 * 1024 * 1024

// MaxRotations is the number of rotated files we keep on disk. Per D19:
// keep last 3, so we hold audit.jsonl.1 through audit.jsonl.3 (~150 MB max).
const MaxRotations = 3

// auditFilePerm is the permission for audit.jsonl and its rotations. 0600
// follows the same convention as ~/.ceebee/config.yaml.
const auditFilePerm os.FileMode = 0o600

// auditDirPerm matches internal/config: 0700 for ~/.ceebee.
const auditDirPerm os.FileMode = 0o700

// auditDirName is the home-relative directory for ceebee state files.
const auditDirName = ".ceebee"

// auditFileName is the active log filename.
const auditFileName = "audit.jsonl"

// homeDirFn is an indirection that lets tests redirect the audit path to a
// t.TempDir without touching the real ~/.ceebee. Tests assign and restore
// it directly. (Lane B's abilities.go uses the same pattern; once that
// lane lands the two will share this single var.)
var homeDirFn = os.UserHomeDir

// ErrNotFound is returned by Reader.Show when no entry matches the given
// idempotency key across audit.jsonl and its rotations.
var ErrNotFound = errors.New("audit: entry not found")

// AuditEntry is the JSONL record written for each mutation.
//
// Named AuditEntry (rather than just Entry) because the inventory package
// also defines other Entry-shaped types (e.g. the whoami cache entry in
// Lane B). The JSON tags match the spec exactly regardless of the Go
// type name.
//
// ForensicSummary is left to the caller (the per-CommandDef wiring in Lane
// H). The audit package does not interpret its contents — it only stores
// what the caller passes. This is the seam that keeps PII off disk: the
// audit package never reaches into request/response bodies.
type AuditEntry struct {
	Ts              time.Time      `json:"ts"`
	Profile         string         `json:"profile,omitempty"`
	Tenant          string         `json:"tenant"`
	Command         string         `json:"command"`
	Endpoint        string         `json:"endpoint"`
	IdempotencyKey  string         `json:"idempotency_key"`
	BodySHA256      string         `json:"body_sha256"`
	AbilityUsed     string         `json:"ability_used"`
	DryRun          bool           `json:"dry_run"`
	Status          int            `json:"status"`
	ResponseID      string         `json:"response_id,omitempty"`
	DurationMs      int64          `json:"duration_ms"`
	ErrorCode       string         `json:"error_code,omitempty"`
	ForensicSummary map[string]any `json:"forensic_summary,omitempty"`
	Version         int            `json:"version"`
}

// FileLogger appends AuditEntry records to a JSONL file with cross-process
// safety and size-based rotation.
//
// Use NewFileLogger to construct. Append is safe for concurrent calls from
// any number of goroutines and processes.
type FileLogger struct {
	path           string
	thresholdBytes int64
	maxRotations   int
	warnW          io.Writer
	mu             sync.Mutex // serializes in-process writers; the OS file lock handles cross-process
}

// FileLoggerOption customizes a FileLogger. Used primarily by tests to
// shrink the rotation threshold; production callers should not need any
// options.
type FileLoggerOption func(*FileLogger)

// WithThresholdBytes overrides the rotation threshold. Used by tests; the
// production default is 50 MB (DefaultRotateThresholdBytes). Values <= 0
// are ignored so a misconfigured caller can't disable rotation entirely.
func WithThresholdBytes(n int64) FileLoggerOption {
	return func(l *FileLogger) {
		if n > 0 {
			l.thresholdBytes = n
		}
	}
}

// WithMaxRotations overrides the rotation retention count. Defaults to
// MaxRotations (3). Values <= 0 are ignored.
func WithMaxRotations(n int) FileLoggerOption {
	return func(l *FileLogger) {
		if n > 0 {
			l.maxRotations = n
		}
	}
}

// WithWarnWriter overrides the writer used for degraded-write warnings.
// Defaults to os.Stderr. Used by tests to capture stderr.
func WithWarnWriter(w io.Writer) FileLoggerOption {
	return func(l *FileLogger) {
		if w != nil {
			l.warnW = w
		}
	}
}

// NewFileLogger returns a FileLogger that appends to the given path. The
// parent directory is created with 0700 if missing. The audit file itself
// is created lazily on first Append with 0600 permissions.
func NewFileLogger(path string, opts ...FileLoggerOption) (*FileLogger, error) {
	if path == "" {
		return nil, errors.New("audit: empty path")
	}
	l := &FileLogger{
		path:           path,
		thresholdBytes: DefaultRotateThresholdBytes,
		maxRotations:   MaxRotations,
		warnW:          os.Stderr,
	}
	for _, opt := range opts {
		opt(l)
	}
	if err := os.MkdirAll(filepath.Dir(path), auditDirPerm); err != nil {
		return nil, fmt.Errorf("audit: creating parent dir: %w", err)
	}
	return l, nil
}

// Close releases any held resources. The current implementation opens the
// file per-Append (so the OS file lock is naturally scoped to one append),
// which makes Close a no-op. It exists for forward compatibility — if we
// ever pin an open fd to amortize syscall cost, callers should already be
// calling Close.
func (l *FileLogger) Close() error { return nil }

// Append writes one JSONL line for entry. It is safe to call concurrently
// from multiple goroutines and from multiple processes against the same
// path.
//
// Per D15, write failures (disk full, perms denied, etc.) are NOT propagated
// to the caller — they emit a warning to stderr and return nil. The mutation
// already succeeded server-side; losing the local audit log entry is
// informational, not fatal. Callers should treat audit.Append as
// best-effort.
func (l *FileLogger) Append(entry AuditEntry) error {
	if entry.Version == 0 {
		entry.Version = AuditSchemaVersion
	}
	if entry.Ts.IsZero() {
		entry.Ts = time.Now().UTC()
	}

	line, err := json.Marshal(entry)
	if err != nil {
		// Marshaling errors are programmer errors (bad map types). Surface
		// to stderr like every other audit failure, return nil.
		l.warn("marshal entry: %v", err)
		return nil
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.appendLocked(line); err != nil {
		l.warn("write audit entry: %v", err)
		return nil
	}
	return nil
}

// appendLocked performs the open + flock + maybe-rotate + write + unlock +
// close cycle. Returns the underlying error so the caller can decide how
// to surface it; Append wraps that into the D15 stderr-warn behavior.
func (l *FileLogger) appendLocked(line []byte) error {
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, auditFilePerm)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	// Belt-and-braces: chmod in case umask softened the perms on creation.
	_ = os.Chmod(l.path, auditFilePerm)

	if err := lockFile(f); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlockFile(f)

	// Inside the lock: check size, rotate if needed, then append. Rotation
	// happens AFTER acquiring the lock so two processes can't both decide
	// to rotate at once.
	rotated, err := l.maybeRotateLocked(f)
	if err != nil {
		return fmt.Errorf("rotate: %w", err)
	}

	// If we rotated, our existing fd points at the renamed (rotated) file.
	// Open a fresh handle to the canonical path so the entry lands in the
	// new audit.jsonl. We hold the lock on the old inode through the write
	// — that's deliberate: any sibling process that opened the path before
	// we rotated will still be blocked on the rotated inode's lock and
	// won't write to it after we release. New processes opening the path
	// fresh will get a separate lock on the new inode.
	out := f
	if rotated {
		out, err = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, auditFilePerm)
		if err != nil {
			return fmt.Errorf("reopen after rotate: %w", err)
		}
		defer out.Close()
		_ = os.Chmod(l.path, auditFilePerm)
	}

	if _, err := out.Write(line); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// maybeRotateLocked rotates audit.jsonl when its size meets or exceeds the
// threshold. Must be called with f locked. Returns true if a rotation
// happened.
//
// Rotation order (with maxRotations=3):
//
//	delete audit.jsonl.3 (if exists)
//	rename audit.jsonl.2 -> audit.jsonl.3
//	rename audit.jsonl.1 -> audit.jsonl.2
//	rename audit.jsonl   -> audit.jsonl.1
func (l *FileLogger) maybeRotateLocked(f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat: %w", err)
	}
	if info.Size() < l.thresholdBytes {
		return false, nil
	}

	// Drop the oldest rotation if it exists.
	oldest := l.rotationPath(l.maxRotations)
	if _, err := os.Stat(oldest); err == nil {
		if err := os.Remove(oldest); err != nil {
			return false, fmt.Errorf("remove %s: %w", oldest, err)
		}
	}

	// Promote each existing rotation: N -> N+1.
	for i := l.maxRotations - 1; i >= 1; i-- {
		src := l.rotationPath(i)
		dst := l.rotationPath(i + 1)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return false, fmt.Errorf("rename %s -> %s: %w", src, dst, err)
		}
	}

	// Move the active log to .1.
	if err := os.Rename(l.path, l.rotationPath(1)); err != nil {
		return false, fmt.Errorf("rename %s -> %s: %w", l.path, l.rotationPath(1), err)
	}
	return true, nil
}

// rotationPath returns "<path>.<n>" (e.g. audit.jsonl.1).
func (l *FileLogger) rotationPath(n int) string {
	return fmt.Sprintf("%s.%d", l.path, n)
}

func (l *FileLogger) warn(format string, args ...any) {
	if l.warnW == nil {
		return
	}
	fmt.Fprintf(l.warnW, "ceebee: audit log write failed (mutation succeeded): "+format+"\n", args...)
}

// ----------------------------------------------------------------------------
// Reader: feeds `ceebee audit list` and `ceebee audit show`.
// ----------------------------------------------------------------------------

// Reader provides read-only access to the audit log, transparently iterating
// over the active audit.jsonl plus its rotations.
type Reader struct {
	path string
}

// NewReader returns a Reader rooted at path. The path does not need to
// exist; List on a missing log returns an empty slice, and Show returns
// ErrNotFound.
func NewReader(path string) (*Reader, error) {
	if path == "" {
		return nil, errors.New("audit: empty path")
	}
	return &Reader{path: path}, nil
}

// List returns up to limit entries, most recent first. Limit <= 0 means
// "no limit" — return everything we have.
//
// Lines that fail to parse are skipped silently (a partial last line from
// a crashed writer must not hide the good entries before it). This matches
// the D15 contract: writers may degrade and produce partial state; readers
// are tolerant.
func (r *Reader) List(limit int) ([]AuditEntry, error) {
	files := r.candidateFiles()
	var out []AuditEntry

	// Iterate newest -> oldest (active file first, then .1, .2, .3). Within
	// each file, lines are append-ordered, so we walk forward and emit in
	// reverse so the resulting slice is newest-first overall.
	for _, p := range files {
		entries, err := readJSONL(p)
		if err != nil {
			return nil, err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			out = append(out, entries[i])
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// Show returns the entry with the given idempotency key. Searches the
// active log and all rotations; returns the first match (which is also
// the only match, since keys are unique per mutation).
//
// Returns ErrNotFound if no entry matches.
func (r *Reader) Show(idempotencyKey string) (*AuditEntry, error) {
	if idempotencyKey == "" {
		return nil, ErrNotFound
	}
	for _, p := range r.candidateFiles() {
		entries, err := readJSONL(p)
		if err != nil {
			return nil, err
		}
		for i := range entries {
			if entries[i].IdempotencyKey == idempotencyKey {
				e := entries[i]
				return &e, nil
			}
		}
	}
	return nil, ErrNotFound
}

// candidateFiles returns the list of audit files to search, ordered
// newest-first: the active log, then rotation 1, 2, 3, ... (only the ones
// that actually exist on disk).
func (r *Reader) candidateFiles() []string {
	files := []string{r.path}

	matches, _ := filepath.Glob(r.path + ".*")
	type rot struct {
		idx  int
		path string
	}
	rots := make([]rot, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		dot := -1
		for i := len(base) - 1; i >= 0; i-- {
			if base[i] == '.' {
				dot = i
				break
			}
		}
		if dot < 0 || dot == len(base)-1 {
			continue
		}
		var n int
		_, err := fmt.Sscanf(base[dot+1:], "%d", &n)
		if err != nil || n < 1 {
			continue
		}
		rots = append(rots, rot{n, m})
	}
	sort.Slice(rots, func(i, j int) bool { return rots[i].idx < rots[j].idx })
	for _, r := range rots {
		files = append(files, r.path)
	}
	return files
}

// readJSONL reads a JSONL file and returns its entries in file order
// (oldest -> newest within the file). Missing files return (nil, nil).
// Malformed lines are skipped silently.
func readJSONL(path string) ([]AuditEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []AuditEntry
	scanner := bufio.NewScanner(f)
	// Allow long lines (forensic_summary can be substantial).
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			// Corrupt line — skip. The reader is permissive by design.
			continue
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// DefaultAuditPath returns the canonical audit log path under the user's
// home directory: ~/.ceebee/audit.jsonl. Lane H wires this into the
// mutation pipeline; the audit subcommand also calls it.
func DefaultAuditPath() (string, error) {
	home, err := homeDirFn()
	if err != nil {
		return "", fmt.Errorf("audit: resolving home directory: %w", err)
	}
	return filepath.Join(home, auditDirName, auditFileName), nil
}
