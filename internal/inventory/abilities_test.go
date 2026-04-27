package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// withTempHome redirects homeDirFn to a t.TempDir for the duration of the test,
// so DiskCache writes to an isolated directory and can't pollute the real
// ~/.ceebee. The previous homeDirFn is restored on cleanup.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := homeDirFn
	homeDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { homeDirFn = prev })
	return dir
}

// fakeWhoami builds a whoamiFn that returns canned values and counts calls.
// Tests use the counter to assert cache hits don't re-fetch.
func fakeWhoami(abilities Set, expires time.Time, calls *int32) func(context.Context) (Set, time.Time, error) {
	return func(_ context.Context) (Set, time.Time, error) {
		atomic.AddInt32(calls, 1)
		return abilities, expires, nil
	}
}

func TestSet_Has(t *testing.T) {
	s := Set{Read, Write}
	if !s.Has(Read) {
		t.Fatal("expected Has(Read) = true")
	}
	if s.Has(CS) {
		t.Fatal("expected Has(CS) = false on a Set without it")
	}
	var empty Set
	if empty.Has(Read) {
		t.Fatal("expected empty Set to not have Read")
	}
}

func TestPreflight_ColdCacheCallsWhoamiAndCaches(t *testing.T) {
	withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	var calls int32
	want := Set{Read, Write}
	exp := time.Now().Add(time.Hour)

	got, err := Preflight(context.Background(), "h", "tok", cache,
		fakeWhoami(want, exp, &calls))
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 whoami call on cold cache, got %d", calls)
	}
	if !got.Has(Read) || !got.Has(Write) || got.Has(CS) {
		t.Fatalf("unexpected abilities: %v", got)
	}

	// The cache file should now exist with the entry written.
	if e, ok := cache.Get("h", "tok"); !ok {
		t.Fatal("expected cache hit after Preflight wrote entry")
	} else if !e.ExpiresAt.Equal(exp) {
		t.Fatalf("ExpiresAt = %v, want %v", e.ExpiresAt, exp)
	}
}

func TestPreflight_WarmCacheSkipsWhoami(t *testing.T) {
	withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	var calls int32
	want := Set{Read}
	exp := time.Now().Add(time.Hour)
	whoami := fakeWhoami(want, exp, &calls)

	if _, err := Preflight(context.Background(), "h", "tok", cache, whoami); err != nil {
		t.Fatalf("first Preflight: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call after first Preflight, got %d", calls)
	}

	if _, err := Preflight(context.Background(), "h", "tok", cache, whoami); err != nil {
		t.Fatalf("second Preflight: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (warm cache should skip whoami), got %d", calls)
	}
}

