// Package inventory implements the cobra-driven Inventory CLI v1 surface.
//
// This file (abilities.go) is Lane B of the parallelization plan and owns
// token-ability preflight + a disk-backed cache so that repeated CLI
// invocations within a Claude Code session don't re-issue /auth/whoami on
// every command.
//
// State machine (per the plan, decisions D26 amended by D33/D34):
//
//	         ┌────────────────────────────────────────────────────────┐
//	         │             Preflight(ctx, host, token, cache)         │
//	         └────────────────────────────────────────────────────────┘
//	                                  │
//	                                  ▼
//	            ┌─────────────────────────────────────────┐
//	            │  cache.Get(host, token) → Entry, hit?    │
//	            │   miss if: absent | corrupt | expired   │
//	            └─────────────────────────────────────────┘
//	                       │ hit                │ miss
//	                       ▼                    ▼
//	                return Entry          whoamiFn(ctx)
//	                                       │
//	                                       ▼
//	                                cache.Set(host, token, e)
//	                                       │
//	                                       ▼
//	                                return abilities
//
//	On caller-observed 401:  cache.Invalidate(host, token)  → next call refetches.
//
// Why host+token and not profile name (D33): config.Resolve allows env-only,
// profile-only, and env+profile invocations that all resolve to the same
// (host, token). Keying by profile name fragments the cache and forces
// Claude Code to re-whoami every time it switches between bare ceebee and
// `--profile foo` even though both resolve identically. host+token gives one
// shared entry per real (server, credential) pair.
//
// This package intentionally does NOT import internal/inventory/gen — the
// caller (Lane A's transport, eventually) wraps the generated client and
// passes a small whoamiFn callback. Decoupling keeps the cache trivially
// testable with no HTTP fixture.
package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Ability is a typed string for a single token capability returned by the API's
// /auth/whoami endpoint. The set of valid values is defined by the spec and
// hand-mirrored here per D34 (the spec's `cli:cs` etc. are prose, not enum).
type Ability string

const (
	// Read is the read-only ability. Required for `inventory list`, `get`, etc.
	Read Ability = "cli:read"
	// Write is the mutation ability. Required for `inventory update`, etc.
	Write Ability = "cli:write"
	// CS is the customer-success ability. Required for cross-tenant operations.
	CS Ability = "cli:cs"
)

// Set is an unordered list of abilities granted to a token.
type Set []Ability

// Has reports whether a is present in s.
func (s Set) Has(a Ability) bool {
	for _, x := range s {
		if x == a {
			return true
		}
	}
	return false
}

// Entry is the cached whoami result for a single (host, token) pair.
//
// ExpiresAt comes from the server's view of the token, not from a client-side
// TTL — we never invent expiry. CachedAt is the local clock at write time and
// is recorded for observability (e.g. `ceebee whoami --debug` could surface
// "cached 3 minutes ago"); it does NOT participate in expiry logic.
type Entry struct {
	Abilities Set       `json:"abilities"`
	ExpiresAt time.Time `json:"expires_at"` // from token, not from cache TTL
	CachedAt  time.Time `json:"cached_at"`
}

// Cache is the storage interface for whoami results. DiskCache implements it;
// tests can substitute an in-memory implementation if useful, but the disk
// path is what ships.
type Cache interface {
	Get(host, token string) (Entry, bool)
	Set(host, token string, entry Entry) error
	Invalidate(host, token string) error
}

// homeDirFn is the indirection that lets tests redirect the cache file to a
// t.TempDir without touching the real ~/.ceebee. Tests assign and restore it
// with the homeDirForTest helper at the bottom of this file.
var homeDirFn = os.UserHomeDir

const (
	cacheDir       = ".ceebee"
	cacheFileName  = ".whoami-cache.json"
	cacheLockName  = ".whoami-cache.lock"
	cachePerm      = 0o600
	cacheDirPerm   = 0o700
)

