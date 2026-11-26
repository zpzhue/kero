package config

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLoadLegacyConfig(t *testing.T) {
	const data = `{"base_url":"http://localhost:8000/v1","model":"local","api_key_env":"KERO_TEST_KEY","permission":"workspace"}`
	path := writeTestConfig(t, data)
	t.Setenv("KERO_TEST_KEY", "test-secret-value")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ProfileNames(); !reflect.DeepEqual(got, []string{"default"}) {
		t.Fatalf("ProfileNames() = %v", got)
	}
	p, err := c.SelectProfile("")
	want := Profile{BaseURL: "http://localhost:8000/v1", Model: "local", APIKeyEnv: "KERO_TEST_KEY"}
	if err != nil || p != want {
		t.Fatalf("SelectProfile() = %+v, %v", p, err)
	}
	if c.BaseURL != want.BaseURL || c.Model != want.Model || c.APIKeyEnv != want.APIKeyEnv || c.Permission != "workspace" {
		t.Fatal("Load did not preserve legacy fields")
	}
	bytes, err := os.ReadFile(path)
	if err != nil || string(bytes) != data {
		t.Fatalf("Load changed legacy config: %v", err)
	}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	bytes, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), "test-secret-value") {
		t.Fatal("Save wrote the value of the API key")
	}
}

func TestConfigureEditsNamedDefault(t *testing.T) {
	writeTestConfig(t, `{
		"default_profile": "work", "permission": "ask",
		"profiles": {
			"work": {"base_url":"http://localhost:8000/v1","model":"before","context_tokens":32000},
			"fast": {"base_url":"https://example.com/v1","model":"quick","api_key_env":"FAST_KEY"}
		},
		"routing": {"enabled":true,"fast_profile":"fast"}
	}`)
	old, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if old.Model != "before" || old.BaseURL != "http://localhost:8000/v1" {
		t.Fatal("Load did not project the named default into the legacy fields")
	}
	if got := old.ProfileNames(); !reflect.DeepEqual(got, []string{"fast", "work"}) {
		t.Fatalf("ProfileNames() = %v", got)
	}
	updated, err := Configure(bufio.NewReader(strings.NewReader("\nafter\n\nworkspace\n")), old)
	if err != nil {
		t.Fatal(err)
	}
	if old.Profiles["work"].Model != "before" {
		t.Fatal("Configure mutated the input profile map")
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, updated) {
		t.Fatal("Configure returned a different config from what it saved")
	}
	p, err := loaded.SelectProfile("")
	if err != nil || p.Model != "after" || p.ContextTokens != 32000 || loaded.Model != p.Model || loaded.Permission != "workspace" {
		t.Fatalf("configured profile = %+v, %v", p, err)
	}
	if loaded.Profiles["fast"] != old.Profiles["fast"] || loaded.Routing != old.Routing {
		t.Fatal("Configure changed an unrelated profile or routing")
	}
}

