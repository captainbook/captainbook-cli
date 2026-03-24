package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	configDir  = ".ceebee"
	configFile = "config.yaml"
	filePerm   = 0600
	dirPerm    = 0700
)

// Config represents the full configuration file.
type Config struct {
	DefaultProfile string             `yaml:"default_profile"`
	Profiles       map[string]Profile `yaml:"profiles"`
}

// Profile holds the credentials for a single API target.
type Profile struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// Resolved holds the final URL and token after resolving env vars and profiles.
type Resolved struct {
	URL   string
	Token string
}

// ConfigError is returned for configuration problems.
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("Config error: %s", e.Message)
}

// Resolve returns the API URL and token for the given profile name.
// Resolution order: env vars > named profile > default profile > error.
func Resolve(profileName string) (*Resolved, error) {
	envURL := os.Getenv("CEEBEE_API_URL")
	envToken := os.Getenv("CEEBEE_API_TOKEN")

	// If both env vars are set, use them directly
	if envURL != "" && envToken != "" {
		return &Resolved{URL: envURL, Token: envToken}, nil
	}

	cfg, err := Load()
	if err != nil && envURL == "" && envToken == "" {
		return nil, err
	}

	var profile Profile
	if cfg != nil {
		if profileName == "" {
			profileName = cfg.DefaultProfile
		}
		if profileName == "" {
			// Use the first (or only) profile if no default is set
			for name := range cfg.Profiles {
				profileName = name
				break
			}
		}
		if profileName != "" {
			p, ok := cfg.Profiles[profileName]
			if !ok {
				return nil, &ConfigError{Message: fmt.Sprintf("profile %q not found", profileName)}
			}
			profile = p
		}
	}

	// Env vars override profile values
	resolved := &Resolved{
		URL:   profile.URL,
		Token: profile.Token,
	}
	if envURL != "" {
		resolved.URL = envURL
	}
	if envToken != "" {
		resolved.Token = envToken
	}

	if resolved.URL == "" {
		return nil, &ConfigError{Message: "no API URL configured. Run 'ceebee config add <name> --url <url> --token <token>' or set CEEBEE_API_URL"}
	}
	if resolved.Token == "" {
		return nil, &ConfigError{Message: "no API token configured. Run 'ceebee config add <name> --url <url> --token <token>' or set CEEBEE_API_TOKEN"}
	}

	return resolved, nil
}

// Load reads the config file from ~/.ceebee/config.yaml.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, &ConfigError{Message: err.Error()}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &ConfigError{Message: "no config file found. Run 'ceebee config add <name> --url <url> --token <token>'"}
		}
		return nil, &ConfigError{Message: fmt.Sprintf("reading config: %v", err)}
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, &ConfigError{Message: fmt.Sprintf("parsing config: %v", err)}
	}

	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]Profile)
	}

	return &cfg, nil
}

// AddProfile adds or updates a profile in the config file.
func AddProfile(name, url, token string) error {
	cfg, err := Load()
	if err != nil {
		cfg = &Config{Profiles: make(map[string]Profile)}
	}

	cfg.Profiles[name] = Profile{URL: url, Token: token}

	// Set as default if it's the first profile
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = name
	}

	return save(cfg)
}

// RemoveProfile removes a profile from the config file.
func RemoveProfile(name string) error {
	cfg, err := Load()
	if err != nil {
		return &ConfigError{Message: fmt.Sprintf("cannot remove profile: %v", err)}
	}

	if _, ok := cfg.Profiles[name]; !ok {
		return &ConfigError{Message: fmt.Sprintf("profile %q not found", name)}
	}

	delete(cfg.Profiles, name)

	if cfg.DefaultProfile == name {
		cfg.DefaultProfile = ""
		for n := range cfg.Profiles {
			cfg.DefaultProfile = n
			break
		}
	}

	return save(cfg)
}

// SetDefault sets the default profile.
func SetDefault(name string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	if _, ok := cfg.Profiles[name]; !ok {
		return &ConfigError{Message: fmt.Sprintf("profile %q not found", name)}
	}

	cfg.DefaultProfile = name
	return save(cfg)
}

// ListProfiles returns the config (for listing profiles).
func ListProfiles() (*Config, error) {
	return Load()
}

func save(cfg *Config) error {
	dir, err := configDir_()
	if err != nil {
		return &ConfigError{Message: err.Error()}
	}

	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return &ConfigError{Message: fmt.Sprintf("creating config directory: %v", err)}
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return &ConfigError{Message: fmt.Sprintf("marshaling config: %v", err)}
	}

	path := filepath.Join(dir, configFile)
	if err := os.WriteFile(path, data, filePerm); err != nil {
		return &ConfigError{Message: fmt.Sprintf("writing config: %v", err)}
	}

	return nil
}

func configDir_() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	return filepath.Join(home, configDir), nil
}

func configPath() (string, error) {
	dir, err := configDir_()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFile), nil
}