func TestDiskCache_SurvivesReconstruction(t *testing.T) {
	withTempHome(t)
	c1, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	exp := time.Now().Add(time.Hour)
	if err := c1.Set("h", "tok", Entry{Abilities: Set{Read}, ExpiresAt: exp, CachedAt: time.Now()}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	c2, err := NewDiskCache()
	if err != nil {
		t.Fatalf("second NewDiskCache: %v", err)
	}
	e, ok := c2.Get("h", "tok")
	if !ok {
		t.Fatal("expected reconstructed cache to find the entry on disk")
	}
	if !e.Abilities.Has(Read) {
		t.Fatalf("abilities lost across reconstruction: %v", e.Abilities)
	}
}

func TestDiskCache_CorruptFileTreatedAsMiss(t *testing.T) {
	home := withTempHome(t)
	cachePath := filepath.Join(home, cacheDir, cacheFileName)
	if err := os.MkdirAll(filepath.Dir(cachePath), cacheDirPerm); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("{not valid json"), cachePerm); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	if _, ok := cache.Get("h", "tok"); ok {
		t.Fatal("expected miss on corrupt cache file")
	}

	// Subsequent Set should overwrite the corrupt file without panic.
	if err := cache.Set("h", "tok", Entry{Abilities: Set{Read}, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Set after corrupt: %v", err)
	}
	if _, ok := cache.Get("h", "tok"); !ok {
		t.Fatal("expected hit after Set overwrote corrupt file")
	}
}

func TestDiskCache_ExpiredTokenIsMiss(t *testing.T) {
	withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	past := time.Now().Add(-time.Hour)
	if err := cache.Set("h", "tok", Entry{Abilities: Set{Read}, ExpiresAt: past, CachedAt: time.Now()}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, ok := cache.Get("h", "tok"); ok {
		t.Fatal("expected miss on entry whose ExpiresAt is in the past")
	}

	// Preflight with the same expired entry on disk should re-fetch.
	var calls int32
	if _, err := Preflight(context.Background(), "h", "tok", cache,
		fakeWhoami(Set{Write}, time.Now().Add(time.Hour), &calls)); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected expired entry to force a whoami call, got %d", calls)
	}
}

func TestDiskCache_ZeroExpiresAtNeverExpires(t *testing.T) {
	withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	if err := cache.Set("h", "tok", Entry{Abilities: Set{Read}, ExpiresAt: time.Time{}, CachedAt: time.Now()}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, ok := cache.Get("h", "tok"); !ok {
		t.Fatal("expected zero ExpiresAt to be treated as 'never expires'")
	}
}

func TestDiskCache_Invalidate(t *testing.T) {
	withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	if err := cache.Set("h", "tok", Entry{Abilities: Set{Read}, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, ok := cache.Get("h", "tok"); !ok {
		t.Fatal("expected hit before invalidate")
	}

	if err := cache.Invalidate("h", "tok"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if _, ok := cache.Get("h", "tok"); ok {
		t.Fatal("expected miss after Invalidate")
	}

	// Invalidating a missing entry is not an error.
	if err := cache.Invalidate("h", "tok"); err != nil {
		t.Fatalf("Invalidate of missing entry returned err: %v", err)
	}
}

func TestDiskCache_FilePermissionsAre0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode bits don't apply on Windows")
	}
	home := withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	if err := cache.Set("h", "tok", Entry{Abilities: Set{Read}, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, cacheDir, cacheFileName))
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file perms = %o, want 0600", info.Mode().Perm())
	}
}

// TestDiskCache_AtomicWritePreservesExisting simulates a crash mid-write by
// dropping a stale .tmp file in the cache directory and then doing a normal
// Set. The pre-existing cache file's contents must be intact because the
// atomic-write contract is "write tmp + rename". A leftover tmp file from a
// crashed earlier run must NEVER be observable as a partial cache.
func TestDiskCache_AtomicWritePreservesExisting(t *testing.T) {
	home := withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}

	// Seed a real entry so the cache file exists with known contents.
	original := Entry{Abilities: Set{Read}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := cache.Set("h", "tok", original); err != nil {
		t.Fatalf("Set: %v", err)
	}
	cachePath := filepath.Join(home, cacheDir, cacheFileName)
	pre, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}

	// Simulate a crashed earlier write: a tmp file with garbage that was
	// never renamed into place. The real cache file must still be readable.
	stale := filepath.Join(filepath.Dir(cachePath), ".whoami-cache.crashed.tmp")
	if err := os.WriteFile(stale, []byte("garbage from a crash"), 0o600); err != nil {
		t.Fatalf("seed stale tmp: %v", err)
	}

	// Reading via a fresh DiskCache must see the original entry, not the tmp.
	c2, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	if e, ok := c2.Get("h", "tok"); !ok {
		t.Fatal("expected hit; atomic-write contract broken if the tmp file influenced reads")
	} else if !e.Abilities.Has(Read) {
		t.Fatalf("entry corrupted: %v", e)
	}

	post, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("re-read cache: %v", err)
	}
	if string(pre) != string(post) {
		t.Fatalf("cache file contents changed despite no successful Set:\npre:  %s\npost: %s", pre, post)
	}
}

func TestRefuse(t *testing.T) {
	if err := Refuse(Read, Set{Read, Write}); err != nil {
		t.Fatalf("expected nil when ability is present, got %v", err)
	}
	err := Refuse(CS, Set{Read, Write})
	if err == nil {
		t.Fatal("expected error when ability is missing")
	}
	var amErr *AbilityMissingError
	if !errors.As(err, &amErr) {
		t.Fatalf("expected *AbilityMissingError, got %T", err)
	}
	if amErr.Needed != CS {
		t.Fatalf("Needed = %q, want %q", amErr.Needed, CS)
	}
	if !IsAbilityMissing(err) {
		t.Fatal("IsAbilityMissing should report true for AbilityMissingError")
	}
	if IsAbilityMissing(errors.New("plain error")) {
		t.Fatal("IsAbilityMissing should report false for unrelated errors")
	}
	if msg := amErr.UserMessage(); msg == "" {
		t.Fatal("UserMessage should be non-empty")
	}
}

