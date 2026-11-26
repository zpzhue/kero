package config

import (
	"fmt"
	"strings"
	"unicode"
)

// RoutingConfig enables task-based profile selection and names its optional targets.
type RoutingConfig struct {
	Enabled        bool   `json:"enabled"`
	FastProfile    string `json:"fast_profile,omitempty"`
	ComplexProfile string `json:"complex_profile,omitempty"`
}

// RouteDecision identifies the selected profile and explains the selection.
type RouteDecision struct {
	Name    string
	Profile Profile
	Reason  string
}

// RouteProfile selects a profile for task. An explicit manualProfile takes
// precedence over automatic routing. Unclassified tasks use the default.
func (c Config) RouteProfile(task, manualProfile string) (RouteDecision, error) {
	if manualProfile != "" {
		return c.routeDecision(manualProfile, "manual profile selection")
	}
	name := c.defaultProfileName()
	if !c.Routing.Enabled {
		return c.routeDecision(name, "automatic routing is disabled")
	}
	for _, phrase := range []string{"架构设计", "跨文件", "跨模块", "并发", "死锁", "竞态", "数据库迁移", "重构"} {
		if strings.Contains(task, phrase) {
			if c.Routing.ComplexProfile == "" {
				return c.routeDecision(name, "task mentions "+phrase+" and no complex profile is configured")
			}
			return c.routeDecision(c.Routing.ComplexProfile, "task mentions "+phrase)
		}
	}

	words := strings.FieldsFunc(strings.ToLower(task), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	text := " " + strings.Join(words, " ") + " "
	for _, phrase := range []string{
		"architecture", "migration", "refactor", "refactoring", "concurrency", "race condition", "deadlock",
		"multi file", "across files", "across packages", "multiple files", "multiple packages",
	} {
		if strings.Contains(text, " "+phrase+" ") {
			if c.Routing.ComplexProfile == "" {
				return c.routeDecision(name, "task mentions "+phrase+" and no complex profile is configured")
			}
			return c.routeDecision(c.Routing.ComplexProfile, "task mentions "+phrase)
		}
	}
	if len(words) <= 24 && !strings.ContainsAny(task, "\r\n;+") &&
		!strings.Contains(text, " and ") && !strings.Contains(text, " then ") && !strings.Contains(text, " also ") {
		for _, phrase := range []string{
			"fix typo", "fix a typo", "fix the typo", "correct a typo", "correct the typo",
			"fix spelling", "list files", "show current directory",
		} {
			if strings.HasPrefix(text, " "+phrase+" ") {
				if c.Routing.FastProfile == "" {
					return c.routeDecision(name, "simple request and no fast profile is configured")
				}
				return c.routeDecision(c.Routing.FastProfile, "simple request starts with "+phrase)
			}
		}
	}
	return c.routeDecision(name, "task has no clear routing match")
}

func (c Config) routeDecision(name, reason string) (RouteDecision, error) {
	p, err := c.SelectProfile(name)
	if err != nil {
		return RouteDecision{}, err
	}
	return RouteDecision{Name: name, Profile: p, Reason: reason}, nil
}

func (c Config) validateRouting() error {
	for _, target := range []struct {
		field string
		name  string
	}{
		{"fast_profile", c.Routing.FastProfile},
		{"complex_profile", c.Routing.ComplexProfile},
	} {
		if target.name != "" {
			if _, err := c.SelectProfile(target.name); err != nil {
				return fmt.Errorf("routing.%s: %w", target.field, err)
			}
		}
	}
	return nil
}
