package config

import (
	"strings"
	"testing"
)

func TestRouteProfile(t *testing.T) {
	c := Config{
		Permission: "ask", DefaultProfile: "work",
		Profiles: map[string]Profile{
			"work": {BaseURL: "https://example.com", Model: "general"},
			"fast": {BaseURL: "https://example.com", Model: "quick", ContextTokens: 8000},
			"deep": {BaseURL: "https://example.com", Model: "deep", ContextTokens: 64000},
		},
		Routing: RoutingConfig{Enabled: true, FastProfile: "fast", ComplexProfile: "deep"},
	}
	for _, tt := range []struct {
		name, task, manual, want, reason string
	}{
		{"manual", "Plan a database migration", "fast", "fast", "manual"},
		{"fast", "FIX A TYPO in README.md.", "", "fast", "fix a typo"},
		{"complex", "Plan a database migration", "", "deep", "migration"},
		{"chinese complex", "设计跨模块架构", "", "deep", "跨模块"},
		{"compound task", "Fix a typo across multiple files", "", "deep", "multiple files"},
		{"multiple steps", "List files then inspect the largest one", "", "work", "no clear routing match"},
		{"whole words", "What is the purpose of migrations.go?", "", "work", "no clear routing match"},
		{"ambiguous", "Look into this", "", "work", "no clear routing match"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := c.RouteProfile(tt.task, tt.manual)
			if err != nil || d.Name != tt.want || d.Profile != c.Profiles[tt.want] || !strings.Contains(d.Reason, tt.reason) {
				t.Fatalf("RouteProfile() = %+v, %v", d, err)
			}
		})
	}
	if _, err := c.RouteProfile("Fix a typo", "unknown"); err == nil {
		t.Fatal("unknown manual profile should return an error")
	}

	c.Routing.Enabled = false
	d, err := c.RouteProfile("Fix a typo", "")
	if err != nil || d.Name != "work" || !strings.Contains(d.Reason, "disabled") {
		t.Fatalf("disabled routing = %+v, %v", d, err)
	}
	d, err = c.RouteProfile("Fix a typo", "deep")
	if err != nil || d.Name != "deep" || !strings.Contains(d.Reason, "manual") {
		t.Fatalf("manual override with routing disabled = %+v, %v", d, err)
	}

	c.Routing = RoutingConfig{Enabled: true}
	d, err = c.RouteProfile("Plan a database migration", "")
	if err != nil || d.Name != "work" || !strings.Contains(d.Reason, "no complex profile") {
		t.Fatalf("missing complex target = %+v, %v", d, err)
	}
}
