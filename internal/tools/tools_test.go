package tools

import (
	"context"
	"encoding/json"
	"kero/internal/provider"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditRequiresApprovalAndCreatesFile(t *testing.T) {
	root := t.TempDir()
	args, _ := json.Marshal(map[string]string{"path": "new.txt", "old": "", "new": "hello\n"})
	call := provider.ToolCall{Function: provider.FunctionCall{Name: "edit_file", Arguments: string(args)}}
	result := Execute(context.Background(), root, "ask", call, func(string) bool { return false })
	if !strings.Contains(result, "denied") {
		t.Fatalf("expected denial: %s", result)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied edit created file: %v", err)
	}
	result = Execute(context.Background(), root, "ask", call, func(string) bool { return true })
	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("creation failed: %s %v %q", result, err, data)
	}
}
func TestRejectOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	args, _ := json.Marshal(map[string]string{"path": "../outside", "old": "", "new": "x"})
	result := Execute(context.Background(), root, "auto", provider.ToolCall{Function: provider.FunctionCall{Name: "edit_file", Arguments: string(args)}}, nil)
	if !strings.Contains(result, "Error:") {
		t.Fatalf("outside write allowed: %s", result)
	}
}