// withCacheLock acquires an exclusive cross-process lock on the cache lockfile,
// invokes fn, and releases the lock. The lockfile path is stable across
// rename-based atomic writes of the cache JSON, so the lock truly serializes
// concurrent writers across processes — read-modify-write is safe.
func (c *DiskCache) withCacheLock(fn func() error) error {
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return fmt.Errorf("abilities: creating cache directory: %w", err)
	}
	lockPath := filepath.Join(dir, cacheLockName)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, cachePerm)
	if err != nil {
		return fmt.Errorf("abilities: opening cache lockfile: %w", err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("abilities: acquiring cache lock: %w", err)
	}
	defer unlockFile(f)
	return fn()
}

// DiskCache is backed by ~/.ceebee/.whoami-cache.json.
//
// The file holds a JSON object: {"<key>": Entry, ...} where key is
// sha256(host + 0x00 + sha256(token)). The whole map is loaded on every
// Get/Set so two concurrent ceebee processes don't clobber each other's
// entries — instead each call does a read-modify-write under an atomic
// rename. The mutex below is only for in-process safety; cross-process
// safety relies on the rename being atomic on POSIX file systems.
type DiskCache struct {
	path string
	mu   sync.Mutex
}

// NewDiskCache resolves the cache path under the user's home directory and
// returns a DiskCache. It does NOT create the file — the file is created
// lazily on first Set, with 0600 perms via the atomic-write path.
func NewDiskCache() (*DiskCache, error) {
	home, err := homeDirFn()
	if err != nil {
		return nil, fmt.Errorf("abilities: resolving home directory: %w", err)
	}
	return &DiskCache{path: filepath.Join(home, cacheDir, cacheFileName)}, nil
}

// cacheKey derives the on-disk key for a (host, token) pair.
//
// We hash the token first so the on-disk key never reveals raw token bytes,
// then mix in the host with a NUL separator so that "ab"+"cd" can't collide
// with "a"+"bcd". The result is hex-encoded sha256, 64 chars, JSON-safe.
func cacheKey(host, token string) string {
	tokenHash := sha256.Sum256([]byte(token))
	h := sha256.New()
	h.Write([]byte(host))
	h.Write([]byte{0})
	h.Write(tokenHash[:])
	return hex.EncodeToString(h.Sum(nil))
}

// loadAll reads the cache file and returns the map. A missing or corrupt file
// is treated as an empty map — never an error — so a single bad write can't
// permanently brick the cache. The next successful Set overwrites the corrupt
// file.
func (c *DiskCache) loadAll() map[string]Entry {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return map[string]Entry{}
	}
	out := map[string]Entry{}
	if err := json.Unmarshal(data, &out); err != nil {
		// Corrupt file: pretend it's empty.
		return map[string]Entry{}
	}
	return out
}

