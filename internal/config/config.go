package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	Permission string `json:"permission"`

	DefaultProfile string             `json:"default_profile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`
	Routing        RoutingConfig      `json:"routing,omitzero"`
}

func Path() (string, error) {
	d, e := os.UserConfigDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(d, "kero", "config.json"), nil
}
func Load() (Config, error) {
	var c Config
	p, e := Path()
	if e != nil {
		return c, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if e = c.Validate(); e != nil {
		return c, e
	}
	c.applyDefaultProfile()
	return c, nil
}
func (c Config) Validate() error {
	defaultName := c.defaultProfileName()
	if _, e := c.SelectProfile(defaultName); e != nil {
		return e
	}
	for _, name := range c.ProfileNames() {
		if name == "" {
			return errors.New("profile names must be nonempty and have no surrounding whitespace")
		}
		if name == defaultName {
			continue
		}
		if _, e := c.SelectProfile(name); e != nil {
			return e
		}
	}
	if e := c.validateRouting(); e != nil {
		return e
	}
	switch c.Permission {
	case "ask", "workspace", "auto":
		return nil
	}
	return errors.New("permission must be ask, workspace, or auto")
}
func Save(c Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	c.applyDefaultProfile()
	p, e := Path()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(p, append(b, '\n'), 0600)
}
func Configure(in *bufio.Reader, old Config) (Config, error) {
	c := old
	name := c.defaultProfileName()
	profile, named := c.Profiles[name]
	if named {
		c.BaseURL, c.Model, c.APIKeyEnv = profile.BaseURL, profile.Model, profile.APIKeyEnv
	}
	ask := func(label, value string) (string, error) {
		fmt.Printf("%s [%s]: ", label, value)
		s, e := in.ReadString('\n')
		if e != nil {
			return "", e
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return value, nil
		}
		return s, nil
	}
	var e error
	if c.BaseURL, e = ask("OpenAI compatible base URL", c.BaseURL); e != nil {
		return c, e
	}
	if c.Model, e = ask("Model ID", c.Model); e != nil {
		return c, e
	}
	fmt.Println("API key: enter an environment variable NAME (for example OPENAI_API_KEY), not the key value. Leave empty for an unauthenticated local gateway; enter - to clear the current setting.")
	if c.APIKeyEnv, e = ask("API key environment variable name (optional; - clears)", c.APIKeyEnv); e != nil {
		return c, e
	}
	if c.APIKeyEnv == "-" {
		c.APIKeyEnv = ""
	}
	if c.Permission == "" {
		c.Permission = "ask"
	}
	fmt.Println("Permissions: ask = confirm edits and commands; workspace = allow workspace edits, confirm commands; auto = allow edits and commands (commands can access anything).")
	if c.Permission, e = ask("Permission mode", c.Permission); e != nil {
		return c, e
	}
	if named {
		c.Profiles = maps.Clone(c.Profiles)
		profile.BaseURL, profile.Model, profile.APIKeyEnv = c.BaseURL, c.Model, c.APIKeyEnv
		c.Profiles[name] = profile
		c.DefaultProfile = name
	}
	if e = Save(c); e != nil {
		return c, e
	}
	return c, nil
}