func TestConfigureClearsAPIKeyEnvironment(t *testing.T) {
	writeTestConfig(t, `{"base_url":"http://localhost:3425/v1","model":"example","api_key_env":"missing","permission":"ask"}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Configure(bufio.NewReader(strings.NewReader("\n\n-\n\n")), c); err != nil {
		t.Fatal(err)
	}
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKeyEnv != "" {
		t.Fatalf("API key environment variable was not cleared: %q", c.APIKeyEnv)
	}
}

func TestConfigureRetriesInvalidFields(t *testing.T) {
	writeTestConfig(t, `{"base_url":"http://localhost:8000/v1","model":"old","permission":"ask"}`)
	old, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	input := "bad-url\nhttps://example.com/v1\nnew-model\nBAD=KEY\nGOOD_KEY\ninvalid\nworkspace\n"
	got, err := Configure(bufio.NewReader(strings.NewReader(input)), old)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "https://example.com/v1" || got.Model != "new-model" || got.APIKeyEnv != "GOOD_KEY" || got.Permission != "workspace" {
		t.Fatalf("unexpected configuration: %+v", got)
	}
	loaded, err := Load()
	if err != nil || !reflect.DeepEqual(got, loaded) {
		t.Fatalf("saved configuration = %+v, %v", loaded, err)
	}
}

func TestConfigureCancellationDoesNotSave(t *testing.T) {
	data := `{"base_url":"http://localhost:8000/v1","model":"old","permission":"ask"}`
	path := writeTestConfig(t, data)
	old, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Configure(bufio.NewReader(strings.NewReader("https://example.com/v1\nnew-model\n")), old)
	if err == nil || !reflect.DeepEqual(got, old) {
		t.Fatalf("cancellation = %+v, %v", got, err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != data {
		t.Fatalf("config changed on cancellation: %q, %v", b, err)
	}
}

func TestConfigureMenuProfilesAndDefault(t *testing.T) {
	writeTestConfig(t, `{"default_profile":"work","permission":"ask","profiles":{"work":{"base_url":"https://work.example/v1","model":"work","context_tokens":32000},"fast":{"base_url":"https://fast.example/v1","model":"fast"}},"routing":{"enabled":true,"fast_profile":"fast"}}`)
	old, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	input := "1\nnew\nhttps://new.example/v1\nnew-model\nNEW_KEY\n-1\n64000\n2\nmissing\nnew\n3\nauto\n4\n"
	got, err := ConfigureMenu(bufio.NewReader(strings.NewReader(input)), old)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultProfile != "new" || got.Model != "new-model" || got.Profiles["new"].ContextTokens != 64000 || got.Permission != "auto" {
		t.Fatalf("unexpected configuration: %+v", got)
	}
	if got.Profiles["work"] != old.Profiles["work"] || got.Profiles["fast"] != old.Profiles["fast"] || got.Routing != old.Routing || len(old.Profiles) != 2 {
		t.Fatal("menu modified existing profiles, routing, or input")
	}
	loaded, err := Load()
	if err != nil || !reflect.DeepEqual(got, loaded) {
		t.Fatalf("saved configuration = %+v, %v", loaded, err)
	}
}

func TestConfigureMenuCancellationDoesNotSave(t *testing.T) {
	data := `{"base_url":"http://localhost:8000/v1","model":"old","permission":"ask"}`
	for _, input := range []string{"1\nnew\nhttps://new.example/v1\nmodel\n\n0\n0\n", "3\nauto\n"} {
		path := writeTestConfig(t, data)
		old, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		got, err := ConfigureMenu(bufio.NewReader(strings.NewReader(input)), old)
		if err == nil || !reflect.DeepEqual(got, old) {
			t.Fatalf("cancellation = %+v, %v", got, err)
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != data {
			t.Fatalf("config changed on cancellation: %q, %v", b, err)
		}
	}
}

func TestLoadImplicitDefault(t *testing.T) {
	for _, tt := range []struct {
		name, data, profile, model string
	}{
		{"one named profile", `{"permission":"ask","profiles":{"work":{"base_url":"https://example.com","model":"work"}}}`, "work", "work"},
		{"literal default profile", `{"permission":"ask","profiles":{"work":{"base_url":"https://example.com","model":"work"},"default":{"base_url":"https://example.com","model":"primary"}}}`, "default", "primary"},
		{"legacy with a named addition", `{"permission":"ask","base_url":"https://example.com","model":"legacy","profiles":{"work":{"base_url":"https://example.com","model":"work"}}}`, "default", "legacy"},
		{"explicit legacy default", `{"default_profile":"default","permission":"ask","base_url":"https://example.com","model":"legacy","profiles":{"work":{"base_url":"https://example.com","model":"work"}}}`, "default", "legacy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writeTestConfig(t, tt.data)
			c, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			d, err := c.RouteProfile("", "")
			if err != nil || d.Name != tt.profile || d.Profile.Model != tt.model || c.Model != tt.model {
				t.Fatalf("defaults = %+v, %v", d, err)
			}
		})
	}
}

func TestValidateProfiles(t *testing.T) {
	for _, baseURL := range []string{
		"localhost:8000/v1", "file:///v1", "http:///v1", "https://user:do-not-echo@example.com/v1",
		"https://example.com/?password=do-not-echo", "https://example.com/#", "https://example.com/%do-not-echo",
	} {
		c := Config{BaseURL: baseURL, Model: "local", Permission: "ask"}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "base_url") || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal("Validate should report an invalid base URL without including its value")
		}
	}
	for _, tt := range []struct {
		name, defaultProfile, profileName string
		contextTokens                     int
		routing                           RoutingConfig
		wantError                         string
	}{
		{name: "unknown default", defaultProfile: "missing", profileName: "work", wantError: "unknown profile"},
		{name: "empty profile name", defaultProfile: "default", profileName: "", wantError: "profile names"},
		{name: "whitespace in name", defaultProfile: "default", profileName: " work ", wantError: "profile names"},
		{name: "negative context", defaultProfile: "default", profileName: "work", contextTokens: -1, wantError: "context_tokens"},
		{name: "unknown route", defaultProfile: "default", profileName: "work", routing: RoutingConfig{Enabled: true, FastProfile: "missing"}, wantError: "routing.fast_profile"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{
				BaseURL: "https://example.com", Model: "local", Permission: "ask", DefaultProfile: tt.defaultProfile,
				Profiles: map[string]Profile{tt.profileName: {BaseURL: "https://example.com", Model: "work", ContextTokens: tt.contextTokens}},
				Routing:  tt.routing,
			}
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func writeTestConfig(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", dir)
	case "darwin", "ios":
		t.Setenv("HOME", dir)
	default:
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