// saveAll writes the map atomically: temp file in the same directory, then
// rename. The temp file is chmod'd to 0600 BEFORE the write, and we re-chmod
// the final path after rename in case the FS dropped permission bits across
// the rename (some Linux configurations do).
func (c *DiskCache) saveAll(m map[string]Entry) error {
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return fmt.Errorf("abilities: creating cache directory: %w", err)
	}

	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("abilities: marshaling cache: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".whoami-cache.*.tmp")
	if err != nil {
		return fmt.Errorf("abilities: creating temp cache file: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails before rename. After a
	// successful rename, tmpPath no longer exists so Remove is a harmless no-op.
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(cachePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("abilities: chmod temp cache: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("abilities: writing temp cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("abilities: closing temp cache: %w", err)
	}
	if err := os.Rename(tmpPath, c.path); err != nil {
		return fmt.Errorf("abilities: renaming temp cache: %w", err)
	}
	_ = os.Chmod(c.path, cachePerm)
	return nil
}

// Get returns the cached entry for (host, token) if present and not expired.
// An entry whose token ExpiresAt is non-zero and not after now is treated as
// a miss — the caller will refetch and overwrite it. ExpiresAt == zero time
// means "no expiry known" and is honored as cached forever (only Invalidate
// can evict in that case).
func (c *DiskCache) Get(host, token string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	m := c.loadAll()
	e, ok := m[cacheKey(host, token)]
	if !ok {
		return Entry{}, false
	}
	if !e.ExpiresAt.IsZero() && !time.Now().Before(e.ExpiresAt) {
		return Entry{}, false
	}
	return e, true
}

// Set writes (host, token) -> entry, preserving any other entries in the file.
// The read-modify-write is serialized across processes via flock on the
// cache lockfile, so two concurrent writers cannot lose each other's entries.
func (c *DiskCache) Set(host, token string, entry Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.withCacheLock(func() error {
		m := c.loadAll()
		m[cacheKey(host, token)] = entry
		return c.saveAll(m)
	})
}

// Invalidate removes the (host, token) entry. Missing entries are not an
// error — callers (the transport, on a 401) shouldn't have to distinguish.
// The R-M-W is serialized across processes via the cache lockfile.
func (c *DiskCache) Invalidate(host, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.withCacheLock(func() error {
		m := c.loadAll()
		key := cacheKey(host, token)
		if _, ok := m[key]; !ok {
			return nil
		}
		delete(m, key)
		return c.saveAll(m)
	})
}

// Preflight returns the abilities for (host, token), using the cache if fresh
// and otherwise calling whoamiFn. On a successful fetch the result is written
// back to the cache.
//
// Cache write errors are non-fatal — we still return the abilities — because
// failing the command because we couldn't optimize the next invocation would
// be a poor tradeoff. (If you need to surface cache write failures, wrap the
// Cache implementation.)
//
// whoamiFn returns (abilities, expiresAt, error). expiresAt may be zero if
// the token has no expiry; in that case the cache entry never expires on its
// own and only Invalidate (e.g. on a 401) will evict it.
//
// A nil cache disables caching: every call hits whoamiFn. This is convenient
// for tests and for explicit `--no-cache` invocations.
func Preflight(
	ctx context.Context,
	host, token string,
	cache Cache,
	whoamiFn func(context.Context) (Set, time.Time, error),
) (Set, error) {
	if cache != nil {
		if e, ok := cache.Get(host, token); ok {
			return e.Abilities, nil
		}
	}

	abilities, expiresAt, err := whoamiFn(ctx)
	if err != nil {
		return nil, err
	}

	if cache != nil {
		entry := Entry{
			Abilities: abilities,
			ExpiresAt: expiresAt,
			CachedAt:  time.Now(),
		}
		_ = cache.Set(host, token, entry)
	}

	return abilities, nil
}

// AbilityMissingError is returned by Refuse when the token lacks a required
// ability. UserMessage is suitable for printing directly to stderr; Error is
// terser and meant for logs.
//
// Lane E owns the broader inventory.errors taxonomy, but this single
// ability-error lives here so abilities.go is self-contained and the rest of
// the codebase can depend on this package without a cycle.
type AbilityMissingError struct {
	Needed Ability
	Have   Set
}

func (e *AbilityMissingError) Error() string {
	return fmt.Sprintf("token missing required ability %q (have %v)", e.Needed, e.Have)
}

// UserMessage is a friendlier rendering aimed at end users / Claude Code. It
// explains the missing ability and points at the most likely remediation.
func (e *AbilityMissingError) UserMessage() string {
	return fmt.Sprintf(
		"This command requires the %q ability, but your token doesn't have it.\n"+
			"Granted abilities: %v\n"+
			"Ask an admin to issue a token with the missing ability, "+
			"or switch profiles with --profile <name>.",
		e.Needed, e.Have,
	)
}

// Refuse returns an *AbilityMissingError if needed is not present in have, and
// nil otherwise. Use it as the gate at the top of any command handler:
//
//	if err := inventory.Refuse(inventory.Write, have); err != nil { return err }
func Refuse(needed Ability, have Set) error {
	if have.Has(needed) {
		return nil
	}
	return &AbilityMissingError{Needed: needed, Have: have}
}

// IsAbilityMissing is a small helper for callers that want to branch on the
// typed error without writing errors.As inline.
func IsAbilityMissing(err error) bool {
	var e *AbilityMissingError
	return errors.As(err, &e)
}
