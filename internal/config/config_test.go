package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setTestHome sets HOME to a temp directory and returns a cleanup function.
// This causes configDir_() to return <tmpDir>/.ceebee.
func setTestHome(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	return tmpDir
}

func TestLoad_NoConfigFile(t *testing.T) {
	setTestHome(t)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when no config file exists")
	}
	cfgErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("error type = %T, want *ConfigError", err)
	}
	if !strings.Contains(cfgErr.Error(), "no config file") {
		t.Errorf("error = %q, want substring 'no config file'", cfgErr.Error())
	}
}

func TestAddProfile_CreatesConfigFile(t *testing.T) {
	home := setTestHome(t)

	err := AddProfile("prod", "https://api.example.com", "tok-prod")
	if err != nil {
		t.Fatalf("AddProfile() error: %v", err)
	}

	// Verify file was created
	path := filepath.Join(home, ".ceebee", "config.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("config file not created at %s", path)
	}

	// Verify we can load it back
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Profiles) != 1 {
		t.Fatalf("profiles count = %d, want 1", len(cfg.Profiles))
	}
	p, ok := cfg.Profiles["prod"]
	if !ok {
		t.Fatal("profile 'prod' not found")
	}
	if p.URL != "https://api.example.com" {
		t.Errorf("URL = %q, want %q", p.URL, "https://api.example.com")
	}
	if p.Token != "tok-prod" {
		t.Errorf("Token = %q, want %q", p.Token, "tok-prod")
	}
}

func TestAddProfile_SetsDefaultOnFirst(t *testing.T) {
	setTestHome(t)

	err := AddProfile("first", "https://first.example.com", "tok1")
	if err != nil {
		t.Fatalf("AddProfile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DefaultProfile != "first" {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, "first")
	}
}

func TestAddProfile_DoesNotOverrideDefault(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("first", "https://first.example.com", "tok1")
	_ = AddProfile("second", "https://second.example.com", "tok2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DefaultProfile != "first" {
		t.Errorf("DefaultProfile = %q, want %q (should remain 'first')", cfg.DefaultProfile, "first")
	}
	if len(cfg.Profiles) != 2 {
		t.Errorf("profiles count = %d, want 2", len(cfg.Profiles))
	}
}

func TestAddProfile_UpdatesExisting(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://old.example.com", "old-tok")
	_ = AddProfile("prod", "https://new.example.com", "new-tok")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Profiles) != 1 {
		t.Fatalf("profiles count = %d, want 1", len(cfg.Profiles))
	}
	p := cfg.Profiles["prod"]
	if p.URL != "https://new.example.com" {
		t.Errorf("URL = %q, want %q", p.URL, "https://new.example.com")
	}
	if p.Token != "new-tok" {
		t.Errorf("Token = %q, want %q", p.Token, "new-tok")
	}
}

func TestRemoveProfile(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")
	_ = AddProfile("staging", "https://staging.example.com", "tok-staging")

	err := RemoveProfile("staging")
	if err != nil {
		t.Fatalf("RemoveProfile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Profiles) != 1 {
		t.Fatalf("profiles count = %d, want 1", len(cfg.Profiles))
	}
	if _, ok := cfg.Profiles["staging"]; ok {
		t.Error("profile 'staging' should have been removed")
	}
}

func TestRemoveProfile_UpdatesDefault(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("alpha", "https://alpha.example.com", "tok-a")
	_ = AddProfile("beta", "https://beta.example.com", "tok-b")
	_ = SetDefault("alpha")

	err := RemoveProfile("alpha")
	if err != nil {
		t.Fatalf("RemoveProfile() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// Default should have been reassigned since alpha was the default
	if cfg.DefaultProfile == "alpha" {
		t.Error("DefaultProfile should no longer be 'alpha'")
	}
	// With only 'beta' left, it should be the new default
	if cfg.DefaultProfile != "beta" {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, "beta")
	}
}

func TestRemoveProfile_NotFound(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok")

	err := RemoveProfile("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent profile")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want substring 'not found'", err.Error())
	}
}

func TestRemoveProfile_NoConfigFile(t *testing.T) {
	setTestHome(t)

	err := RemoveProfile("anything")
	if err == nil {
		t.Fatal("expected error when no config file exists")
	}
}

func TestSetDefault(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")
	_ = AddProfile("staging", "https://staging.example.com", "tok-staging")

	err := SetDefault("staging")
	if err != nil {
		t.Fatalf("SetDefault() error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DefaultProfile != "staging" {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, "staging")
	}
}

func TestSetDefault_NotFound(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok")

	err := SetDefault("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent profile")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want substring 'not found'", err.Error())
	}
}

func TestSetDefault_NoConfigFile(t *testing.T) {
	setTestHome(t)

	err := SetDefault("anything")
	if err == nil {
		t.Fatal("expected error when no config file exists")
	}
}

func TestResolve_EnvVarsOverride(t *testing.T) {
	setTestHome(t)

	t.Setenv("CEEBEE_API_URL", "https://env.example.com")
	t.Setenv("CEEBEE_API_TOKEN", "env-tok")

	resolved, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if resolved.URL != "https://env.example.com" {
		t.Errorf("URL = %q, want %q", resolved.URL, "https://env.example.com")
	}
	if resolved.Token != "env-tok" {
		t.Errorf("Token = %q, want %q", resolved.Token, "env-tok")
	}
}

func TestResolve_EnvVarsBothRequired(t *testing.T) {
	setTestHome(t)

	// Only URL set, no token, no config file -> should fail
	t.Setenv("CEEBEE_API_URL", "https://env.example.com")
	t.Setenv("CEEBEE_API_TOKEN", "")

	_, err := Resolve("")
	if err == nil {
		t.Fatal("expected error when only URL env var is set and no config")
	}
}

func TestResolve_ProfileFallback(t *testing.T) {
	setTestHome(t)

	// Clear env vars
	t.Setenv("CEEBEE_API_URL", "")
	t.Setenv("CEEBEE_API_TOKEN", "")

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")

	resolved, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if resolved.URL != "https://prod.example.com" {
		t.Errorf("URL = %q, want %q", resolved.URL, "https://prod.example.com")
	}
	if resolved.Token != "tok-prod" {
		t.Errorf("Token = %q, want %q", resolved.Token, "tok-prod")
	}
}

func TestResolve_NamedProfile(t *testing.T) {
	setTestHome(t)

	t.Setenv("CEEBEE_API_URL", "")
	t.Setenv("CEEBEE_API_TOKEN", "")

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")
	_ = AddProfile("staging", "https://staging.example.com", "tok-staging")

	resolved, err := Resolve("staging")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if resolved.URL != "https://staging.example.com" {
		t.Errorf("URL = %q, want %q", resolved.URL, "https://staging.example.com")
	}
	if resolved.Token != "tok-staging" {
		t.Errorf("Token = %q, want %q", resolved.Token, "tok-staging")
	}
}

func TestResolve_NamedProfileNotFound(t *testing.T) {
	setTestHome(t)

	t.Setenv("CEEBEE_API_URL", "")
	t.Setenv("CEEBEE_API_TOKEN", "")

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")

	_, err := Resolve("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent profile")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want substring 'not found'", err.Error())
	}
}

