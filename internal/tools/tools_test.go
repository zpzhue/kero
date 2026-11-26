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

func TestReadLineRanges(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "file.txt", "first\r\n\r\nthird\rlast")
	var workspace Workspace
	for _, tc := range []struct {
		name, args, want string
	}{
		{"all", `{"path":"file.txt"}`, "first\r\n\r\nthird\rlast"},
		{"inclusive", `{"path":"file.txt","start_line":2,"end_line":3}`, "\r\nthird\r"},
		{"start only", `{"path":"file.txt","start_line":4}`, "last"},
		{"end only", `{"path":"file.txt","end_line":1}`, "first\r\n"},
		{"end past EOF", `{"path":"file.txt","start_line":4,"end_line":99}`, "last"},
		{"start past EOF", `{"path":"file.txt","start_line":5}`, "Error: start_line is past the end of the file"},
		{"zero", `{"path":"file.txt","start_line":0}`, "Error: start_line and end_line must be positive"},
		{"reversed", `{"path":"file.txt","start_line":3,"end_line":2}`, "Error: start_line must be at or before end_line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspace.Execute(context.Background(), root, "auto", testCall("read_file", tc.args), nil); got != tc.want {
				t.Fatalf("read_file = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSearchRegexpAndIgnores(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".gitignore":           "*.log\n!keep.log\nbuild/\n/root-only\ncache/**/generated\n",
		".git/config":          "item 1\n",
		"build/ignored.txt":    "item 2\n",
		"debug.log":            "item 3\n",
		"keep.log":             "item 4\n",
		"nested/.gitignore":    "local.txt\n",
		"nested/local.txt":     "item 5\n",
		"nested/root-only":     "item 6\n",
		"other/local.txt":      "item 7\n",
		"root-only":            "item 8\n",
		"cache/generated":      "item 9\n",
		"cache/deep/generated": "item 10\n",
		"regular.txt":          "skip\nitem [0-9]+\nitem 11\n",
	}
	for path, data := range files {
		writeFixture(t, root, path, data)
	}
	var workspace Workspace
	for _, tc := range []struct {
		name, args, want string
	}{
		{"regexp", `{"query":"^item [0-9]+$","regexp":true}`, "keep.log:1:item 4\nnested/root-only:1:item 6\nother/local.txt:1:item 7\nregular.txt:3:item 11\n"},
		{"literal", `{"query":"item [0-9]+"}`, "regular.txt:2:item [0-9]+\n"},
		{"ignored direct file", `{"path":"nested/local.txt","query":"item"}`, ""},
		{"ignored ancestor", `{"path":"build/ignored.txt","query":"item"}`, ""},
		{"ignored direct directory", `{"path":"build","query":"item"}`, ""},
		{"git directory", `{"path":".git","query":"item"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspace.Execute(context.Background(), root, "auto", testCall("search", tc.args), nil); got != tc.want {
				t.Fatalf("search = %q, want %q", got, tc.want)
			}
		})
	}
	if got := workspace.Execute(context.Background(), root, "auto", testCall("search", `{"query":"[","regexp":true}`), nil); !strings.Contains(got, "invalid regular expression") {
		t.Fatalf("search accepted invalid regexp: %s", got)
	}
}

func TestWorkspaceRejectsStaleEdits(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "file.txt", "before\ninitial\n")
	ctx := context.Background()
	var first, second Workspace
	read := testCall("read_file", `{"path":"file.txt","end_line":1}`)
	edit := testCall("edit_file", `{"path":"file.txt","old":"before","new":"after"}`)
	if got := first.Execute(ctx, root, "auto", edit, nil); !strings.Contains(got, "read_file before editing") {
		t.Fatalf("edit without read: %s", got)
	}
	if got := first.Execute(ctx, root, "auto", read, nil); got != "before\n" {
		t.Fatalf("read: %q", got)
	}
	if got := second.Execute(ctx, root, "auto", edit, nil); !strings.Contains(got, "read_file before editing") {
		t.Fatalf("different workspace used another workspace's snapshot: %s", got)
	}
	writeFixture(t, root, "file.txt", "before\nchanged\n")
	if got := first.Execute(ctx, root, "auto", edit, nil); !strings.Contains(got, "conflict") {
		t.Fatalf("edit with stale read: %s", got)
	}
	if got := first.Execute(ctx, root, "auto", read, nil); got != "before\n" {
		t.Fatalf("reread: %q", got)
	}
	if got := first.Execute(ctx, root, "ask", edit, func(string) bool {
		writeFixture(t, root, "file.txt", "before\nchanged during approval\n")
		return true
	}); !strings.Contains(got, "conflict") {
		t.Fatalf("edit with approval-time change: %s", got)
	}
	assertFile := func(want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, "file.txt"))
		if err != nil || string(data) != want {
			t.Fatalf("file = %q, %v, want %q", data, err, want)
		}
	}
	assertFile("before\nchanged during approval\n")
	if got := first.Execute(ctx, root, "auto", read, nil); got != "before\n" {
		t.Fatalf("reread: %q", got)
	}
	if got := first.Execute(ctx, root, "auto", edit, nil); !strings.HasPrefix(got, "Edit applied.") {
		t.Fatalf("edit after reread: %s", got)
	}
	next := testCall("edit_file", `{"path":"file.txt","old":"after","new":"final"}`)
	if got := first.Execute(ctx, root, "auto", next, nil); !strings.HasPrefix(got, "Edit applied.") {
		t.Fatalf("second edit with updated snapshot: %s", got)
	}
	assertFile("final\nchanged during approval\n")
}

func TestWorkspaceCreateDoesNotOverwriteConcurrentFile(t *testing.T) {
	root := t.TempDir()
	var workspace Workspace
	call := testCall("edit_file", `{"path":"new.txt","old":"","new":"tool contents"}`)
	got := workspace.Execute(context.Background(), root, "ask", call, func(string) bool {
		writeFixture(t, root, "new.txt", "external contents")
		return true
	})
	if !strings.Contains(got, "conflict") {
		t.Fatalf("edit did not detect concurrent creation: %s", got)
	}
	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil || string(data) != "external contents" {
		t.Fatalf("concurrent file = %q, %v", data, err)
	}
}

func testCall(name, args string) provider.ToolCall {
	return provider.ToolCall{Function: provider.FunctionCall{Name: name, Arguments: args}}
}

func writeFixture(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}
