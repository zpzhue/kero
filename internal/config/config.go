package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	Permission string `json:"permission"`
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
	return c, c.Validate()
}
func (c Config) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.Model) == "" {
		return errors.New("base_url and model are required")
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
	if c.APIKeyEnv, e = ask("API key environment variable (optional)", c.APIKeyEnv); e != nil {
		return c, e
	}
	if c.Permission == "" {
		c.Permission = "ask"
	}
	fmt.Println("Permissions: ask = confirm edits and commands; workspace = allow workspace edits, confirm commands; auto = allow edits and commands (commands can access anything).")
	if c.Permission, e = ask("Permission mode", c.Permission); e != nil {
		return c, e
	}
	if e = c.Validate(); e != nil {
		return c, e
	}
	return c, Save(c)
}
