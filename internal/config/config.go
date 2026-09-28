// Package config stores mcpcli profiles: one directory per profile under
// ~/.mcpcli (or $MCPCLI_HOME) holding config.yaml, token.json and a cached
// tool list.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Auth modes.
const (
	AuthOAuth  = "oauth"
	AuthBearer = "bearer"
	AuthNone   = "none"
)

// Profile is one remote MCP server plus the identity used to reach it.
// The same URL under two profile names is two accounts.
type Profile struct {
	Name      string            `yaml:"-"`
	URL       string            `yaml:"url"`
	Auth      string            `yaml:"auth"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	BearerEnv string            `yaml:"bearer_env,omitempty"`
	Bearer    string            `yaml:"bearer,omitempty"`
	OAuth     *OAuthState       `yaml:"oauth,omitempty"`
}

// OAuthState is what login discovered and registered; it is enough to
// refresh tokens and to log in again without re-discovery.
type OAuthState struct {
	Issuer          string   `yaml:"issuer"`
	AuthURL         string   `yaml:"auth_url"`
	TokenURL        string   `yaml:"token_url"`
	DeviceAuthURL   string   `yaml:"device_auth_url,omitempty"`
	RegistrationURL string   `yaml:"registration_url,omitempty"`
	ClientID        string   `yaml:"client_id"`
	ClientSecret    string   `yaml:"client_secret,omitempty"`
	RedirectURL     string   `yaml:"redirect_url,omitempty"`
	Scopes          []string `yaml:"scopes,omitempty"`
	Resource        string   `yaml:"resource,omitempty"`
}

// Token is the persisted OAuth token.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

// Valid reports whether the access token is usable for at least buffer.
func (t *Token) Valid(buffer time.Duration) bool {
	if t == nil || t.AccessToken == "" {
		return false
	}
	if t.Expiry.IsZero() {
		return true
	}
	return time.Now().Add(buffer).Before(t.Expiry)
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ValidName rejects names that would escape the profile directory or clash
// with commands.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: use letters, digits, '.', '_' or '-'", name)
	}
	return nil
}

// Home is the profiles root directory.
func Home() (string, error) {
	if h := os.Getenv("MCPCLI_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mcpcli"), nil
}

// Dir is a profile's directory.
func Dir(name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	h, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, name), nil
}

// ErrNotFound is returned when a profile does not exist.
var ErrNotFound = errors.New("profile not found")

// Exists reports whether a profile directory with a config exists.
func Exists(name string) bool {
	d, err := Dir(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(d, "config.yaml"))
	return err == nil
}

// Load reads a profile.
func Load(name string) (*Profile, error) {
	d, err := Dir(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(d, "config.yaml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %q (run `mcpcli add %s <url>` or `mcpcli profiles`)", ErrNotFound, name, name)
		}
		return nil, err
	}
	var p Profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(d, "config.yaml"), err)
	}
	p.Name = name
	if p.Auth == "" {
		p.Auth = AuthOAuth
	}
	return &p, nil
}

// Save writes a profile (mode 0600: it may hold a bearer or client secret).
func Save(p *Profile) (string, error) {
	d, err := Dir(p.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	b, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}
	path := filepath.Join(d, "config.yaml")
	return path, os.WriteFile(path, b, 0o600)
}

// Remove deletes a profile directory with everything in it.
func Remove(name string) error {
	d, err := Dir(name)
	if err != nil {
		return err
	}
	if !Exists(name) {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return os.RemoveAll(d)
}

// List returns profile names, sorted.
func List() ([]string, error) {
	h, err := Home()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(h)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && Exists(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func tokenPath(name string) (string, error) {
	d, err := Dir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "token.json"), nil
}

// LoadToken reads the saved token; a missing file yields nil, nil.
func LoadToken(name string) (*Token, error) {
	p, err := tokenPath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &t, nil
}

// SaveToken writes the token at mode 0600.
func SaveToken(name string, t *Token) error {
	p, err := tokenPath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// DeleteToken removes the token file if present.
func DeleteToken(name string) error {
	p, err := tokenPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ToolCache is the last tool list seen for a profile, used to coerce k=v
// arguments and to resolve tool names without a round trip.
type ToolCache struct {
	FetchedAt time.Time         `json:"fetched_at"`
	Tools     []json.RawMessage `json:"tools"`
}

func toolsPath(name string) (string, error) {
	d, err := Dir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "tools.json"), nil
}

// LoadTools reads the cached tool list; missing yields nil, nil.
func LoadTools(name string) (*ToolCache, error) {
	p, err := toolsPath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var c ToolCache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// SaveTools writes the tool cache.
func SaveTools(name string, c *ToolCache) error {
	p, err := toolsPath(name)
	if err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// ParseHeader splits "Name: value" or "Name=value".
func ParseHeader(s string) (string, string, error) {
	for _, sep := range []string{":", "="} {
		if i := strings.Index(s, sep); i > 0 {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), nil
		}
	}
	return "", "", fmt.Errorf("header %q must be Name:value or Name=value", s)
}
