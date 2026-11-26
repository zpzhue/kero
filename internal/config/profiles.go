package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
)

const legacyProfile = "default"

// Profile describes a model endpoint. APIKeyEnv names the environment variable
// that holds the key. A zero ContextTokens leaves the limit unspecified.
type Profile struct {
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	APIKeyEnv     string `json:"api_key_env,omitempty"`
	ContextTokens int    `json:"context_tokens,omitempty"`
}

// ProfileNames lists selectable profile names in lexical order.
func (c Config) ProfileNames() []string {
	names := slices.Sorted(maps.Keys(c.Profiles))
	if c.hasLegacyProfile() {
		index, exists := slices.BinarySearch(names, legacyProfile)
		if !exists {
			names = slices.Insert(names, index, legacyProfile)
		}
	}
	return names
}

// SelectProfile returns the named profile. An empty name selects the default.
func (c Config) SelectProfile(name string) (Profile, error) {
	if name == "" {
		name = c.defaultProfileName()
	}
	if name == "" || name != strings.TrimSpace(name) {
		return Profile{}, errors.New("profile names must be nonempty and have no surrounding whitespace")
	}
	p, ok := c.Profiles[name]
	if !ok && name == legacyProfile && c.hasLegacyProfile() {
		p = Profile{BaseURL: c.BaseURL, Model: c.Model, APIKeyEnv: c.APIKeyEnv}
		ok = true
	}
	if !ok {
		return Profile{}, fmt.Errorf("unknown profile %q", name)
	}
	if err := p.validate(); err != nil {
		return Profile{}, fmt.Errorf("profile %q: %w", name, err)
	}
	return p, nil
}

func (c Config) defaultProfileName() string {
	if c.DefaultProfile != "" {
		return c.DefaultProfile
	}
	if len(c.Profiles) == 1 && !c.hasLegacyProfile() {
		for name := range c.Profiles {
			return name
		}
	}
	return legacyProfile
}

func (c Config) hasLegacyProfile() bool {
	return len(c.Profiles) == 0 || ((c.DefaultProfile == "" || c.DefaultProfile == legacyProfile) &&
		(c.BaseURL != "" || c.Model != "" || c.APIKeyEnv != ""))
}

func (c *Config) applyDefaultProfile() {
	name := c.defaultProfileName()
	if p, ok := c.Profiles[name]; ok {
		c.DefaultProfile = name
		c.BaseURL, c.Model, c.APIKeyEnv = p.BaseURL, p.Model, p.APIKeyEnv
	}
}

func (p Profile) validate() error {
	if strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.Model) == "" {
		return errors.New("base_url and model are required")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.ForceQuery || strings.ContainsAny(p.BaseURL, "?#") || p.BaseURL != strings.TrimSpace(p.BaseURL) {
		return errors.New("base_url must be an absolute HTTP or HTTPS URL without credentials, a query, or a fragment")
	}
	if p.ContextTokens < 0 {
		return errors.New("context_tokens must be nonnegative")
	}
	if strings.ContainsAny(p.APIKeyEnv, "=\x00") {
		return errors.New("api_key_env must be an environment variable name")
	}
	return nil
}
