package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"kero/internal/provider"
)

// Summary describes a session available to resume.
type Summary struct {
	ID      string
	Updated time.Time
	Preview string
}

// List returns readable sessions belonging to root, most recently modified first.
// Invalid or incomplete logs are omitted without modifying them.
func List(root string) ([]Summary, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve session root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve session root: %w", err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find session directory: %w", err)
	}
	dir := filepath.Join(config, "kero", "sessions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Summary{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session directory: %w", err)
	}
	result := make([]Summary, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if !validID(id) || strings.Contains(id, "-incomplete-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		preview, ok := listPreview(filepath.Join(dir, name), root)
		if ok {
			result = append(result, Summary{ID: id, Updated: info.ModTime(), Preview: preview})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Updated.Equal(result[j].Updated) {
			return result[i].ID < result[j].ID
		}
		return result[i].Updated.After(result[j].Updated)
	})
	return result, nil
}

func listPreview(path, root string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	first := true
	foundUser := false
	preview := ""
	var pending []string
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return preview, errors.Is(err, io.EOF) && len(line) == 0 && !first
		}
		var e event
		if json.Unmarshal(line, &e) != nil || e.Version != eventVersion {
			return "", false
		}
		if first {
			first = false
			var info sessionInfo
			if e.Kind != "session" || json.Unmarshal(e.Data, &info) != nil || info.Root == "" ||
				(info.Root != root && !(runtime.GOOS == "windows" && strings.EqualFold(info.Root, root))) {
				return "", false
			}
			continue
		}
		switch e.Kind {
		case "session":
			return "", false
		case "message":
			var message provider.Message
			if json.Unmarshal(e.Data, &message) != nil {
				return "", false
			}
			pending, err = nextPending(pending, message)
			if err != nil {
				return "", false
			}
			if !foundUser && message.Role == "user" {
				foundUser = true
				preview = strings.Join(strings.Fields(message.Content), " ")
				runes := []rune(preview)
				if len(runes) > 100 {
					preview = string(runes[:100]) + "…"
				}
			}
		default:
			if strings.TrimSpace(e.Kind) == "" {
				return "", false
			}
		}
	}
}