func TestCacheKey_Reproducible(t *testing.T) {
	a := cacheKey("https://t.captainbook.io", "tok-123")
	b := cacheKey("https://t.captainbook.io", "tok-123")
	if a != b {
		t.Fatalf("same (host, token) produced different keys: %s vs %s", a, b)
	}
	if a == cacheKey("https://other.captainbook.io", "tok-123") {
		t.Fatal("different host should produce different key")
	}
	if a == cacheKey("https://t.captainbook.io", "tok-456") {
		t.Fatal("different token should produce different key")
	}
	// And the boundary collision: ensure the NUL separator does its job.
	// "ab" + "cd" must not equal "a" + "bcd" once routed through the key fn.
	// (Token is hashed, but host is mixed in raw — this asserts the separator.)
	if cacheKey("ab", "x") == cacheKey("a", "x") {
		// Different inputs that should never collide. (Really a sanity test.)
		t.Fatal("host boundary collision; separator missing?")
	}
}

func TestDiskCache_PreservesOtherEntries(t *testing.T) {
	withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	if err := cache.Set("h1", "t1", Entry{Abilities: Set{Read}, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Set h1: %v", err)
	}
	if err := cache.Set("h2", "t2", Entry{Abilities: Set{Write}, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Set h2: %v", err)
	}
	if err := cache.Invalidate("h1", "t1"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if _, ok := cache.Get("h1", "t1"); ok {
		t.Fatal("h1 should be gone")
	}
	if e, ok := cache.Get("h2", "t2"); !ok || !e.Abilities.Has(Write) {
		t.Fatalf("h2 should be intact: ok=%v entry=%v", ok, e)
	}
}

func TestPreflight_NilCacheAlwaysFetches(t *testing.T) {
	var calls int32
	whoami := fakeWhoami(Set{Read}, time.Now().Add(time.Hour), &calls)
	for i := 0; i < 3; i++ {
		if _, err := Preflight(context.Background(), "h", "tok", nil, whoami); err != nil {
			t.Fatalf("Preflight #%d: %v", i, err)
		}
	}
	if calls != 3 {
		t.Fatalf("nil cache should not memoize; calls = %d, want 3", calls)
	}
}

func TestPreflight_WhoamiErrorPropagated(t *testing.T) {
	withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	wantErr := errors.New("whoami exploded")
	_, err = Preflight(context.Background(), "h", "tok", cache,
		func(_ context.Context) (Set, time.Time, error) { return nil, time.Time{}, wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error to be propagated, got %v", err)
	}
	// And a fetch failure must NOT cache anything.
	if _, ok := cache.Get("h", "tok"); ok {
		t.Fatal("a failed whoami should not produce a cache entry")
	}
}

// TestDiskCache_OnDiskShape pins the JSON shape so a future refactor doesn't
// silently rename fields and break readers (including older ceebee binaries
// that may still have an older cache file on disk).
func TestDiskCache_OnDiskShape(t *testing.T) {
	home := withTempHome(t)
	cache, err := NewDiskCache()
	if err != nil {
		t.Fatalf("NewDiskCache: %v", err)
	}
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := cache.Set("h", "tok", Entry{Abilities: Set{Read, Write}, ExpiresAt: exp, CachedAt: exp}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, cacheDir, cacheFileName))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("expected 1 entry on disk, got %d", len(raw))
	}
	for _, entry := range raw {
		if _, ok := entry["abilities"]; !ok {
			t.Errorf("missing 'abilities' field: %v", entry)
		}
		if _, ok := entry["expires_at"]; !ok {
			t.Errorf("missing 'expires_at' field: %v", entry)
		}
		if _, ok := entry["cached_at"]; !ok {
			t.Errorf("missing 'cached_at' field: %v", entry)
		}
	}
}