func TestResolve_ExplicitProfileIgnoresEnv(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")

	// Env vars set to something else entirely — explicit --profile must win.
	t.Setenv("CEEBEE_API_URL", "https://env-override.example.com")
	t.Setenv("CEEBEE_API_TOKEN", "env-tok")

	resolved, err := Resolve("prod")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if resolved.URL != "https://prod.example.com" {
		t.Errorf("URL = %q, want profile URL (explicit --profile must win over env)", resolved.URL)
	}
	if resolved.Token != "tok-prod" {
		t.Errorf("Token = %q, want profile token %q", resolved.Token, "tok-prod")
	}
	if resolved.Source != "profile:prod" {
		t.Errorf("Source = %q, want %q", resolved.Source, "profile:prod")
	}
}

func TestResolve_EnvPartialOverridesDefaultProfile(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")

	// Only env token set, no explicit --profile → token from env, URL from default profile.
	t.Setenv("CEEBEE_API_URL", "")
	t.Setenv("CEEBEE_API_TOKEN", "env-tok")

	resolved, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if resolved.URL != "https://prod.example.com" {
		t.Errorf("URL = %q, want profile URL", resolved.URL)
	}
	if resolved.Token != "env-tok" {
		t.Errorf("Token = %q, want env override %q", resolved.Token, "env-tok")
	}
	if resolved.Source != "env+profile:prod" {
		t.Errorf("Source = %q, want %q", resolved.Source, "env+profile:prod")
	}
}

func TestResolve_Source(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")

	tests := []struct {
		name       string
		profile    string
		envURL     string
		envToken   string
		wantSource string
	}{
		{"explicit profile", "prod", "https://env.example.com", "env-tok", "profile:prod"},
		{"default profile, no env", "", "", "", "profile:prod"},
		{"both env, no explicit profile", "", "https://env.example.com", "env-tok", "env"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CEEBEE_API_URL", tc.envURL)
			t.Setenv("CEEBEE_API_TOKEN", tc.envToken)

			resolved, err := Resolve(tc.profile)
			if err != nil {
				t.Fatalf("Resolve() error: %v", err)
			}
			if resolved.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", resolved.Source, tc.wantSource)
			}
		})
	}
}

func TestResolve_NoConfigNoEnv(t *testing.T) {
	setTestHome(t)

	t.Setenv("CEEBEE_API_URL", "")
	t.Setenv("CEEBEE_API_TOKEN", "")

	_, err := Resolve("")
	if err == nil {
		t.Fatal("expected error when no config and no env vars")
	}
}

func TestListProfiles(t *testing.T) {
	setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok-prod")
	_ = AddProfile("staging", "https://staging.example.com", "tok-staging")

	cfg, err := ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles() error: %v", err)
	}
	if len(cfg.Profiles) != 2 {
		t.Errorf("profiles count = %d, want 2", len(cfg.Profiles))
	}
}

func TestConfigError_Error(t *testing.T) {
	err := &ConfigError{Message: "test message"}
	got := err.Error()
	if !strings.Contains(got, "Config error") {
		t.Errorf("Error() = %q, want prefix 'Config error'", got)
	}
	if !strings.Contains(got, "test message") {
		t.Errorf("Error() = %q, want substring 'test message'", got)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	home := setTestHome(t)

	// Create a config file with invalid YAML
	dir := filepath.Join(home, ".ceebee")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("\t\t\tinvalid:\n  - broken\n    yaml"), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parsing config") {
		t.Errorf("error = %q, want substring 'parsing config'", err.Error())
	}
}

func TestLoad_EmptyProfiles(t *testing.T) {
	home := setTestHome(t)

	// Create a config file with no profiles
	dir := filepath.Join(home, ".ceebee")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	content := "default_profile: \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Profiles == nil {
		t.Error("Profiles should be initialized (not nil)")
	}
	if len(cfg.Profiles) != 0 {
		t.Errorf("profiles count = %d, want 0", len(cfg.Profiles))
	}
}

func TestConfigFilePermissions(t *testing.T) {
	home := setTestHome(t)

	_ = AddProfile("prod", "https://prod.example.com", "tok")

	path := filepath.Join(home, ".ceebee", "config.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}

	// File should be created with 0600 permissions
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}
