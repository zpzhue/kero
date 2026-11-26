package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kero/internal/provider"
)

func TestListSessions(t *testing.T) {
	config := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", config)
	case "darwin":
		t.Setenv("HOME", config)
	default:
		t.Setenv("XDG_CONFIG_HOME", config)
	}
	root := t.TempDir()
	got, err := List(root)
	if err != nil || len(got) != 0 {
		t.Fatalf("missing directory: got %#v, %v", got, err)
	}
	first, _, err := Open("", root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := first.AppendMessage(provider.Message{Role: "user", Content: "  hello\n  world  "}); err != nil {
		t.Fatal(err)
	}
	if err := first.AppendMessage(provider.Message{Role: "user", Content: "later"}); err != nil {
		t.Fatal(err)
	}
	second, _, err := Open("", root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.AppendMessage(provider.Message{Role: "user", Content: strings.Repeat("界", 101)}); err != nil {
		t.Fatal(err)
	}
	other, _, err := Open("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	newer := old.Add(time.Minute)
	if err := os.Chtimes(first.file.Name(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(second.file.Name(), newer, newer); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(first.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(first.file.Name())
	for name, data := range map[string][]byte{
		"backup-incomplete-123.jsonl": original,
		"corrupt.jsonl":               append(append([]byte{}, original...), []byte("bad\n")...),
		"partial.jsonl":               append(append([]byte{}, original...), []byte("{\"version\":")...),
		"empty.jsonl":                 nil,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err = List(filepath.Join(root, "."))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != second.ID() || got[1].ID != first.ID() {
		t.Fatalf("unexpected sessions: %#v", got)
	}
	if !got[0].Updated.Equal(newer) || !got[1].Updated.Equal(old) {
		t.Fatalf("unexpected modification times: %#v", got)
	}
	if got[0].Preview != strings.Repeat("界", 100)+"…" || got[1].Preview != "hello world" {
		t.Fatalf("unexpected previews: %#v", got)
	}
	after, err := os.ReadFile(filepath.Join(dir, "partial.jsonl"))
	if err != nil || string(after) != string(original)+"{\"version\":" {
		t.Fatalf("partial log changed: %q, %v", after, err)
	}
}
